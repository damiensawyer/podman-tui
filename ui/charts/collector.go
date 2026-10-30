package charts

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/containers/podman-tui/ui/style"
	"github.com/containers/podman-tui/ui/utils"
	"github.com/gdamore/tcell/v2"
	"github.com/rs/zerolog/log"
)

// Collector feeds a Chart from container stats streams. One collector
// runs per open load chart dialog.
//
// The collector engine is podman agnostic: it consumes ContainerSample
// and ContainerInfo values through replaceable function seams, and the
// podman specific adapters live in the ui/loadchart package. This keeps
// the whole ui/charts package free of podman dependencies.
//
// Stopping the collector cancels the stream context and then keeps
// draining the stream channel until its producer closes it, so the
// producer side never leaks. Ring buffers live in the chart, so a stream
// restart on container churn keeps the collected history.
type Collector struct {
	chart  *Chart
	scope  Scope
	target ScopeTarget

	// replaceable seams, typically wired by the ui/loadchart glue
	streamFunc    func(ctx context.Context, ids []string, intervalSec int) (<-chan SampleReport, error)
	listFunc      func() ([]ContainerInfo, error)
	hostCPUFunc   func() (float64, error)
	memTotalFunc  func() (uint64, error)
	processFunc   func(ids []string) ([]ProcessSample, error)
	localHost     bool
	interval      time.Duration
	sourceStarted bool

	memTotal uint64

	mu           sync.Mutex
	cancel       context.CancelFunc // active stream cancel; nil when closed
	readerWG     sync.WaitGroup
	tickWG       sync.WaitGroup
	doneChan     chan struct{}
	running      bool
	streamIDs    map[string]bool
	latest       map[string]ContainerSample
	nameOf       map[string]string // container id -> display name
	podOf        map[string]string // container id -> pod name ("" = no pod)
	infraOf      map[string]bool   // container id -> is infra
	podsKnown    map[string]bool   // pod names with series
	procKnown    map[string]bool   // process series ids with series
	absentTicks  map[string]int    // container id -> absent ticks
	podAbsent    map[string]int    // pod name -> absent ticks
	group        GroupMode
	metric       Metric
	paused       bool
	absentTarget int
	processTick  int
	errHandler   func(error)
}

// SampleReport is one report of the stats stream.
type SampleReport struct {
	// Error from reading the stream; Stats is empty when set.
	Error error
	// Stats carries the latest measurement of every streamed container.
	Stats []ContainerSample
}

// ContainerSample is the measurement of a single container.
type ContainerSample struct {
	ID       string
	Name     string
	CPU      float64 // percent
	MemUsage uint64  // bytes
}

// ContainerInfo is the container listing entry the collector needs.
type ContainerInfo struct {
	ID      string
	Name    string
	Pod     string // pod id, empty when the container has no pod
	PodName string // pod display name
	IsInfra bool
	Running bool
}

// ProcessSample is one process measurement from a container process
// table. CPU is the ps style lifetime average percentage the top
// endpoint reports (not an instantaneous delta).
type ProcessSample struct {
	ContainerID string
	PID         string
	Name        string
	CPU         float64
}

// NewCollector returns a collector for the given scope and target. The
// stream, listing and host seams must be wired before Start; see the
// ui/loadchart package for the podman wiring.
func NewCollector(chart *Chart, scope Scope, target ScopeTarget) *Collector {
	chart.SetLegendFilter(GroupTotal.owns)

	return &Collector{
		chart:       chart,
		scope:       scope,
		target:      target,
		interval:    utils.RefreshInterval,
		streamIDs:   make(map[string]bool),
		latest:      make(map[string]ContainerSample),
		nameOf:      make(map[string]string),
		podOf:       make(map[string]string),
		infraOf:     make(map[string]bool),
		podsKnown:   make(map[string]bool),
		procKnown:   make(map[string]bool),
		absentTicks: make(map[string]int),
		podAbsent:   make(map[string]int),
		group:       GroupTotal,
		metric:      MetricCPU,
	}
}

