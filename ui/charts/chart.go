package charts

import (
	"fmt"
	"math"
	"sync"

	"github.com/containers/podman-tui/ui/style"
	"github.com/docker/go-units"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Metric selects the plotted measurement and its y-axis scaling.
type Metric int

const (
	// MetricCPU plots CPU usage with a fixed 0..100 percent scale.
	MetricCPU Metric = iota
	// MetricMemory plots memory usage with a dynamic byte scale derived
	// from the maximum of the whole 60 second window, so the axis does
	// not jitter between frames.
	MetricMemory
)

const (
	// yAxisWidth reserves room for the y-axis labels ("256MiB", "100%").
	yAxisWidth = 6
	// legendMinRows is the legend row count always granted when there
	// are enough series; larger charts give the legend up to a third of
	// their height.
	legendMinRows = 6
	// legendHeightDiv is the divisor of the chart height bounding the
	// legend rows.
	legendHeightDiv = 3
	// legendValueWidth reserves room for the right aligned legend value.
	legendValueWidth = 8
	// sparkMaxCells caps the legend sparkline width in cells.
	sparkMaxCells = 20
	// minPlotRows is the smallest usable plot height in cells.
	minPlotRows = 2
	// minPlotCols is the smallest usable plot width in cells.
	minPlotCols = 8
)

// yAxisTickFracs are the fractions of the y-axis maximum carrying a label
// and a horizontal gridline.
var yAxisTickFracs = []float64{0, 0.25, 0.5, 0.75, 1}

// SeriesInfo is a legend entry describing one chart series.
type SeriesInfo struct {
	ID      string
	Name    string
	Color   tcell.Color
	Visible bool
	Value   float64 // most recent sample
}

// seriesSnapshot is an immutable copy of a series for rendering.
type seriesSnapshot struct {
	id      string
	name    string
	color   tcell.Color
	visible bool

	samples []float64
}

// last returns the most recent sample, or 0 when the window is empty.
func (s seriesSnapshot) last() float64 {
	if len(s.samples) == 0 {
		return 0
	}

	return s.samples[len(s.samples)-1]
}

// Chart is a tview primitive drawing several series on one shared canvas
// with common axes and a legend.
//
// The collector pushes samples from its background goroutine while Draw
// runs on the application goroutine; every access to the series maps is
// guarded by the chart mutex.
type Chart struct {
	*tview.Box

	mu     sync.Mutex
	series map[string]*Series
	order  []string
	hidden map[string]bool
	colors *colorAssigner

	metric     Metric
	symbolMode SymbolMode

	// legendFilter selects the series listed in the legend; nil lists
	// every series.
	legendFilter func(id string) bool
	// legendOffset is the index of the first listed legend entry.
	legendOffset int
}

// NewChart returns a new empty chart.
func NewChart() *Chart {
	return &Chart{
		Box:        tview.NewBox(),
		series:     make(map[string]*Series),
		hidden:     make(map[string]bool),
		colors:     newColorAssigner(),
		metric:     MetricCPU,
		symbolMode: SymbolBraille,
	}
}

// EnsureSeries returns the series with the given id, creating it with the
// display name when missing. A tcell.ColorDefault colour assigns the next
// free palette slot, which stays stable for the chart's lifetime.
func (c *Chart) EnsureSeries(id, name string, color tcell.Color) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if series, ok := c.series[id]; ok {
		series.SetName(name)

		return
	}

	if color == tcell.ColorDefault {
		color = c.colors.colorFor(name)
	}

	c.series[id] = NewSeries(id, name, color)
	c.order = append(c.order, id)
}

// Push appends one sample to the series with the given id. Unknown series
// are ignored; the collector owns series creation.
func (c *Chart) Push(id string, value float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if series, ok := c.series[id]; ok {
		series.Push(value)
	}
}

// RemoveSeries drops the series with the given id. A pending hidden
// state is cleared with it: a re-created series starts visible.
func (c *Chart) RemoveSeries(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.series, id)
	delete(c.hidden, id)

	for i, item := range c.order {
		if item == id {
			c.order = append(c.order[:i], c.order[i+1:]...)

			break
		}
	}
}

