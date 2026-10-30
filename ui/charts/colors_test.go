package charts_test

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/containers/podman-tui/ui/charts"
	"github.com/containers/podman-tui/ui/style"
)

var _ = Describe("Palette assignment by first appearance", func() {
	It("hands out palette slots by first appearance", func() {
		chart := charts.NewChart()
		chart.EnsureSeries("1", "web", tcell.ColorDefault)
		chart.EnsureSeries("2", "db", tcell.ColorDefault)

		infos := chart.SeriesInfos()
		Expect(infos[0].Color).To(Equal(style.ChartPalette[0]))
		Expect(infos[1].Color).To(Equal(style.ChartPalette[1]))
	})

	It("gives 12 series 12 distinct colours", func() {
		chart := charts.NewChart()

		seen := map[int]bool{}

		for i := range 12 {
			id := fmt.Sprintf("id-%d", i)
			chart.EnsureSeries(id, fmt.Sprintf("series-%d", i), tcell.ColorDefault)

			color := chart.SeriesInfos()[i].Color
			slot := paletteSlotOf(color)

			Expect(slot).To(BeNumerically(">=", 0))
			Expect(seen[slot]).To(BeFalse(), "slot %d assigned twice", slot)
			seen[slot] = true
		}

		Expect(seen).To(HaveLen(12))
	})

	It("remembers names so re-appearing series keep their colour", func() {
		chart := charts.NewChart()
		chart.EnsureSeries("1", "web", tcell.ColorDefault)
		chart.EnsureSeries("2", "db", tcell.ColorDefault)
		chart.EnsureSeries("3", "cache", tcell.ColorDefault)

		// a new series with a known display name reuses the first slot
		// given to that name.
		chart.EnsureSeries("4", "web", tcell.ColorDefault)

		infos := chart.SeriesInfos()
		Expect(infos[0].Color).To(Equal(infos[3].Color))
	})

	It("keeps colours stable when the metric or symbol mode is toggled", func() {
		chart := charts.NewChart()
		chart.EnsureSeries("1", "web", tcell.ColorDefault)

		before := chart.SeriesInfos()[0].Color

		chart.SetMetric(charts.MetricMemory)
		chart.SetSymbolMode(charts.SymbolBlock)
		chart.EnsureSeries("1", "web", tcell.ColorDefault)

		Expect(chart.SeriesInfos()[0].Color).To(Equal(before))
	})

	It("falls back to a stable name hash when the palette is full", func() {
		buildFullChart := func() tcell.Color {
			chart := charts.NewChart()

			for i := range 12 {
				chart.EnsureSeries(fmt.Sprintf("id-%d", i),
					fmt.Sprintf("series-%d", i), tcell.ColorDefault)
			}

			// palette exhausted: the next series falls back to the hash
			chart.EnsureSeries("extra", "extra", tcell.ColorDefault)

			return chart.SeriesInfos()[12].Color
		}

		color := buildFullChart()
		Expect(paletteSlotOf(color)).To(BeNumerically(">=", 0))

		// the fallback hash is deterministic across assigners
		Expect(buildFullChart()).To(Equal(color))
	})

	It("honours an explicitly requested colour", func() {
		chart := charts.NewChart()
		chart.EnsureSeries("total", "podman total", tcell.ColorWhite)

		Expect(chart.SeriesInfos()[0].Color).To(Equal(tcell.ColorWhite))
	})
})

// paletteSlotOf returns the index of color in the chart palette, or -1
// when the colour is not a palette member.
func paletteSlotOf(color tcell.Color) int {
	for i, palette := range style.ChartPalette {
		if palette == color {
			return i
		}
	}

	return -1
}