// SetStreamFunc wires the stats stream source. The context passed to the
// function is cancelled to stop the stream; the function must close the
// returned channel afterwards, and the collector keeps draining until it
// is closed.
func (c *Collector) SetStreamFunc(fn func(context.Context, []string, int) (<-chan SampleReport, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.streamFunc = fn
}

// SetListFunc wires the container listing source.
func (c *Collector) SetListFunc(fn func() ([]ContainerInfo, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.listFunc = fn
}

// SetHostCPUFunc wires the host CPU sampler; nil disables host CPU
// collection.
func (c *Collector) SetHostCPUFunc(fn func() (float64, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.hostCPUFunc = fn
}

// SetMemTotalFunc wires the host memory total source.
func (c *Collector) SetMemTotalFunc(fn func() (uint64, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.memTotalFunc = fn
}

// SetProcessFunc wires the process table source used by process mode.
func (c *Collector) SetProcessFunc(fn func(ids []string) ([]ProcessSample, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.processFunc = fn
}

// SetLocalHost marks the connection as local, which makes the system
// scope CPU total series carry host samples instead of the container
// sum.
func (c *Collector) SetLocalHost(local bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.localHost = local
}

// SetInterval overrides the collection tick interval.
func (c *Collector) SetInterval(interval time.Duration) {
	c.interval = interval
}

// SetErrorHandler registers a callback for collection errors, e.g. a
// lost connection while streaming.
func (c *Collector) SetErrorHandler(handler func(error)) {
	c.errHandler = handler
}

// MemTotal returns the host memory total in bytes, or 0 when unknown.
func (c *Collector) MemTotal() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.memTotal
}

// Start begins collection in a background goroutine. Errors surface
// through the error handler instead of blocking the caller.
func (c *Collector) Start() {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()

		return
	}

	c.running = true
	c.doneChan = make(chan struct{})
	doneChan := c.doneChan
	c.mu.Unlock()

	if c.memTotalFunc != nil {
		go c.fetchMemTotal()
	}

	c.tickWG.Go(func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// the total series exists from the start so aggregate and host
		// samples land even before the first container report.
		c.mu.Lock()
		c.ensureSeries()
		c.mu.Unlock()

		if ids := c.currentIDs(); len(ids) > 0 {
			c.openStream(ctx, ids)
		}

		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-doneChan:
				c.cancelStream()
				c.readerWG.Wait()

				return
			case <-ticker.C:
				c.tick(ctx)
			}
		}
	})
}

// Stop cancels the stream, drains the reader goroutine and stops the
// collection loop.
func (c *Collector) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()

		return
	}

	c.running = false
	close(c.doneChan)
	c.mu.Unlock()

	c.cancelStream()
	c.tickWG.Wait()
}

// cancelStream cancels the active stream context, if any, so its reader
// starts draining.
func (c *Collector) cancelStream() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// SetPaused freezes scrolling and frame ingestion; the stream keeps
// running so resume has no historical gap.
func (c *Collector) SetPaused(paused bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.paused = paused
}

// Group returns the active group mode.
func (c *Collector) Group() GroupMode {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.group
}

// NextGroup cycles to the next group mode available for the scope and
// applies the matching series visibility.
func (c *Collector) NextGroup() GroupMode {
	c.mu.Lock()
	defer c.mu.Unlock()

	available := c.scope.availableGroups()
	index := slices.Index(available, c.scope.resolve(c.group))

	c.group = available[(index+1)%len(available)]
	c.chart.SetLegendFilter(c.group.owns)
	c.applyVisibility()

	return c.group
}

// SetGroup switches the group mode (scope aware) and applies visibility.
func (c *Collector) SetGroup(group GroupMode) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.group = c.scope.resolve(group)
	c.chart.SetLegendFilter(c.group.owns)
	c.applyVisibility()
}

