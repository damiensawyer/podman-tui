//go:build linux

package hoststats

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	procStatPath      = "/proc/stat"
	cgroupCPUStatPath = "/sys/fs/cgroup/cpu.stat"
)

// Reader produces host CPU usage samples from consecutive snapshots.
// It is not safe for concurrent use; the owner serialises access.
type Reader struct {
	prevTotal   uint64 // /proc/stat: total jiffies
	prevBusy    uint64 // /proc/stat: non idle jiffies
	prevUsage   uint64 // cgroup v2: usage_usec
	prevWall    time.Time
	hasSnapshot bool
}

// NewReader returns a new host CPU reader.
func NewReader() *Reader {
	return &Reader{}
}

// Sample returns the host CPU usage in percent since the previous call.
// The first call primes the baseline and returns 0. It prefers the cgroup
// v2 cpu.stat usage counter and falls back to a /proc/stat delta.
func (r *Reader) Sample() (float64, error) {
	now := time.Now()

	if usage, ok := readCgroupUsage(); ok {
		defer r.storeCgroup(usage, now)

		if !r.hasSnapshot {
			return 0, nil
		}

		elapsed := now.Sub(r.prevWall)
		if elapsed <= 0 {
			return 0, nil
		}

		cores := float64(runtime.NumCPU())
		percent := float64(usage-r.prevUsage) / (elapsed.Seconds() * cores * 1e6) * 100 //nolint:mnd

		return clampPercent(percent), nil
	}

	total, busy, ok := readProcStat()
	if !ok {
		return 0, ErrUnavailable
	}

	defer r.storeProc(total, busy)

	if !r.hasSnapshot {
		return 0, nil
	}

	return CPUPercentFromDeltas(r.prevTotal, r.prevBusy, total, busy), nil
}

func (r *Reader) storeCgroup(usage uint64, now time.Time) {
	r.prevUsage = usage
	r.prevWall = now
	r.hasSnapshot = true
}

func (r *Reader) storeProc(total, busy uint64) {
	r.prevTotal = total
	r.prevBusy = busy
	r.hasSnapshot = true
}

// readCgroupUsage returns the cgroup v2 usage_usec counter.
func readCgroupUsage() (uint64, bool) {
	data, err := os.ReadFile(cgroupCPUStatPath) //nolint:gosec
	if err != nil {
		return 0, false
	}

	return parseCgroupUsage(data)
}

// parseCgroupUsage extracts usage_usec from cpu.stat content.
func parseCgroupUsage(data []byte) (uint64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			usage, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, false
			}

			return usage, true
		}
	}

	return 0, false
}

// readProcStat returns the total and non idle jiffies of the first "cpu"
// line of /proc/stat.
func readProcStat() (uint64, uint64, bool) {
	data, err := os.ReadFile(procStatPath) //nolint:gosec
	if err != nil {
		return 0, 0, false
	}

	return parseProcStat(data)
}

// parseProcStat extracts total and busy (non idle) jiffies from the
// "cpu" aggregate line of /proc/stat content.
func parseProcStat(data []byte) (uint64, uint64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 { //nolint:mnd
			return 0, 0, false
		}

		var (
			total uint64
			idle  uint64
		)

		for i, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return 0, 0, false
			}

			total += value

			// field 3 is idle, field 4 is iowait (0 indexed after
			// dropping the "cpu" label)
			if i == 3 || i == 4 { //nolint:mnd
				idle += value
			}
		}

		return total, total - idle, true
	}

	return 0, 0, false
}

// CPUPercentFromDeltas converts two /proc/stat snapshots into a CPU usage
// percentage. Counter resets (CPU hotplug, cgroup migration) yield 0.
func CPUPercentFromDeltas(prevTotal, prevBusy, total, busy uint64) float64 {
	if total < prevTotal || busy < prevBusy {
		return 0
	}

	totalDelta := total - prevTotal
	busyDelta := busy - prevBusy

	if totalDelta == 0 {
		return 0
	}

	return clampPercent(float64(busyDelta) / float64(totalDelta) * 100) //nolint:mnd
}

// clampPercent restricts a percentage to [0, 100]; counters can jump
// backwards after CPU hotplug or cgroup migration.
func clampPercent(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}

	return math.Min(v, 100)
}
