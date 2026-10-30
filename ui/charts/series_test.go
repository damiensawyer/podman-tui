package charts_test

import (
	"github.com/gdamore/tcell/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/containers/podman-tui/ui/charts"
)

var _ = Describe("Series", func() {
	It("keeps the newest WindowSize samples", func() {
		series := charts.NewSeries("id", "name", tcell.ColorRed)

		for i := range 100 {
			series.Push(float64(i))
		}

		Expect(series.Len()).To(Equal(charts.WindowSize))
		Expect(series.At(0)).To(Equal(float64(100 - charts.WindowSize)))
		Expect(series.At(charts.WindowSize - 1)).To(Equal(float64(99)))
		Expect(series.Last()).To(Equal(float64(99)))
	})

	It("returns zero for out of range indices", func() {
		series := charts.NewSeries("id", "name", tcell.ColorRed)

		series.Push(1)

		Expect(series.At(-1)).To(Equal(0.0))
		Expect(series.At(1)).To(Equal(0.0))
		Expect(series.Last()).To(Equal(1.0))
	})

	It("reports the window maximum", func() {
		series := charts.NewSeries("id", "name", tcell.ColorRed)

		Expect(series.Max()).To(Equal(0.0))

		for _, v := range []float64{3, 9, 1, 42, 7} {
			series.Push(v)
		}

		Expect(series.Max()).To(Equal(42.0))
	})

	It("wraps the ring buffer without losing order", func() {
		series := charts.NewSeries("id", "name", tcell.ColorRed)

		for i := range charts.WindowSize {
			series.Push(float64(i))
		}
		// push two more: the two oldest wrap around
		series.Push(1000)
		series.Push(2000)

		Expect(series.Len()).To(Equal(charts.WindowSize))
		Expect(series.At(0)).To(Equal(2.0))
		Expect(series.At(1)).To(Equal(3.0))
		Expect(series.At(charts.WindowSize - 2)).To(Equal(1000.0))
		Expect(series.At(charts.WindowSize - 1)).To(Equal(2000.0))
	})

	It("resets to an empty window keeping identity", func() {
		series := charts.NewSeries("id", "name", tcell.ColorRed)

		series.Push(1)
		series.Reset()

		Expect(series.Len()).To(Equal(0))
		Expect(series.ID()).To(Equal("id"))
		Expect(series.Name()).To(Equal("name"))
		Expect(series.Color()).To(Equal(tcell.ColorRed))

		series.Push(7)
		Expect(series.At(0)).To(Equal(7.0))
	})

	It("updates its display name", func() {
		series := charts.NewSeries("id", "old", tcell.ColorRed)
		series.SetName("new")

		Expect(series.Name()).To(Equal("new"))
	})
})