// SetMetric switches the plotted measurement. The chart buffers are
// metric specific, so they are reset and recreated; palette assignment
// keeps every series colour stable across the switch.
func (c *Collector) SetMetric(metric Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.metric == metric {
		return
	}

	c.metric = metric
	c.chart.SetMetric(metric)
	c.chart.Reset()
	c.ensureSeries()
	c.applyVisibility()
}

// fetchMemTotal reads the host memory total once per dialog.
func (c *Collector) fetchMemTotal() {
	c.mu.Lock()
	memTotalFunc := c.memTotalFunc
	c.mu.Unlock()

	total, err := memTotalFunc()
	if err != nil {
		log.Error().Msgf("load chart collector: memory total error: %v", err)

		return
	}

	c.mu.Lock()
	c.memTotal = total
	c.mu.Unlock()
}

// openStream replaces the active stats stream with one bound to ids.
// The previous stream is cancelled first; its reader keeps draining
// until the producer closes the channel.
func (c *Collector) openStream(ctx context.Context, ids []string) {
	c.cancelStream()

	c.mu.Lock()
	c.streamIDs = make(map[string]bool, len(ids))
	for _, id := range ids {
		c.streamIDs[id] = true
	}
	c.mu.Unlock()

	streamCtx, streamCancel := context.WithCancel(ctx)

	c.mu.Lock()
	c.cancel = streamCancel
	c.mu.Unlock()

	c.mu.Lock()
	streamFunc := c.streamFunc
	c.mu.Unlock()

	reportChan, err := streamFunc(streamCtx, ids, 1)
	if err != nil {
		streamCancel()

		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()

		c.reportError(err)

		return
	}

	c.readerWG.Go(func() {
		c.readReports(streamCtx, reportChan)
	})
}

// readReports consumes the stats stream until it is closed. Reports
// arriving after cancellation are drained and skipped, never surfaced.
// The caller owns the reader WaitGroup accounting.
func (c *Collector) readReports(ctx context.Context, reportChan <-chan SampleReport) {
	for report := range reportChan {
		if ctx.Err() != nil {
			continue
		}

		if report.Error != nil {
			continue
		}

		c.mu.Lock()
		for _, stat := range report.Stats {
			c.latest[stat.ID] = stat
		}
		c.mu.Unlock()
	}
}

// swapLatest returns the reports collected since the previous tick.
func (c *Collector) swapLatest() map[string]ContainerSample {
	c.mu.Lock()
	defer c.mu.Unlock()

	swapped := c.latest
	c.latest = make(map[string]ContainerSample)

	return swapped
}

// currentIDs returns the running container ids the stream should be bound
// to for the collector's scope.
func (c *Collector) currentIDs() []string {
	if c.scope == ScopeContainer {
		return []string{c.target.ContainerID}
	}

	c.mu.Lock()
	listFunc := c.listFunc
	c.mu.Unlock()

	listing, err := listFunc()
	if err != nil {
		c.reportError(err)

		return nil
	}

	ids := make([]string, 0, len(listing))
	for _, item := range listing {
		if !c.inScope(item) || !item.Running {
			continue
		}

		// pod scope streams include the infra container so its load
		// counts into the pod total; system scope skips it.
		if c.scope == ScopeSystem && item.IsInfra {
			continue
		}

		ids = append(ids, item.ID)
	}

	return ids
}

// tick performs one collection cycle: ingest samples, sample the host
// CPU and react to container churn.
func (c *Collector) tick(ctx context.Context) {
	c.mu.Lock()
	paused := c.paused
	c.mu.Unlock()

	if paused {
		return
	}

	c.ingest(c.swapLatest())
	c.sampleHostCPU()
	c.pollProcessesTick()

	if c.scope == ScopeContainer {
		c.trackTarget()
	} else {
		c.churn(ctx)
	}
}

