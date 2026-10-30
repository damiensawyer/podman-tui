package loadchart

// Podman glue for the load chart collector: it adapts the podman bindings
// types to the podman agnostic seams of charts.Collector. Keeping the
// glue in its own package leaves ui/charts free of podman dependencies.

import (
	"context"
	"strconv"
	"strings"

	pdcscontainers "github.com/containers/podman-tui/pdcs/containers"
	"github.com/containers/podman-tui/pdcs/hoststats"
	"github.com/containers/podman-tui/pdcs/registry"
	"github.com/containers/podman-tui/pdcs/sysinfo"
	"github.com/containers/podman-tui/ui/charts"
	"github.com/containers/podman/v5/libpod/define"
	"github.com/containers/podman/v5/pkg/bindings/containers"
)

// NewCollector returns a collector wired to the live podman connection:
// container stats streaming, container listing, host memory total and,
// for local unix:// connections, host CPU sampling.
func NewCollector(chart *charts.Chart, scope charts.Scope, target charts.ScopeTarget) *charts.Collector {
	collector := charts.NewCollector(chart, scope, target)

	collector.SetStreamFunc(streamAdapter)
	collector.SetListFunc(listAdapter)
	collector.SetMemTotalFunc(memTotalAdapter)
	collector.SetProcessFunc(processAdapter)

	if registry.ConnectionURI() == "" || strings.HasPrefix(registry.ConnectionURI(), "unix://") {
		collector.SetLocalHost(true)
		collector.SetHostCPUFunc(hoststats.NewReader().Sample)
	}

	return collector
}

// streamAdapter runs the podman container stats stream and converts its
// reports. The collector cancels its context to stop the stream; the
// podman stream keeps draining until the bindings close it, which this
// adapter forwards before closing its own channel.
func streamAdapter(ctx context.Context, ids []string, intervalSec int) (<-chan charts.SampleReport, error) {
	conn, err := registry.GetConnection()
	if err != nil {
		return nil, err
	}

	// stop the podman stream when the collector cancels its context
	podmanCtx, stop := context.WithCancel(conn)
	go func() {
		<-ctx.Done()
		stop()
	}()

	opts := new(containers.StatsOptions)

	stream := true
	opts.Stream = &stream
	opts.Interval = &intervalSec

	reportChan, err := containers.Stats(podmanCtx, ids, opts)
	if err != nil {
		stop()

		return nil, err
	}

	out := make(chan charts.SampleReport)

	go func() {
		defer close(out)

		for report := range reportChan {
			out <- charts.SampleReport{
				Error: report.Error,
				Stats: convertStats(report.Stats),
			}
		}
	}()

	return out, nil
}

// convertStats maps bindings stats reports to collector samples.
func convertStats(stats []define.ContainerStats) []charts.ContainerSample {
	samples := make([]charts.ContainerSample, 0, len(stats))

	for _, stat := range stats {
		samples = append(samples, charts.ContainerSample{
			ID:       stat.ContainerID,
			Name:     stat.Name,
			CPU:      stat.CPU,
			MemUsage: stat.MemUsage,
		})
	}

	return samples
}

// listAdapter converts the podman container listing.
func listAdapter() ([]charts.ContainerInfo, error) {
	listing, err := pdcscontainers.List()
	if err != nil {
		return nil, err
	}

	result := make([]charts.ContainerInfo, 0, len(listing))

	for _, item := range listing {
		name := item.ID
		if len(item.Names) > 0 {
			name = item.Names[0]
		}

		result = append(result, charts.ContainerInfo{
			ID:      item.ID,
			Name:    name,
			Pod:     item.Pod,
			PodName: item.PodName,
			IsInfra: item.IsInfra,
			Running: item.State == "running",
		})
	}

	return result, nil
}

// memTotalAdapter reads the host memory total once per dialog.
func memTotalAdapter() (uint64, error) {
	info, err := sysinfo.SysInfo()
	if err != nil {
		return 0, err
	}

	return info.MemTotal, nil
}

// processAdapter polls the process tables of the given containers via
// the podman top endpoint. Per container failures are tolerated while
// at least one container reports.
func processAdapter(ids []string) ([]charts.ProcessSample, error) {
	samples := make([]charts.ProcessSample, 0, len(ids)*8)

	var firstErr error

	for _, id := range ids {
		report, err := pdcscontainers.Top(id)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}

			continue
		}

		samples = append(samples, convertProcesses(id, report)...)
	}

	if len(samples) == 0 && firstErr != nil {
		return nil, firstErr
	}

	return samples, nil
}

// convertProcesses maps podman top rows to process samples. The first
// row is the header and rows are ordered USER PID PPID CPU ELAPSED TTY
// TIME COMMAND.
func convertProcesses(id string, report [][]string) []charts.ProcessSample {
	samples := make([]charts.ProcessSample, 0, len(report))

	for i, row := range report {
		if i == 0 || len(row) < 8 { //nolint:mnd
			continue
		}

		cpu, err := strconv.ParseFloat(row[3], 64)
		if err != nil {
			continue
		}

		samples = append(samples, charts.ProcessSample{
			ContainerID: id,
			PID:         row[1],
			Name:        strings.Join(row[7:], " "),
			CPU:         cpu,
		})
	}

	return samples
}
