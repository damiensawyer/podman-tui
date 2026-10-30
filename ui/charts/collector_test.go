package charts_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/containers/podman-tui/ui/charts"
)

// fakeStream is a collector stats stream the test controls. It emulates
// the bindings channel contract: on context cancellation the producer
// sends one final error report on the unbuffered channel and only then
// closes it.
type fakeStream struct {
	mu    sync.Mutex
	opens int
	ids   []string
	ctxs  []context.Context
	chans []chan charts.SampleReport
}

func (f *fakeStream) open(ctx context.Context, ids []string, _ int) (<-chan charts.SampleReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.opens++
	f.ids = append([]string(nil), ids...)
	f.ctxs = append(f.ctxs, ctx)

	ch := make(chan charts.SampleReport)
	f.chans = append(f.chans, ch)

	go func() {
		<-ctx.Done()

		ch <- charts.SampleReport{Error: errStreamCancelled}
		close(ch)
	}()

	return ch, nil
}

func (f *fakeStream) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.opens
}

func (f *fakeStream) streamedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.ids
}

func (f *fakeStream) lastChan() chan charts.SampleReport {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.chans[len(f.chans)-1]
}

func (f *fakeStream) lastCtx() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.ctxs[len(f.ctxs)-1]
}

// send pushes one report carrying the given samples into the active
// stream channel, mirroring the bindings which batch every streamed
// container into a single report per interval. It blocks until the
// collector's reader consumes it.
func (f *fakeStream) send(samples ...charts.ContainerSample) {
	f.lastChan() <- charts.SampleReport{Stats: samples}
}

// sendOne pushes a single container sample.
func (f *fakeStream) sendOne(id, name string, cpu float64, mem uint64) {
	f.send(charts.ContainerSample{ID: id, Name: name, CPU: cpu, MemUsage: mem})
}

// errStreamCancelled is the final report error emulating a cancelled
// bindings stream.
var errStreamCancelled = errors.New("stream cancelled") //nolint:err113

// fakeListing is a mutable container listing.
type fakeListing struct {
	mu    sync.Mutex
	items []charts.ContainerInfo
}

func (l *fakeListing) set(items []charts.ContainerInfo) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.items = items
}

func (l *fakeListing) list() ([]charts.ContainerInfo, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]charts.ContainerInfo(nil), l.items...), nil
}

func listInfo(id, pod, podName string, infra, running bool) charts.ContainerInfo {
	return charts.ContainerInfo{
		ID:      id,
		Name:    id,
		Pod:     pod,
		PodName: podName,
		IsInfra: infra,
		Running: running,
	}
}

// seriesValue returns the last sample of a series, or NaN.
func seriesValue(chart *charts.Chart, id string) float64 {
	for _, info := range chart.SeriesInfos() {
		if info.ID == id {
			return info.Value
		}
	}

	return math.NaN()
}

// seriesColor returns the colour of a series, or ColorDefault.
func seriesColor(chart *charts.Chart, id string) (tcell.Color, bool) {
	for _, info := range chart.SeriesInfos() {
		if info.ID == id {
			return info.Color, true
		}
	}

	return tcell.ColorDefault, false
}