// pollProcessesTick polls the process tables every processPollRate
// ticks while process mode is active; the top endpoint is one round
// trip per container so polling runs on a relaxed cycle.
func (c *Collector) pollProcessesTick() {
	c.mu.Lock()
	group := c.group
	processFunc := c.processFunc
	c.processTick++
	tick := c.processTick

	ids := make([]string, 0, len(c.streamIDs))
	for id := range c.streamIDs {
		ids = append(ids, id)
	}
	c.mu.Unlock()

	if group != GroupProcess || processFunc == nil || len(ids) == 0 {
		return
	}

	if tick%processPollRate != 0 {
		return
	}

	samples, err := processFunc(ids)
	if err != nil {
		c.reportError(err)

		return
	}

	c.ingestProcesses(samples)
}

// ingestProcesses keeps the top N processes as individual series and
// aggregates the remainder into the "other" series. Process series that
// leave the top set are evicted immediately; process curves always plot
// the ps style CPU percentage regardless of the active metric.
func (c *Collector) ingestProcesses(samples []ProcessSample) {
	c.mu.Lock()
	defer c.mu.Unlock()

	sortDesc(samples, func(sample ProcessSample) float64 { return sample.CPU })

	group := c.scope.resolve(c.group)

	keep := make(map[string]bool)
	other := 0.0

	for i, sample := range samples {
		id := procSeriesPrefix + sample.ContainerID + ":" + sample.PID

		if i < processTopN {
			keep[id] = true

			if !c.procKnown[id] {
				c.procKnown[id] = true
				c.chart.EnsureSeries(id, sample.Name, tcell.ColorDefault)
				c.chart.SetVisible(id, group == GroupProcess)
			}

			c.chart.Push(id, sample.CPU)

			continue
		}

		other += sample.CPU
	}

	if len(samples) > processTopN {
		keep[otherSeriesID] = true

		if !c.procKnown[otherSeriesID] {
			c.procKnown[otherSeriesID] = true
			c.chart.EnsureSeries(otherSeriesID, "other", tcell.ColorDefault)
			c.chart.SetVisible(otherSeriesID, group == GroupProcess)
		}

		c.chart.Push(otherSeriesID, other)
	}

	for id := range c.procKnown {
		if !keep[id] {
			c.chart.RemoveSeries(id)
			delete(c.procKnown, id)
		}
	}
}

// ingest pushes the current metric value of every reported container
// into the chart series and keeps the pod and total aggregates current.
func (c *Collector) ingest(reports map[string]ContainerSample) {
	c.mu.Lock()
	defer c.mu.Unlock()

	sum := 0.0
	hostBacked := c.scope == ScopeSystem && c.metric == MetricCPU && c.localHost
	group := c.scope.resolve(c.group)
	podSums := make(map[string]float64)

	for id, sample := range reports {
		value := c.valueOf(sample)
		sum += value

		// infra containers have no own series but count into totals.
		// a new series starts with its group default visibility; user
		// toggles made through the entity table are never overridden
		// here, so they persist for the dialog's lifetime.
		if !c.infraOf[id] {
			if _, known := c.nameOf[id]; !known {
				c.nameOf[id] = sample.Name
				c.chart.EnsureSeries(cntSeriesPrefix+id, sample.Name, tcell.ColorDefault)
				c.chart.SetVisible(cntSeriesPrefix+id, group == GroupContainer)
			}

			c.chart.Push(cntSeriesPrefix+id, value)
		}

		if pod := c.podOf[id]; pod != "" {
			if !c.podsKnown[pod] {
				c.podsKnown[pod] = true
				c.chart.EnsureSeries(podSeriesPrefix+pod, pod, tcell.ColorDefault)
				c.chart.SetVisible(podSeriesPrefix+pod, group == GroupPod && c.scope == ScopeSystem)
			}

			podSums[pod] += value
		}
	}

	// one push per pod with the aggregated tick value
	for pod, podSum := range podSums {
		c.chart.Push(podSeriesPrefix+pod, podSum)
	}

	// on local connections the system scope CPU total is the host
	// sample, pushed separately by sampleHostCPU. With no reports there
	// is nothing to aggregate: the last values age out of the window
	// naturally instead of being zeroed.
	if len(reports) > 0 && !hostBacked {
		c.chart.Push(totalSeriesID, sum)
	}
}

