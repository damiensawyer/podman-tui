package loadchart

import (
	"fmt"
	"sync"

	"github.com/containers/podman-tui/ui/charts"
	"github.com/containers/podman-tui/ui/dialogs"
	"github.com/containers/podman-tui/ui/style"
	"github.com/containers/podman-tui/ui/utils"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rs/zerolog/log"
)

const (
	dialogTitle  = "LOAD CHART"
	windowLabel  = "60s · 1/s"
	chartPadding = 1
)

// hotkeys, handled before any focused child widget
const (
	groupKey  = 'g'
	metricKey = 'm'
	symbolKey = 's'
	pauseKey  = 'p'
)

// collectorFactory creates the collector for an open dialog; replaceable
// for tests.
type collectorFactory func(chart *charts.Chart, scope charts.Scope, target charts.ScopeTarget) *charts.Collector

// LoadChartDialog implements the load chart dialog primitive: a live
// multi series chart with group, metric and symbol controls.
//
// Hotkeys g (group), m (metric), s (symbol), p (pause) and Esc (close)
// are intercepted in InputHandler before any focused child sees them.
type LoadChartDialog struct {
	*tview.Box

	layout        *tview.Flex
	controls      *tview.TextView
	chart         *charts.Chart
	collector     *charts.Collector
	form          *tview.Form
	collectorInit collectorFactory

	scope      charts.Scope
	resolve    func() (charts.ScopeTarget, error)
	metric     charts.Metric
	symbolMode charts.SymbolMode
	paused     bool

	mu              sync.Mutex
	doneHandler     func()
	fastRefreshChan chan bool
	display         bool
}

// NewLoadChartDialog returns a new load chart dialog for the given scope.
// resolve is called on display: it validates the page selection (showing
// the page error dialog when empty) and returns the scope target; on
// error the dialog is never displayed.
func NewLoadChartDialog(scope charts.Scope, resolve func() (charts.ScopeTarget, error)) *LoadChartDialog {
	chart := charts.NewChart()

	dialog := LoadChartDialog{
		Box:           tview.NewBox(),
		chart:         chart,
		scope:         scope,
		resolve:       resolve,
		metric:        charts.MetricCPU,
		symbolMode:    charts.SymbolBraille,
		collectorInit: NewCollector,
	}

	dialog.chart.SetSymbolMode(charts.SymbolBraille)
	dialog.chart.SetMetric(charts.MetricCPU)
	dialog.chart.SetBackgroundColor(style.DialogBgColor)

	// controls legend row
	dialog.controls = tview.NewTextView()
	dialog.controls.SetDynamicColors(true)
	dialog.controls.SetBackgroundColor(style.DialogBgColor)

	// form
	dialog.form = tview.NewForm().
		AddButton("Close", nil).
		SetButtonsAlign(tview.AlignRight)
	dialog.form.SetBackgroundColor(style.DialogBgColor)
	dialog.form.SetButtonBackgroundColor(style.ButtonBgColor)

	// main dialog layout
	dialog.layout = tview.NewFlex().SetDirection(tview.FlexRow)
	dialog.layout.SetBorder(true)
	dialog.layout.SetBorderColor(style.DialogBorderColor)
	dialog.layout.SetBackgroundColor(style.DialogBgColor)

	dialog.layout.AddItem(utils.EmptyBoxSpace(style.DialogBgColor), chartPadding, 0, true)
	dialog.layout.AddItem(dialog.chart, 0, 1, true)
	dialog.layout.AddItem(utils.EmptyBoxSpace(style.DialogBgColor), chartPadding, 0, true)
	dialog.layout.AddItem(dialog.controls, 1, 0, true)
	dialog.layout.AddItem(dialog.form, dialogs.DialogFormHeight, 0, true)

	dialog.updateControls()

	return &dialog
}

// SetDoneFunc sets the dialog close function.
func (d *LoadChartDialog) SetDoneFunc(handler func()) *LoadChartDialog {
	d.doneHandler = handler

	closeButton := d.form.GetButton(d.form.GetButtonCount() - 1)
	closeButton.SetSelectedFunc(handler)

	return d
}