// SetVisible shows or hides a series without discarding its history, so
// re-enabling it has no historical gaps. The desired state is recorded
// even for series that do not exist yet: a group default set before a
// series is created is respected when the series appears.
func (c *Chart) SetVisible(id string, visible bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if visible {
		delete(c.hidden, id)

		return
	}

	c.hidden[id] = true
}

// Visible reports whether the series with the given id is drawn.
func (c *Chart) Visible(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return !c.hidden[id]
}

// SetLegendFilter selects which series the legend lists (nil lists all)
// and scrolls the legend back to its top.
func (c *Chart) SetLegendFilter(filter func(id string) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.legendFilter = filter
	c.legendOffset = 0
}

// ScrollLegend scrolls the legend by delta entries. The offset is clamped
// to the listed entries on the next draw.
func (c *Chart) ScrollLegend(delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.legendOffset = max(c.legendOffset+delta, 0)
}

// LegendOffset returns the index of the first listed legend entry.
func (c *Chart) LegendOffset() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.legendOffset
}

// SetMetric switches the plotted measurement.
func (c *Chart) SetMetric(metric Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.metric = metric
}

// Metric returns the currently plotted measurement.
func (c *Chart) Metric() Metric {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.metric
}

// SetSymbolMode switches the canvas symbol resolution.
func (c *Chart) SetSymbolMode(mode SymbolMode) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.symbolMode = mode
}

// SymbolMode returns the canvas symbol resolution.
func (c *Chart) SymbolMode() SymbolMode {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.symbolMode
}

// Reset removes every series and colour assignment.
func (c *Chart) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.series = make(map[string]*Series)
	c.order = nil
	c.hidden = make(map[string]bool)
	c.colors = newColorAssigner()
	c.legendOffset = 0
}

// SeriesCount returns the number of samples collected for the series with
// the given id, or 0 when it does not exist.
func (c *Chart) SeriesCount(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if series, ok := c.series[id]; ok {
		return series.Len()
	}

	return 0
}

// SeriesInfos returns legend metadata for every series in stable order.
func (c *Chart) SeriesInfos() []SeriesInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	infos := make([]SeriesInfo, 0, len(c.order))

	for _, id := range c.order {
		series := c.series[id]
		infos = append(infos, SeriesInfo{
			ID:      series.ID(),
			Name:    series.Name(),
			Color:   series.Color(),
			Visible: !c.hidden[id],
			Value:   series.Last(),
		})
	}

	return infos
}

// LegendInfos returns legend metadata for the series the legend lists.
func (c *Chart) LegendInfos() []SeriesInfo {
	c.mu.Lock()
	filter := c.legendFilter
	c.mu.Unlock()

	infos := c.SeriesInfos()
	if filter == nil {
		return infos
	}

	listed := infos[:0]

	for _, info := range infos {
		if filter(info.ID) {
			listed = append(listed, info)
		}
	}

	return listed
}

// Draw draws this primitive onto the screen.
func (c *Chart) Draw(screen tcell.Screen) {
	c.DrawForSubclass(screen, c)

	x, y, width, height := c.GetInnerRect()

	plotCols := width - yAxisWidth - 1
	if plotCols < minPlotCols || height < minPlotRows+1 {
		return
	}

	snaps, legend := c.snapshot()

	legendRows := min(len(legend), max(legendMinRows, height/legendHeightDiv))
	plotRows := height - 1 - legendRows

	for plotRows < minPlotRows && legendRows > 0 {
		legendRows--
		plotRows = height - 1 - legendRows
	}

	yMax := yMaxOf(c.metric, snaps)
	canvas := NewMicroCanvas(plotCols, plotRows, c.symbolMode)

	c.plotSeries(canvas, snaps, yMax)

	tickRows := drawYAxis(screen, x, y, plotRows, c.metric, yMax)
	plotX, plotY := x+yAxisWidth+1, y

	blitCanvas(screen, canvas, plotX, plotY, tickRows, c.GetBackgroundColor())
	drawXLabels(screen, plotX, y+plotRows, plotCols)
	c.drawLegend(screen, legend, x, y+plotRows+1, width, legendRows, plotCols, yMax)
}