// valueOf returns the currently plotted measurement of a sample.
func (c *Collector) valueOf(sample ContainerSample) float64 {
	if c.metric == MetricMemory {
		return float64(sample.MemUsage)
	}

	return sample.CPU
}

// sampleHostCPU pushes one host CPU sample at system scope; the total
// series carries the host value on local connections.
func (c *Collector) sampleHostCPU() {
	c.mu.Lock()
	isHostTotal := c.scope == ScopeSystem && c.metric == MetricCPU && c.hostCPUFunc != nil
	c.mu.Unlock()

	if !isHostTotal {
		return
	}

	percent, err := c.hostCPUFunc()
	if err != nil {
		return
	}

	c.chart.Push(totalSeriesID, percent)
}

// churn diffs the container listing against the stream binding: it
// updates pod and infra knowledge, restarts the stream when the
// container set changes and ages out disappeared entities.
func (c *Collector) churn(ctx context.Context) {
	c.mu.Lock()
	listFunc := c.listFunc
	c.mu.Unlock()

	listing, err := listFunc()
	if err != nil {
		c.reportError(err)

		return
	}

	present := make(map[string]bool)
	presentPods := make(map[string]bool)

	c.mu.Lock()
	for _, item := range listing {
		if !c.inScope(item) {
			continue
		}

		if item.Running {
			present[item.ID] = true
		}

		if item.IsInfra {
			c.infraOf[item.ID] = true

			// self-heal: drop an infra series created before the
			// listing knowledge arrived.
			if _, known := c.nameOf[item.ID]; known {
				c.chart.RemoveSeries(cntSeriesPrefix + item.ID)
				delete(c.nameOf, item.ID)
			}
		} else {
			// ensure the series here too: churn usually runs before the
			// first report arrives, and a name marked known without a
			// series would block the lazy ensure in ingest.
			if _, known := c.nameOf[item.ID]; !known {
				c.chart.EnsureSeries(cntSeriesPrefix+item.ID, item.Name, tcell.ColorDefault)
				c.chart.SetVisible(cntSeriesPrefix+item.ID,
					c.scope.resolve(c.group) == GroupContainer)
			}

			c.nameOf[item.ID] = item.Name
		}

		if item.PodName != "" {
			c.podOf[item.ID] = item.PodName
			presentPods[item.PodName] = true

			// group default for a pod series that does not exist yet
			// (SetVisible records the pending state safely)
			if !c.podsKnown[item.PodName] {
				c.chart.SetVisible(podSeriesPrefix+item.PodName,
					c.scope.resolve(c.group) == GroupPod && c.scope == ScopeSystem)
			}
		} else {
			c.podOf[item.ID] = ""
		}
	}
	c.mu.Unlock()

	c.restartIfChanged(ctx, present)
	c.ageOut(present, presentPods)
}

// restartIfChanged reopens the stream when the set of running containers
// diverged from the active stream binding.
func (c *Collector) restartIfChanged(ctx context.Context, present map[string]bool) {
	c.mu.Lock()
	streamIDs := c.streamIDs
	changed := len(present) != len(streamIDs)

	if !changed {
		for id := range present {
			if !streamIDs[id] {
				changed = true

				break
			}
		}
	}
	c.mu.Unlock()

	if !changed {
		return
	}

	if ids := c.currentIDs(); len(ids) > 0 {
		c.openStream(ctx, ids)
	}
}

