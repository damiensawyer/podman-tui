package charts_test

import (
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/containers/podman-tui/ui/charts"
)

// renderChart draws the chart onto a simulation screen of the given size
// and returns the screen contents as a rune grid.
func renderChart(chart *charts.Chart, width, height int) [][]rune {
	screen := tcell.NewSimulationScreen("UTF-8")
	Expect(screen.Init()).To(Succeed())
	screen.SetSize(width, height)
	defer screen.Fini()

	chart.SetRect(0, 0, width, height)
	chart.Draw(screen)
	screen.Show()

	cells, screenWidth, _ := screen.GetContents()
	Expect(screenWidth).To(Equal(width))

	grid := make([][]rune, height)
	for row := range grid {
		grid[row] = make([]rune, width)
	}

	for idx, cell := range cells {
		if len(cell.Runes) > 0 {
			grid[idx/width][idx%width] = cell.Runes[0]
		}
	}

	return grid
}

// gridContains reports whether the row contains the given string.
func rowContains(grid [][]rune, row int, text string) bool {
	if row < 0 || row >= len(grid) {
		return false
	}

	runes := []rune(text)

	for col := range grid[row] {
		if col+len(runes) > len(grid[row]) {
			return false
		}

		found := true
		for i, r := range runes {
			if grid[row][col+i] != r {
				found = false

				break
			}
		}

		if found {
			return true
		}
	}

	return false
}