// snapshot copies all series samples under the mutex and returns them
// together with the subset the legend lists.
func (c *Chart) snapshot() ([]seriesSnapshot, []seriesSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	snaps := make([]seriesSnapshot, 0, len(c.order))
	legend := make([]seriesSnapshot, 0, len(c.order))

	for _, id := range c.order {
		series := c.series[id]

		samples := make([]float64, series.Len())
		for i := range samples {
			samples[i] = series.At(i)
		}

		snap := seriesSnapshot{
			id:      series.ID(),
			name:    series.Name(),
			color:   series.Color(),
			visible: !c.hidden[id],
			samples: samples,
		}

		snaps = append(snaps, snap)

		if c.legendFilter == nil || c.legendFilter(id) {
			legend = append(legend, snap)
		}
	}

	return snaps, legend
}

// legendWindow clamps the legend offset to the listed entries and returns
// the entries fitting into rows. When the entries overflow, the last row
// is kept for the scroll indicator.
func (c *Chart) legendWindow(legend []seriesSnapshot, rows int) ([]seriesSnapshot, int, bool) {
	if len(legend) <= rows {
		c.mu.Lock()
		c.legendOffset = 0
		c.mu.Unlock()

		return legend, 0, false
	}

	// a single row has no room for the indicator: show the top entry
	if rows < 2 { //nolint:mnd
		return legend[:rows], 0, false
	}

	entries := rows - 1

	c.mu.Lock()
	c.legendOffset = min(c.legendOffset, len(legend)-entries)
	offset := c.legendOffset
	c.mu.Unlock()

	return legend[offset : offset+entries], offset, true
}

// plotSeries renders the visible snapshots onto the canvas.
func (c *Chart) plotSeries(canvas *MicroCanvas, snaps []seriesSnapshot, yMax float64) {
	if canvas.mode == SymbolTTY {
		plotTTYColumns(canvas, snaps, yMax)

		return
	}

	plotDotSeries(canvas, snaps, yMax)
}

// plotDotSeries rasterises each visible series as a Bresenham line across
// the sub-pixel grid. Samples are right aligned so the newest sample sits
// at the right edge and the chart scrolls leftwards; the second sub-column
// of every cell is the Bresenham midpoint between two samples.
func plotDotSeries(canvas *MicroCanvas, snaps []seriesSnapshot, yMax float64) {
	gridH := canvas.GridHeight()

	yFor := func(v float64) int {
		frac := clamp(v/yMax, 0, 1)

		return int(math.Round(float64(gridH-1) * (1 - frac)))
	}

	for _, snap := range snaps {
		if !snap.visible || len(snap.samples) == 0 {
			continue
		}

		samples := snap.samples
		if len(samples) > canvas.Width() {
			samples = samples[len(samples)-canvas.Width():]
		}

		startX := canvas.GridWidth() - 2*len(samples) //nolint:mnd

		if len(samples) == 1 {
			canvas.SetDot(startX, yFor(samples[0]), snap.color, samples[0])

			continue
		}

		for i := range len(samples) - 1 {
			canvas.DrawLine(startX+2*i, yFor(samples[i]), //nolint:mnd
				startX+2*(i+1), yFor(samples[i+1]),
				snap.color, max(samples[i], samples[i+1]))
		}
	}
}

// plotTTYColumns paints columns as filled areas without sub-cell
// geometry: per column the visible series are painted back-to-front,
// highest value first, so overlaps show the smaller series on top.
func plotTTYColumns(canvas *MicroCanvas, snaps []seriesSnapshot, yMax float64) {
	for col := range canvas.Width() {
		type columnSeries struct {
			height float64
			color  tcell.Color
		}

		column := make([]columnSeries, 0, len(snaps))

		for _, snap := range snaps {
			if !snap.visible || len(snap.samples) == 0 {
				continue
			}

			sampleIdx := len(snap.samples) - canvas.Width() + col
			if sampleIdx < 0 {
				continue
			}

			if sampleIdx >= len(snap.samples) {
				sampleIdx = len(snap.samples) - 1
			}

			column = append(column, columnSeries{
				height: snap.samples[sampleIdx],
				color:  snap.color,
			})
		}

		sortDesc(column, func(entry columnSeries) float64 { return entry.height })

		for _, entry := range column {
			canvas.FillColumn(col, entry.height/yMax, entry.color)
		}
	}
}