// inScope reports whether a listed container belongs to the collector's
// scope.
func (c *Collector) inScope(item ContainerInfo) bool {
	switch c.scope {
	case ScopePod:
		return idMatches(item.Pod, c.target.PodID)
	case ScopeContainer:
		return idMatches(item.ID, c.target.ContainerID)
	default:
		return !item.IsInfra
	}
}

// idMatches reports whether the full podman id matches the target id;
// the pages pass the truncated 12 character ids shown in their tables.
func idMatches(fullID, targetID string) bool {
	return targetID != "" && strings.HasPrefix(fullID, targetID)
}

// ageOut increments absence counters and evicts series after a full
// window of ticks without the entity appearing in the listing.
func (c *Collector) ageOut(present, presentPods map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for id := range c.nameOf {
		if present[id] {
			c.absentTicks[id] = 0

			continue
		}

		c.absentTicks[id]++
		if c.absentTicks[id] >= evictionTicks {
			c.chart.RemoveSeries(cntSeriesPrefix + id)
			delete(c.nameOf, id)
			delete(c.absentTicks, id)
			delete(c.podOf, id)
		}
	}

	for pod := range c.podsKnown {
		if presentPods[pod] {
			c.podAbsent[pod] = 0

			continue
		}

		c.podAbsent[pod]++
		if c.podAbsent[pod] >= evictionTicks {
			c.chart.RemoveSeries(podSeriesPrefix + pod)
			delete(c.podsKnown, pod)
			delete(c.podAbsent, pod)
		}
	}
}

// trackTarget ages out the single container scope target.
func (c *Collector) trackTarget() {
	c.mu.Lock()
	listFunc := c.listFunc
	c.mu.Unlock()

	listing, err := listFunc()
	if err != nil {
		c.reportError(err)

		return
	}

	present := false
	for _, item := range listing {
		if idMatches(item.ID, c.target.ContainerID) {
			present = true

			break
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if present {
		c.absentTarget = 0

		return
	}

	c.absentTarget++
	if c.absentTarget >= evictionTicks {
		// the series is keyed by the full id the stats stream reports
		for id := range c.nameOf {
			c.chart.RemoveSeries(cntSeriesPrefix + id)
		}

		c.chart.RemoveSeries(totalSeriesID)
	}
}

// ensureSeries recreates missing chart series after a buffer reset.
// Callers must hold the collector mutex.
func (c *Collector) ensureSeries() {
	c.chart.EnsureSeries(totalSeriesID, c.totalName(), style.FgColor)

	for id, name := range c.nameOf {
		if !c.infraOf[id] {
			c.chart.EnsureSeries(cntSeriesPrefix+id, name, tcell.ColorDefault)
		}
	}

	for pod := range c.podsKnown {
		c.chart.EnsureSeries(podSeriesPrefix+pod, pod, tcell.ColorDefault)
	}
}

// applyVisibility shows exactly the series of the active group mode.
// Callers must hold the collector mutex.
func (c *Collector) applyVisibility() {
	group := c.scope.resolve(c.group)

	c.chart.SetVisible(totalSeriesID, group == GroupTotal)

	for id := range c.nameOf {
		c.chart.SetVisible(cntSeriesPrefix+id, group == GroupContainer)
	}

	for pod := range c.podsKnown {
		c.chart.SetVisible(podSeriesPrefix+pod, group == GroupPod && c.scope == ScopeSystem)
	}
}

// totalName returns the display name of the aggregate series.
func (c *Collector) totalName() string {
	switch c.scope {
	case ScopePod:
		return c.target.PodName
	case ScopeContainer:
		return c.target.ContainerName
	default:
		if c.metric == MetricCPU && c.localHost {
			return "host"
		}

		return "podman total"
	}
}

// reportError forwards an error to the registered handler.
func (c *Collector) reportError(err error) {
	log.Error().Msgf("load chart collector: %v", err)

	c.mu.Lock()
	handler := c.errHandler
	c.mu.Unlock()

	if handler != nil {
		handler(err)
	}
}
