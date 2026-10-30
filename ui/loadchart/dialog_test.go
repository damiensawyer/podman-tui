package loadchart_test

import (
	"context"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rivo/tview"

	"github.com/containers/podman-tui/ui/charts"
	"github.com/containers/podman-tui/ui/loadchart"
)

// errResolve is the resolve error for a missing selection.
var errResolve = errResolveType{}

type errResolveType struct{}

func (errResolveType) Error() string { return "no selection" }

// fakeStream is a controlled stats stream emulating the bindings channel
// contract: final error report then close on cancellation.
type fakeStream struct {
	mu       sync.Mutex
	failOpen bool
	opens    int
	ctxs     []context.Context
	chans    []chan charts.SampleReport
}

// errOpenFailed is the open failure used by the banner test.
var errOpenFailed = errOpenFailedType{}

type errOpenFailedType struct{}

func (errOpenFailedType) Error() string { return "open failed" }

func (f *fakeStream) open(ctx context.Context, ids []string, _ int) (<-chan charts.SampleReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failOpen {
		return nil, errOpenFailed
	}

	f.opens++
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

// lastCtx returns the stream context of the most recent open.
func (f *fakeStream) lastCtx() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.ctxs[len(f.ctxs)-1]
}

func (f *fakeStream) sendOne(id, name string, cpu float64, mem uint64) {
	f.mu.Lock()
	ch := f.chans[len(f.chans)-1]
	f.mu.Unlock()

	ch <- charts.SampleReport{
		Stats: []charts.ContainerSample{{ID: id, Name: name, CPU: cpu, MemUsage: mem}},
	}
}

// errStreamCancelled is the final report error of a cancelled stream.
var errStreamCancelled = errStreamCancelledType{}

type errStreamCancelledType struct{}

func (errStreamCancelledType) Error() string { return "stream cancelled" }

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

// listInfo builds a container listing entry.
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

// sendKey delivers a rune key press to the dialog input handler.
func sendKey(dialog *loadchart.LoadChartDialog, key rune) {
	handler := dialog.InputHandler()
	handler(tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone), func(tview.Primitive) {})
}