var _ = Describe("Chart", func() {
	newSampledChart := func() *charts.Chart {
		chart := charts.NewChart()
		chart.EnsureSeries("web", "web", tcell.ColorDefault)
		chart.EnsureSeries("db", "db", tcell.ColorDefault)

		for i := range 30 {
			chart.Push("web", 10+float64(i%10))
			chart.Push("db", 2+float64(i%3))
		}

		return chart
	}

	Describe("series management", func() {
		It("keeps a stable legend order", func() {
			chart := newSampledChart()

			infos := chart.SeriesInfos()
			Expect(infos).To(HaveLen(2))
			Expect(infos[0].ID).To(Equal("web"))
			Expect(infos[1].ID).To(Equal("db"))
		})

		It("renames a series on re-registration", func() {
			chart := charts.NewChart()
			chart.EnsureSeries("1", "old", tcell.ColorDefault)
			chart.EnsureSeries("1", "new", tcell.ColorDefault)

			Expect(chart.SeriesInfos()[0].Name).To(Equal("new"))
			Expect(chart.SeriesInfos()).To(HaveLen(1))
		})

		It("ignores samples for unknown series", func() {
			chart := charts.NewChart()

			Expect(func() { chart.Push("missing", 1.0) }).ToNot(Panic())
		})

		It("shows and hides series keeping their history", func() {
			chart := newSampledChart()
			Expect(chart.Visible("web")).To(BeTrue())

			chart.SetVisible("web", false)
			Expect(chart.Visible("web")).To(BeFalse())

			// hidden series keep collecting
			chart.Push("web", 50)
			chart.SetVisible("web", true)

			infos := chart.SeriesInfos()
			Expect(infos[0].Value).To(Equal(50.0))
		})

		It("removes a series from the legend", func() {
			chart := newSampledChart()
			chart.RemoveSeries("db")

			infos := chart.SeriesInfos()
			Expect(infos).To(HaveLen(1))
			Expect(infos[0].ID).To(Equal("web"))
		})

		It("resets everything", func() {
			chart := newSampledChart()
			chart.SetVisible("web", false)
			chart.Reset()

			Expect(chart.SeriesInfos()).To(BeEmpty())
			Expect(chart.Visible("web")).To(BeTrue())
		})
	})

	Describe("rendering", func() {
		It("draws y axis labels and the legend", func() {
			chart := newSampledChart()
			grid := renderChart(chart, 100, 30)

			// CPU percent fixed scale: top tick label "100%" in the
			// y-axis gutter of the first plot row.
			Expect(rowContains(grid, 0, "100%")).To(BeTrue())
			Expect(rowContains(grid, 26, "0%")).To(BeTrue())

			// legend rows carry the series names, right below the plot
			Expect(rowContains(grid, 28, "web")).To(BeTrue())
			Expect(rowContains(grid, 29, "db")).To(BeTrue())

			// x-axis labels sit between plot and legend
			Expect(rowContains(grid, 27, "now")).To(BeTrue())
		})

		It("renders braille dots in the plot area", func() {
			chart := newSampledChart()
			chart.SetSymbolMode(charts.SymbolBraille)
			grid := renderChart(chart, 100, 30)

			dots := 0
			for y := range 27 {
				for x := 7; x < 100; x++ {
					switch grid[y][x] {
					case ' ', '│', '┼', '─':
					default:
						dots++
					}
				}
			}

			Expect(dots).To(BeNumerically(">", 20))
		})

		It("renders quadrant blocks in block mode", func() {
			chart := newSampledChart()
			chart.SetSymbolMode(charts.SymbolBlock)
			grid := renderChart(chart, 100, 30)

			found := false
			for y := range 27 {
				for x := 7; x < 100; x++ {
					if grid[y][x] == '█' || grid[y][x] == '▄' || grid[y][x] == '▀' {
						found = true
					}
				}
			}

			Expect(found).To(BeTrue())
		})

		It("renders filled shading glyphs in TTY mode", func() {
			chart := newSampledChart()
			chart.SetSymbolMode(charts.SymbolTTY)
			grid := renderChart(chart, 100, 30)

			filled := 0
			for y := range 27 {
				for x := 7; x < 100; x++ {
					switch grid[y][x] {
					case '░', '▒', '▓', '█':
						filled++
					}
				}
			}

			Expect(filled).To(BeNumerically(">", 20))
		})

		It("scales the memory axis to the visible window maximum", func() {
			chart := charts.NewChart()
			chart.SetMetric(charts.MetricMemory)
			chart.EnsureSeries("big", "big", tcell.ColorDefault)
			chart.EnsureSeries("small", "small", tcell.ColorDefault)

			for i := range 5 {
				chart.Push("big", 10*1024*1024)           // 10 MiB
				chart.Push("small", float64(1024*1024+i)) // ~1 MiB
			}

			// the hidden 100 MiB series must not stretch the axis
			chart.EnsureSeries("hidden", "hidden", tcell.ColorDefault)

			for range 5 {
				chart.Push("hidden", 100*1024*1024)
			}

			chart.SetVisible("hidden", false)

			grid := renderChart(chart, 100, 30)

			// window max is 10 MiB -> nice ceil = 16 MiB at the top tick
			Expect(rowContains(grid, 0, "16MiB")).To(BeTrue())
		})

		It("survives tiny screen sizes", func() {
			chart := newSampledChart()

			Expect(func() {
				renderChart(chart, 8, 3)
				renderChart(chart, 4, 2)
			}).ToNot(Panic())
		})
	})

	Describe("value formatting", func() {
		It("formats percent values", func() {
			Expect(charts.FormatValue(charts.MetricCPU, 18.4)).To(Equal("18.4%"))
			Expect(charts.FormatValue(charts.MetricCPU, 100)).To(Equal("100%"))
			Expect(charts.FormatValue(charts.MetricCPU, 0)).To(Equal("0%"))
		})

		It("formats byte values in binary units", func() {
			Expect(charts.FormatValue(charts.MetricMemory, 1024)).To(Equal("1KiB"))
			Expect(charts.FormatValue(charts.MetricMemory, 5*1024*1024)).
				To(Equal("5MiB"))
		})
	})

	Describe("concurrency", func() {
		It("supports concurrent pushes during draws", func() {
			chart := charts.NewChart()
			chart.EnsureSeries("live", "live", tcell.ColorDefault)

			var writers sync.WaitGroup

			writers.Go(func() {
				defer GinkgoRecover()

				tick := time.NewTicker(time.Microsecond)
				defer tick.Stop()

				for i := range 500 {
					<-tick.C
					chart.Push("live", float64(i%100))
				}
			})

			for i := range 50 {
				chart.Push("live", float64(i))
				renderChart(chart, 80, 24)
			}

			writers.Wait()

			// the final value depends on interleaving: either the main
			// loop's last push (49) or the writer's last push (499%100).
			Expect([]float64{49, 99}).To(ContainElement(
				chart.SeriesInfos()[0].Value))
		})
	})

	Describe("metric and symbol accessors", func() {
		It("stores the metric and symbol mode", func() {
			chart := charts.NewChart()

			Expect(chart.Metric()).To(Equal(charts.MetricCPU))
			Expect(chart.SymbolMode()).To(Equal(charts.SymbolBraille))

			chart.SetMetric(charts.MetricMemory)
			chart.SetSymbolMode(charts.SymbolTTY)

			Expect(chart.Metric()).To(Equal(charts.MetricMemory))
			Expect(chart.SymbolMode()).To(Equal(charts.SymbolTTY))
		})
	})
})