var _ = Describe("Collector", func() {
	newTestCollector := func(scope charts.Scope, target charts.ScopeTarget) (*charts.Collector, *charts.Chart, *fakeStream, *fakeListing) {
		chart := charts.NewChart()
		collector := charts.NewCollector(chart, scope, target)

		stream := &fakeStream{}
		listing := &fakeListing{}

		collector.SetStreamFunc(stream.open)
		collector.SetListFunc(listing.list)
		collector.SetInterval(2 * time.Millisecond)

		return collector, chart, stream, listing
	}

	It("starts, ingests samples and stops without leaking goroutines", func() {
		collector, chart, stream, listing := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})

		before := runtime.NumGoroutine()

		collector.Start()
		Expect(stream.openCount()).To(Equal(0)) // stream opens asynchronously

		Eventually(stream.openCount, "2s").Should(Equal(1))
		Expect(stream.streamedIDs()).To(Equal([]string{"a"}))

		stream.sendOne("a", "a", 42, 1000)

		Eventually(func() float64 { return seriesValue(chart, "cnt:a") }, "2s").
			Should(Equal(42.0))

		collector.Stop()

		// the tick and reader goroutines are gone
		Eventually(runtime.NumGoroutine, "2s").Should(BeNumerically("<=", before+1))
	})

	It("drains the final error report after cancellation without surfacing it", func() {
		collector, chart, stream, listing := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})

		errorCount := 0
		collector.SetErrorHandler(func(error) { errorCount++ })

		before := runtime.NumGoroutine()

		collector.Start()
		Eventually(stream.openCount, "2s").Should(Equal(1))

		stream.sendOne("a", "a", 1, 1)
		Eventually(func() float64 { return seriesValue(chart, "cnt:a") }, "2s").
			Should(Equal(1.0))

		// Stop cancels the stream context: the fake's producer goroutine
		// then sends its final error report on the unbuffered channel and
		// closes it. If the collector abandoned the reader instead of
		// draining, that send blocks forever and the goroutine baseline
		// below never recovers.
		collector.Stop()

		// the final error was swallowed, not surfaced
		Expect(errorCount).To(Equal(0))

		// the stream context was cancelled
		Eventually(func() bool {
			return stream.lastCtx().Err() != nil
		}, "2s").Should(BeTrue())

		// the reader goroutine is gone
		Eventually(runtime.NumGoroutine, "2s").Should(BeNumerically("<=", before+1))
	})

	It("evicts series after a full window of absence", func() {
		collector, chart, stream, listing := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})

		collector.Start()
		Eventually(stream.openCount, "2s").Should(Equal(1))

		stream.sendOne("a", "a", 5, 100)
		Eventually(func() float64 { return seriesValue(chart, "cnt:a") }, "2s").
			Should(Equal(5.0))

		// the container disappears from the listing
		listing.set(nil)

		// 60 absent ticks at 2ms plus slack; only the total remains
		Eventually(func() int { return len(chart.SeriesInfos()) }, "3s").
			Should(Equal(1))
	})

	It("restarts the stream exactly once on container churn and keeps history", func() {
		collector, chart, stream, listing := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})

		collector.Start()
		Eventually(stream.openCount, "2s").Should(Equal(1))

		stream.sendOne("a", "a", 7, 100)
		Eventually(func() float64 { return seriesValue(chart, "cnt:a") }, "2s").
			Should(Equal(7.0))

		historyBefore := chart.SeriesCount("cnt:a")
		Expect(historyBefore).To(BeNumerically(">", 0))

		// a new container appears: one restart, history kept
		listing.set([]charts.ContainerInfo{
			listInfo("a", "", "", false, true),
			listInfo("b", "", "", false, true),
		})

		Eventually(stream.openCount, "2s").Should(Equal(2))
		Expect(stream.streamedIDs()).To(ConsistOf("a", "b"))

		Consistently(stream.openCount, "100ms").Should(Equal(2))

		stream.sendOne("a", "a", 8, 100)
		Eventually(func() float64 { return seriesValue(chart, "cnt:a") }, "2s").
			Should(Equal(8.0))

		// the existing ring buffer was not reset
		Expect(chart.SeriesCount("cnt:a")).To(BeNumerically(">", historyBefore))
	})

	It("includes infra containers in pod scope streams but excludes them from totals at system scope", func() {
		podCollector, _, podStream, podListing := newTestCollector(
			charts.ScopePod, charts.ScopeTarget{PodID: "p1", PodName: "web"})

		podListing.set([]charts.ContainerInfo{
			listInfo("c1", "p1", "web", false, true),
			listInfo("infra", "p1", "web", true, true),
		})

		podCollector.Start()
		Eventually(podStream.openCount, "2s").Should(Equal(1))
		Expect(podStream.streamedIDs()).To(ConsistOf("c1", "infra"))
		podCollector.Stop()

		sysCollector, _, sysStream, sysListing := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		sysListing.set([]charts.ContainerInfo{
			listInfo("c1", "p1", "web", false, true),
			listInfo("infra", "p1", "web", true, true),
		})

		sysCollector.Start()
		Eventually(sysStream.openCount, "2s").Should(Equal(1))
		Expect(sysStream.streamedIDs()).To(ConsistOf("c1"))
		sysCollector.Stop()
	})

	It("aggregates pod series and skips infra series", func() {
		collector, chart, stream, listing := newTestCollector(
			charts.ScopePod, charts.ScopeTarget{PodID: "p1", PodName: "web"})

		listing.set([]charts.ContainerInfo{
			listInfo("c1", "p1", "web", false, true),
			listInfo("infra", "p1", "web", true, true),
		})

		collector.Start()
		Eventually(stream.openCount, "2s").Should(Equal(1))

		// both containers report in one batched stream report, like the
		// bindings do; once the churn has mapped the pods, the ingest
		// sums them into the pod total.
		stop := make(chan struct{})

		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					stream.send(
						charts.ContainerSample{ID: "c1", Name: "c1", CPU: 10, MemUsage: 100},
						charts.ContainerSample{ID: "infra", Name: "infra", CPU: 1, MemUsage: 50},
					)
				}
			}
		}()

		Eventually(func() float64 { return seriesValue(chart, "pod:web") }, "2s").
			Should(Equal(11.0)) // container + infra counted in the pod total

		close(stop)

		_, exists := seriesColor(chart, "cnt:infra")
		Expect(exists).To(BeFalse()) // no own series for infra

		collector.Stop()
	})

	It("pushes host CPU samples as the local system total", func() {
		collector, chart, _, listing := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		listing.set(nil)
		collector.SetLocalHost(true)
		collector.SetHostCPUFunc(func() (float64, error) { return 55.5, nil })

		collector.Start()

		Eventually(func() float64 { return seriesValue(chart, "total") }, "2s").
			Should(Equal(55.5))

		collector.Stop()
	})

	It("pauses and resumes ingestion", func() {
		collector, chart, stream, listing := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})

		collector.Start()
		Eventually(stream.openCount, "2s").Should(Equal(1))

		stream.sendOne("a", "a", 42, 100)
		Eventually(func() float64 { return seriesValue(chart, "cnt:a") }, "2s").
			Should(Equal(42.0))

		collector.SetPaused(true)

		stream.sendOne("a", "a", 99, 100)
		Consistently(func() float64 { return seriesValue(chart, "cnt:a") }, "150ms").
			Should(Equal(42.0))

		collector.SetPaused(false)

		Eventually(func() float64 { return seriesValue(chart, "cnt:a") }, "2s").
			Should(Equal(99.0))

		collector.Stop()
	})

	It("applies group visibility and cycles scope aware", func() {
		collector, chart, stream, listing := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		listing.set([]charts.ContainerInfo{
			listInfo("a", "p1", "web", false, true),
		})

		collector.Start()
		Eventually(stream.openCount, "2s").Should(Equal(1))

		stream.sendOne("a", "a", 5, 100)

		Eventually(func() bool {
			_, ok := seriesColor(chart, "cnt:a")
			return ok
		}, "2s").Should(BeTrue())

		// default group is total: only the aggregate is visible
		Expect(chart.Visible("total")).To(BeTrue())
		Expect(chart.Visible("cnt:a")).To(BeFalse())

		collector.SetGroup(charts.GroupContainer)
		Expect(chart.Visible("cnt:a")).To(BeTrue())
		Expect(chart.Visible("total")).To(BeFalse())
		Expect(chart.Visible("pod:web")).To(BeFalse())

		// system scope cycles total -> pod -> container -> process -> total
		collector.SetGroup(charts.GroupContainer)
		Expect(collector.NextGroup()).To(Equal(charts.GroupProcess))
		Expect(collector.NextGroup()).To(Equal(charts.GroupTotal))

		collector.Stop()
	})

	It("resets buffers on metric switch and keeps colours stable", func() {
		collector, chart, stream, listing := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})

		collector.Start()
		Eventually(stream.openCount, "2s").Should(Equal(1))

		stream.sendOne("a", "a", 42, 2048)
		Eventually(func() float64 { return seriesValue(chart, "cnt:a") }, "2s").
			Should(Equal(42.0))

		colorBefore, ok := seriesColor(chart, "cnt:a")
		Expect(ok).To(BeTrue())

		collector.SetMetric(charts.MetricMemory)

		Expect(chart.Metric()).To(Equal(charts.MetricMemory))
		Expect(chart.SeriesCount("cnt:a")).To(Equal(0)) // buffers reset

		colorAfter, ok := seriesColor(chart, "cnt:a")
		Expect(ok).To(BeTrue())
		Expect(colorAfter).To(Equal(colorBefore)) // colour stable

		stream.sendOne("a", "a", 42, 4096)
		Eventually(func() float64 { return seriesValue(chart, "cnt:a") }, "2s").
			Should(Equal(4096.0)) // now plotting bytes

		collector.Stop()
	})

	It("fetches the host memory total once", func() {
		collector, _, _, _ := newTestCollector(
			charts.ScopeSystem, charts.ScopeTarget{})

		collector.SetMemTotalFunc(func() (uint64, error) {
			return 8 << 30, nil
		})

		collector.Start()

		Eventually(collector.MemTotal, "2s").Should(Equal(uint64(8 << 30)))

		collector.Stop()
	})

	It("does not restart the stream for a fixed container scope target", func() {
		collector, chart, stream, listing := newTestCollector(
			charts.ScopeContainer, charts.ScopeTarget{ContainerID: "a", ContainerName: "a"})

		listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})

		collector.Start()
		Eventually(stream.openCount, "2s").Should(Equal(1))

		stream.sendOne("a", "a", 3, 100)
		Eventually(func() float64 { return seriesValue(chart, "total") }, "2s").
			Should(Equal(3.0))

		Consistently(stream.openCount, "100ms").Should(Equal(1))

		collector.Stop()
	})
	Describe("Process mode", func() {
		It("keeps the top processes as individual series", func() {
			collector, chart, stream, listing := newTestCollector(
				charts.ScopeSystem, charts.ScopeTarget{})

			listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})

			collector.SetProcessFunc(func(ids []string) ([]charts.ProcessSample, error) {
				Expect(ids).To(ConsistOf("a"))

				return []charts.ProcessSample{
					{ContainerID: "a", PID: "100", Name: "nginx", CPU: 15},
					{ContainerID: "a", PID: "101", Name: "python", CPU: 8},
					{ContainerID: "a", PID: "102", Name: "sleep", CPU: 0.5},
				}, nil
			})

			collector.Start()
			Eventually(stream.openCount, "2s").Should(Equal(1))

			collector.SetGroup(charts.GroupProcess)

			Eventually(func() int { return chart.SeriesCount("proc:a:100") }, "2s").
				Should(BeNumerically(">", 0))
			Expect(chart.SeriesCount("proc:a:101")).To(BeNumerically(">", 0))
			Expect(chart.SeriesCount("proc:a:102")).To(BeNumerically(">", 0))

			// fewer processes than the top N: no "other" series
			_, exists := seriesColor(chart, "other")
			Expect(exists).To(BeFalse())

			// process curves always plot CPU
			Eventually(func() float64 { return seriesValue(chart, "proc:a:100") }, "2s").
				Should(Equal(15.0))

			collector.Stop()
		})

		It("aggregates processes beyond the top N and evicts departed ones", func() {
			collector, chart, stream, listing := newTestCollector(
				charts.ScopeSystem, charts.ScopeTarget{})

			listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})

			samples := make([]charts.ProcessSample, 0, 15)
			for i := 1; i <= 15; i++ {
				samples = append(samples, charts.ProcessSample{
					ContainerID: "a",
					PID:         fmt.Sprintf("%d", 100+i),
					Name:        fmt.Sprintf("proc%d", i),
					CPU:         float64(i),
				})
			}

			collector.SetProcessFunc(func([]string) ([]charts.ProcessSample, error) {
				return samples, nil
			})

			collector.Start()
			Eventually(stream.openCount, "2s").Should(Equal(1))

			collector.SetGroup(charts.GroupProcess)

			// the highest CPU process is kept, the lowest lands in "other"
			Eventually(func() int { return chart.SeriesCount("proc:a:115") }, "2s").
				Should(BeNumerically(">", 0))
			Eventually(func() int { return chart.SeriesCount("other") }, "2s").
				Should(BeNumerically(">", 0))
			Expect(chart.SeriesCount("proc:a:101")).To(Equal(0))

			// the top process departs: its series is evicted immediately
			// (the fake's slice was sorted in place by earlier ingests, so
			// dropping the head removes the highest CPU process)
			collector.SetProcessFunc(func([]string) ([]charts.ProcessSample, error) {
				return samples[1:], nil
			})

			Eventually(func() int { return chart.SeriesCount("proc:a:115") }, "2s").
				Should(Equal(0))

			collector.Stop()
		})

		It("includes process mode in every scope cycle", func() {
			collector, _, _, _ := newTestCollector(
				charts.ScopeContainer, charts.ScopeTarget{ContainerID: "a", ContainerName: "a"})

			Expect(collector.NextGroup()).To(Equal(charts.GroupProcess))
			Expect(collector.NextGroup()).To(Equal(charts.GroupTotal))
		})
	})
})