// SetFastRefreshChannel sets the channel used to request an immediate
// repaint after hotkey state changes.
func (d *LoadChartDialog) SetFastRefreshChannel(refresh chan bool) {
	d.fastRefreshChan = refresh
}

// SetCollectorFactory replaces the collector factory (test seam).
func (d *LoadChartDialog) SetCollectorFactory(factory collectorFactory) {
	d.collectorInit = factory
}

// Display displays this dialog and starts collection.
func (d *LoadChartDialog) Display() {
	target, err := d.resolve()
	if err != nil {
		// resolve already reported the error on the page
		return
	}

	d.mu.Lock()
	d.display = true
	d.paused = false

	d.chart.Reset()
	d.chart.SetMetric(d.metric)
	d.chart.SetSymbolMode(d.symbolMode)

	d.collector = d.collectorInit(d.chart, d.scope, target)
	d.collector.SetErrorHandler(func(err error) {
		log.Error().Msgf("load chart dialog: %v", err)
	})
	d.collector.SetPaused(false)
	d.collector.Start()
	d.mu.Unlock()

	d.updateControls()

	log.Debug().Msg("load chart dialog: display")
}

// IsDisplay returns true if the dialog is shown.
func (d *LoadChartDialog) IsDisplay() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.display
}

// Hide stops displaying the dialog and stops collection.
func (d *LoadChartDialog) Hide() {
	d.mu.Lock()
	d.display = false
	collector := d.collector
	d.collector = nil
	d.mu.Unlock()

	if collector != nil {
		collector.Stop()
	}

	d.chart.Reset()

	log.Debug().Msg("load chart dialog: hide")
}

// HasFocus returns whether this primitive has focus.
func (d *LoadChartDialog) HasFocus() bool {
	if d.chart.HasFocus() || d.form.HasFocus() {
		return true
	}

	return d.Box.HasFocus()
}

// Focus is called when this primitive receives focus.
func (d *LoadChartDialog) Focus(delegate func(p tview.Primitive)) {
	delegate(d.chart)
}

// InputHandler returns the input handler for this primitive.
func (d *LoadChartDialog) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return d.WrapInputHandler(func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
		log.Debug().Msgf("load chart dialog: event %v received", event)

		// hotkeys are handled before the form so they cannot be
		// swallowed by focused widgets
		switch event.Rune() {
		case groupKey:
			d.nextGroup()
		case metricKey:
			d.cycleMetric()
		case symbolKey:
			d.cycleSymbol()
		case pauseKey:
			d.togglePause()
		case 't':
			// entity toggle arrives with milestone 6
		}

		if event.Key() == tcell.KeyEsc {
			d.doneHandler()

			return
		}

		if d.form.HasFocus() {
			if formHandler := d.form.InputHandler(); formHandler != nil {
				formHandler(event, setFocus)
			}
		}
	})
}

// Draw draws this primitive onto the screen.
func (d *LoadChartDialog) Draw(screen tcell.Screen) {
	if !d.display {
		return
	}

	d.DrawForSubclass(screen, d)

	x, y, width, height := d.GetInnerRect()

	d.layout.SetRect(x, y, width, height)
	d.layout.Draw(screen)
}

// SetRect set rects for this primitive.
func (d *LoadChartDialog) SetRect(x, y, width, height int) {
	dX := x + 1
	dY := y + 1

	d.Box.SetRect(dX, dY, width-2, height-2) //nolint:mnd
}

// Paused reports whether collection is paused.
func (d *LoadChartDialog) Paused() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.paused
}

// Collector returns the active collector, or nil while the dialog is
// hidden.
func (d *LoadChartDialog) Collector() *charts.Collector {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.collector
}

// nextGroup cycles to the next group mode of the scope.
func (d *LoadChartDialog) nextGroup() {
	collector := d.currentCollector()
	if collector == nil {
		return
	}

	collector.NextGroup()
	d.updateControls()
	d.requestFastRefresh()
}