// drawYAxis writes the y-axis labels, the axis column and returns the
// screen rows carrying a gridline.
func drawYAxis(screen tcell.Screen, x, y, plotRows int, metric Metric, yMax float64) []int {
	labelStyle := tcell.StyleDefault.Foreground(style.DialogFgColor)
	axisStyle := tcell.StyleDefault.Foreground(style.DialogSubBoxBorderColor)
	tickRows := make([]int, 0, len(yAxisTickFracs))

	for i, frac := range yAxisTickFracs {
		row := int(math.Round(float64(plotRows-1) * (1 - frac)))
		tickRows = append(tickRows, row)

		label := FormatValue(metric, frac*yMax)

		drawStringRight(screen, x, y+row, yAxisWidth, label, labelStyle)

		axis := '│'
		if i == 0 {
			axis = '┼'
		}

		screen.SetContent(x+yAxisWidth, y+row, axis, nil, axisStyle)
	}

	return tickRows
}

// blitCanvas copies the canvas cells onto the screen, drawing horizontal
// gridlines on tick rows where the canvas has no glyph.
func blitCanvas(screen tcell.Screen, canvas *MicroCanvas, x, y int, tickRows []int, bg tcell.Color) {
	tickSet := make(map[int]bool, len(tickRows))
	for _, row := range tickRows {
		tickSet[row] = true
	}

	gridStyle := tcell.StyleDefault.Foreground(style.DialogSubBoxBorderColor).Background(bg)

	for cy := range canvas.Height() {
		for cx := range canvas.Width() {
			px, py := x+cx, y+cy

			glyph, fg, ok := canvas.Cell(cx, cy)
			if ok {
				screen.SetContent(px, py, glyph, nil,
					tcell.StyleDefault.Foreground(fg).Background(bg))

				continue
			}

			if tickSet[cy] {
				screen.SetContent(px, py, '─', nil, gridStyle)

				continue
			}

			screen.SetContent(px, py, ' ', nil, tcell.StyleDefault.Background(bg))
		}
	}
}

// drawXLabels writes the -60s..now window labels below the plot.
func drawXLabels(screen tcell.Screen, x, y, plotCols int) {
	labelStyle := tcell.StyleDefault.Foreground(style.DialogSubBoxBorderColor)
	labels := []string{"-60s", "-45s", "-30s", "-15s"}

	for i, label := range labels {
		frac := float64(i) / float64(len(labels))
		px := x + int(math.Round(frac*float64(plotCols-1)))
		drawString(screen, px, y, label, labelStyle)
	}

	drawStringRight(screen, x, y, plotCols, "now", labelStyle)
}

// drawLegend writes one row per listed series into at most rows rows:
// colour badge, name, current value and a braille sparkline of the recent
// window. Overflowing entries scroll, with an indicator on the last row.
func (c *Chart) drawLegend(screen tcell.Screen, legend []seriesSnapshot,
	x, y, width, rows, plotCols int, yMax float64,
) {
	if rows <= 0 {
		return
	}

	bg := c.GetBackgroundColor()

	snaps, offset, overflow := c.legendWindow(legend, rows)
	if overflow {
		indicator := fmt.Sprintf("↑↓ %d-%d of %d series", offset+1, offset+len(snaps), len(legend))
		indicatorStyle := tcell.StyleDefault.Foreground(style.DialogSubBoxBorderColor).Background(bg)
		drawString(screen, x+2, y+len(snaps), truncate(indicator, width-2), indicatorStyle) //nolint:mnd
	}

	sparkCells := min(sparkMaxCells, width/4, plotCols)
	valueX := x + width - sparkCells - 1 - legendValueWidth

	if sparkCells < 4 {
		sparkCells = 0
		valueX = x + width - legendValueWidth
	}

	for i, snap := range snaps {
		py := y + i

		if snap.visible {
			badgeStyle := tcell.StyleDefault.Foreground(snap.color).Background(bg)
			screen.SetContent(x, py, '●', nil, badgeStyle)
		} else {
			dimStyle := tcell.StyleDefault.Foreground(style.DialogSubBoxBorderColor).Background(bg)
			screen.SetContent(x, py, '○', nil, dimStyle)
		}

		nameStyle := tcell.StyleDefault.Foreground(style.DialogFgColor).Background(bg)
		if !snap.visible {
			nameStyle = tcell.StyleDefault.
				Foreground(style.DialogSubBoxBorderColor).Background(bg)
		}

		nameWidth := valueX - x - 2
		drawString(screen, x+2, py, truncate(snap.name, nameWidth), nameStyle)

		valueStyle := tcell.StyleDefault.Foreground(style.DialogFgColor).Background(bg)
		if snap.visible {
			drawStringRight(screen, valueX, py, legendValueWidth,
				FormatValue(c.metric, snap.last()), valueStyle)
		} else {
			drawStringRight(screen, valueX, py, legendValueWidth, "(hidden)", valueStyle)
		}

		if sparkCells > 0 && snap.visible {
			drawSparkline(screen, x+width-sparkCells, py,
				sparkCells, snap.samples, yMax, snap.color, bg)
		}
	}
}