var _ = Describe("LoadChartDialog", func() {
	var (
		chart   *charts.Chart
		stream  *fakeStream
		listing *fakeListing
	)

	newDialog := func(resolve func() (charts.ScopeTarget, error)) *loadchart.LoadChartDialog {
		dialog := loadchart.NewLoadChartDialog(charts.ScopeSystem, resolve)
		dialog.SetCollectorFactory(func(c *charts.Chart, _ charts.Scope, _ charts.ScopeTarget) *charts.Collector {
			chart = c
			collector := charts.NewCollector(c, charts.ScopeSystem, charts.ScopeTarget{})
			collector.SetStreamFunc(stream.open)
			collector.SetListFunc(listing.list)
			collector.SetInterval(2 * time.Millisecond)

			return collector
		})
		dialog.SetDoneFunc(func() {})

		return dialog
	}

	simpleResolve := func() (charts.ScopeTarget, error) {
		return charts.ScopeTarget{}, nil
	}

	BeforeEach(func() {
		chart = charts.NewChart()
		stream = &fakeStream{}
		listing = &fakeListing{}
		listing.set([]charts.ContainerInfo{listInfo("a", "", "", false, true)})
	})

	It("does not display when the resolve fails", func() {
		dialog := newDialog(func() (charts.ScopeTarget, error) {
			return charts.ScopeTarget{}, errResolve
		})

		dialog.Display()

		Expect(dialog.IsDisplay()).To(BeFalse())
	})

	It("displays, collects and hides", func() {
		dialog := newDialog(simpleResolve)

		dialog.Display()
		Expect(dialog.IsDisplay()).To(BeTrue())
		Expect(stream.openCount()).To(Equal(0)) // asynchronous start

		Eventually(stream.openCount, "2s").Should(Equal(1))

		stream.sendOne("a", "a", 42, 100)
		Eventually(func() bool { return chart.SeriesCount("cnt:a") > 0 }, "2s").
			Should(BeTrue())

		dialog.Hide()

		Expect(dialog.IsDisplay()).To(BeFalse())
		// chart buffers are cleared on hide
		Expect(chart.SeriesCount("cnt:a")).To(Equal(0))
		// the stream context was cancelled
		Eventually(func() bool {
			return stream.lastCtx().Err() != nil
		}, "2s").Should(BeTrue())
	})

	It("cycles the metric with m", func() {
		dialog := newDialog(simpleResolve)
		dialog.Display()
		defer dialog.Hide()

		Eventually(stream.openCount, "2s").Should(Equal(1))

		Expect(chart.Metric()).To(Equal(charts.MetricCPU))

		sendKey(dialog, 'm')

		Expect(chart.Metric()).To(Equal(charts.MetricMemory))
		Expect(dialog.Paused()).To(BeFalse())
	})

	It("cycles the symbol mode with s", func() {
		dialog := newDialog(simpleResolve)
		dialog.Display()
		defer dialog.Hide()

		Eventually(stream.openCount, "2s").Should(Equal(1))

		Expect(chart.SymbolMode()).To(Equal(charts.SymbolBraille))

		sendKey(dialog, 's')
		Expect(chart.SymbolMode()).To(Equal(charts.SymbolBlock))

		sendKey(dialog, 's')
		Expect(chart.SymbolMode()).To(Equal(charts.SymbolTTY))

		sendKey(dialog, 's')
		Expect(chart.SymbolMode()).To(Equal(charts.SymbolBraille))
	})

	It("cycles the group with g", func() {
		dialog := newDialog(simpleResolve)
		dialog.Display()
		defer dialog.Hide()

		Eventually(stream.openCount, "2s").Should(Equal(1))

		collector := dialog.Collector()
		Expect(collector).ToNot(BeNil())
		Expect(collector.Group()).To(Equal(charts.GroupTotal))

		sendKey(dialog, 'g')
		Expect(collector.Group()).To(Equal(charts.GroupPod))
	})

	It("toggles pause with p", func() {
		dialog := newDialog(simpleResolve)
		dialog.Display()
		defer dialog.Hide()

		Eventually(stream.openCount, "2s").Should(Equal(1))

		Expect(dialog.Paused()).To(BeFalse())

		sendKey(dialog, 'p')
		Expect(dialog.Paused()).To(BeTrue())

		sendKey(dialog, 'p')
		Expect(dialog.Paused()).To(BeFalse())
	})

	It("closes with Esc", func() {
		closed := false
		dialog := newDialog(simpleResolve)
		dialog.SetDoneFunc(func() { closed = true })

		dialog.Display()
		defer dialog.Hide()

		Eventually(stream.openCount, "2s").Should(Equal(1))

		handler := dialog.InputHandler()
		handler(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone), func(tview.Primitive) {})

		Expect(closed).To(BeTrue())
	})

	It("opens the entity toggle with t and flips visibility with enter", func() {
		dialog := newDialog(simpleResolve)
		dialog.Display()
		defer dialog.Hide()

		Eventually(stream.openCount, "2s").Should(Equal(1))

		stream.sendOne("a", "a", 42, 100)
		Eventually(func() int { return chart.SeriesCount("cnt:a") }, "2s").
			Should(BeNumerically(">", 0))

		// container group first, so the series is visible by default
		dialog.Collector().SetGroup(charts.GroupContainer)
		Expect(dialog.ToggleOpen()).To(BeFalse())
		Expect(chart.Visible("cnt:a")).To(BeTrue())

		sendKey(dialog, 't')
		Expect(dialog.ToggleOpen()).To(BeTrue())

		// move selection to the cnt:a row (1: total, 2: cnt:a), then flip
		// the series
		handler := dialog.InputHandler()
		handler(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), func(tview.Primitive) {})
		handler(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), func(tview.Primitive) {})
		handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})

		Expect(chart.Visible("cnt:a")).To(BeFalse())

		// enter again restores it; t closes the table
		handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
		Expect(chart.Visible("cnt:a")).To(BeTrue())

		sendKey(dialog, 't')
		Expect(dialog.ToggleOpen()).To(BeFalse())
	})

	It("shows the connection banner when the stream cannot open", func() {
		stream.failOpen = true

		dialog := newDialog(simpleResolve)
		dialog.Display()
		defer dialog.Hide()

		Eventually(func() string { return dialog.BannerText() }, "2s").
			Should(ContainSubstring("open failed"))
	})

	It("renders the dialog on screen", func() {
		dialog := newDialog(simpleResolve)
		dialog.SetRect(0, 0, 100, 30)

		screen := tcell.NewSimulationScreen("UTF-8")
		Expect(screen.Init()).To(Succeed())
		screen.SetSize(100, 30)
		defer screen.Fini()

		// draw while hidden renders nothing
		dialog.Draw(screen)
		screen.Show()

		dialog.Display()
		defer dialog.Hide()

		dialog.Draw(screen)
		screen.Show()

		cells, width, _ := screen.GetContents()
		Expect(width).To(Equal(100))

		rendered := make([]rune, 0, len(cells))
		for _, cell := range cells {
			if len(cell.Runes) > 0 {
				rendered = append(rendered, cell.Runes[0])
			}
		}

		text := string(rendered)
		Expect(text).To(ContainSubstring("LOAD CHART"))
		Expect(text).To(ContainSubstring("system"))
		Expect(text).To(ContainSubstring("braille"))
		Expect(text).To(ContainSubstring("esc"))
	})
})