// cycleMetric toggles between CPU and memory.
func (d *LoadChartDialog) cycleMetric() {
	collector := d.currentCollector()

	d.mu.Lock()
	if d.metric == charts.MetricCPU {
		d.metric = charts.MetricMemory
	} else {
		d.metric = charts.MetricCPU
	}

	d.chart.SetMetric(d.metric)

	if collector != nil {
		collector.SetMetric(d.metric)
	}
	d.mu.Unlock()

	d.updateControls()
	d.requestFastRefresh()
}

// cycleSymbol switches braille -> block -> tty.
func (d *LoadChartDialog) cycleSymbol() {
	d.mu.Lock()

	switch d.symbolMode {
	case charts.SymbolBraille:
		d.symbolMode = charts.SymbolBlock
	case charts.SymbolBlock:
		d.symbolMode = charts.SymbolTTY
	default:
		d.symbolMode = charts.SymbolBraille
	}

	d.chart.SetSymbolMode(d.symbolMode)
	d.mu.Unlock()

	d.updateControls()
	d.requestFastRefresh()
}

// togglePause pauses or resumes collection.
func (d *LoadChartDialog) togglePause() {
	collector := d.currentCollector()

	d.mu.Lock()
	d.paused = !d.paused

	if collector != nil {
		collector.SetPaused(d.paused)
	}
	d.mu.Unlock()

	d.updateControls()
	d.requestFastRefresh()
}

// currentCollector returns the active collector, if any.
func (d *LoadChartDialog) currentCollector() *charts.Collector {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.collector
}

// requestFastRefresh asks the app for an immediate repaint.
func (d *LoadChartDialog) requestFastRefresh() {
	if d.fastRefreshChan == nil {
		return
	}

	select {
	case d.fastRefreshChan <- true:
	default:
	}
}

// updateControls refreshes the title and the hotkey legend row.
func (d *LoadChartDialog) updateControls() {
	d.mu.Lock()
	metric, symbol, paused := d.metric, d.symbolMode, d.paused
	scopeName := scopeLabel(d.scope)
	d.mu.Unlock()

	groupLabel := "total"
	if collector := d.currentCollector(); collector != nil {
		groupLabel = groupName(collector.Group())
	}

	subtitle := fmt.Sprintf("%s · %s · %s · %s",
		scopeName, metricName(metric), symbolName(symbol), windowLabel)
	if paused {
		subtitle += " · paused"
	}

	d.layout.SetTitle(fmt.Sprintf("%s [%s]", dialogTitle, subtitle))

	legend := fmt.Sprintf(" [::b]g[::-]roup: %s   [::b]m[::-]etric: %s   [::b]s[::-]ymbol: %s   [::b]p[::-]ause   [::b]esc[::-] close",
		groupLabel, metricName(metric), symbolName(symbol))
	if paused {
		legend = fmt.Sprintf(" [::b]p[::-]ause: [yellow]paused[-]  %s", legend)
	}

	d.controls.SetText(legend)
}

// scopeLabel names the dialog scope.
func scopeLabel(scope charts.Scope) string {
	switch scope {
	case charts.ScopePod:
		return "pod"
	case charts.ScopeContainer:
		return "container"
	default:
		return "system"
	}
}

// metricName names the plotted metric.
func metricName(metric charts.Metric) string {
	if metric == charts.MetricMemory {
		return "memory"
	}

	return "cpu%"
}

// symbolName names the symbol mode.
func symbolName(mode charts.SymbolMode) string {
	switch mode {
	case charts.SymbolBlock:
		return "block"
	case charts.SymbolTTY:
		return "tty"
	default:
		return "braille"
	}
}

// groupName names the group mode.
func groupName(group charts.GroupMode) string {
	switch group {
	case charts.GroupPod:
		return "pod"
	case charts.GroupContainer:
		return "container"
	case charts.GroupProcess:
		return "process"
	default:
		return "total"
	}
}