// drawSparkline renders the most recent samples as a filled braille area
// right aligned into the given cells.
func drawSparkline(screen tcell.Screen, x, y, cells int,
	samples []float64, yMax float64, color tcell.Color, bg tcell.Color,
) {
	window := samples
	if len(window) > 2*cells { //nolint:mnd
		window = window[len(window)-2*cells:] //nolint:mnd
	}

	sparkStyle := tcell.StyleDefault.Foreground(color).Background(bg)

	blank := rune(brailleBase)
	for pad := cells - len(window)/2; pad > 0; pad-- {
		screen.SetContent(x+cells-pad, y, blank, nil, sparkStyle)
	}

	for i := range len(window) / 2 { //nolint:mnd
		left := sparkLevel(window[2*i], yMax)    //nolint:mnd
		right := sparkLevel(window[2*i+1], yMax) //nolint:mnd
		screen.SetContent(x+cells-len(window)/2+i, y, BrailleFillGlyph(left, right), nil, sparkStyle)
	}
}

// sparkLevel quantises a sample against the axis maximum to a 0..4
// filled-from-bottom row level.
func sparkLevel(v float64, yMax float64) int {
	if yMax <= 0 {
		return 0
	}

	fraction := clamp(v/yMax, 0, 1)

	return min(max(int(fraction*4+0.5), 0), 4) //nolint:mnd
}

// yMaxOf returns the y-axis maximum: a fixed 0..100 percent scale for CPU
// and a window smoothed nice byte scale for memory.
func yMaxOf(metric Metric, snaps []seriesSnapshot) float64 {
	if metric == MetricCPU {
		return 100
	}

	windowMax := 0.0
	for _, snap := range snaps {
		if !snap.visible {
			continue
		}

		for _, sample := range snap.samples {
			windowMax = max(windowMax, sample)
		}
	}

	return niceByteCeil(windowMax)
}

// niceByteCeil rounds a byte value up to the next power of two so the
// axis labels stay clean binary units.
func niceByteCeil(v float64) float64 {
	const oneMiB = 1024 * 1024

	if v <= 0 {
		return oneMiB
	}

	unit := math.Pow(2, math.Floor(math.Log2(v)))
	if unit >= v {
		return unit
	}

	return 2 * unit
}

// FormatValue formats a measurement value for display.
func FormatValue(metric Metric, v float64) string {
	if metric == MetricCPU {
		// whole values (the axis ticks) stay bare, samples keep one
		// decimal so they fit the legend value column
		if v == math.Trunc(v) {
			return fmt.Sprintf("%.0f%%", v)
		}

		return fmt.Sprintf("%.1f%%", v)
	}

	return units.BytesSize(v)
}

// sortDesc sorts the slice by the float key extracted from each entry,
// largest first. The sort is stable to keep the legend order on ties.
func sortDesc[T any](items []T, key func(T) float64) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && key(items[j]) > key(items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// clamp restricts v to the [lo, hi] interval.
func clamp(v, lo, hi float64) float64 {
	return min(max(v, lo), hi)
}

// truncate shortens s to at most width runes, appending an ellipsis.
func truncate(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}

	if width <= 1 {
		return "…"
	}

	return string(runes[:width-1]) + "…"
}

// drawString writes s starting at (x, y).
func drawString(screen tcell.Screen, x, y int, s string, st tcell.Style) {
	for i, r := range []rune(s) {
		screen.SetContent(x+i, y, r, nil, st)
	}
}

// drawStringRight writes s so it ends right before x+width.
func drawStringRight(screen tcell.Screen, x, y, width int, s string, st tcell.Style) {
	px := max(x+width-len([]rune(s)), x)

	drawString(screen, px, y, s, st)
}
