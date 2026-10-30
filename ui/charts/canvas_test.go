package charts_test

import (
	"github.com/gdamore/tcell/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/containers/podman-tui/ui/charts"
)

// brailleBits matches the brailleDot table: bits per (sub-column, row),
// rows ordered top to bottom.
var brailleBits = [2][4]rune{
	{0x01, 0x02, 0x04, 0x40},
	{0x08, 0x10, 0x20, 0x80},
}

// brailleColumnBits is the OR of all dot bits of a braille sub-column.
var brailleColumnBits = [2]rune{0x01 | 0x02 | 0x04 | 0x40, 0x08 | 0x10 | 0x20 | 0x80}

// maskOf returns the dot bitmask of a braille glyph.
func maskOf(glyph rune) rune { return glyph - 0x2800 }

// brailleColumnHasDot reports whether any dot of grid column x is set in
// the braille glyph of the cell containing x.
func brailleColumnHasDot(glyph rune, subCol int) bool {
	return maskOf(glyph)&brailleColumnBits[subCol] != 0
}

var _ = Describe("MicroCanvas", func() {
	Describe("braille dot placement", func() {
		It("sets exactly one dot bit per sub-pixel coordinate", func() {
			canvas := charts.NewMicroCanvas(1, 1, charts.SymbolBraille)

			for col := range 2 {
				for row := range 4 {
					canvas.Clear()

					canvas.SetDot(col, row, tcell.ColorRed, 1)

					glyph, fg, ok := canvas.Cell(0, 0)
					Expect(ok).To(BeTrue())
					Expect(glyph).To(Equal(rune(0x2800 + brailleBits[col][row])))
					Expect(fg).To(Equal(tcell.ColorRed))
				}
			}
		})

		It("maps four vertical cells onto four distinct dot rows", func() {
			canvas := charts.NewMicroCanvas(1, 4, charts.SymbolBraille)

			Expect(canvas.GridWidth()).To(Equal(2))
			Expect(canvas.GridHeight()).To(Equal(16))

			// one dot in every cell row at grid row = cellRow * 4
			for cellRow := range 4 {
				canvas.Clear()
				canvas.SetDot(0, cellRow*4, tcell.ColorRed, 1)

				glyph, _, ok := canvas.Cell(0, cellRow)
				Expect(ok).To(BeTrue())
				Expect(glyph).To(Equal(rune(0x2800 + 0x01)))
			}
		})

		It("ignores out of range dots", func() {
			canvas := charts.NewMicroCanvas(2, 2, charts.SymbolBraille)

			canvas.SetDot(-1, 0, tcell.ColorRed, 1)
			canvas.SetDot(0, -1, tcell.ColorRed, 1)
			canvas.SetDot(4, 0, tcell.ColorRed, 1)
			canvas.SetDot(0, 8, tcell.ColorRed, 1)

			for x := range 2 {
				for y := range 2 {
					_, _, ok := canvas.Cell(x, y)
					Expect(ok).To(BeFalse())
				}
			}
		})
	})

	Describe("quadrant block placement", func() {
		It("maps each of the four dots to its block glyph", func() {
			// bit0=top-left, bit1=top-right, bit2=bottom-left, bit3=bottom-right
			expectedGlyphs := map[[2]int]rune{
				{0, 0}: '▘', // top-left
				{1, 0}: '▝', // top-right
				{0, 1}: '▖', // bottom-left
				{1, 1}: '▗', // bottom-right
			}

			for dot, expected := range expectedGlyphs {
				canvas := charts.NewMicroCanvas(1, 1, charts.SymbolBlock)
				canvas.SetDot(dot[0], dot[1], tcell.ColorRed, 1)

				glyph, _, ok := canvas.Cell(0, 0)
				Expect(ok).To(BeTrue())
				Expect(glyph).To(Equal(expected))
			}
		})

		It("combines four dots into the full block glyph", func() {
			canvas := charts.NewMicroCanvas(1, 1, charts.SymbolBlock)

			for gx := range 2 {
				for gy := range 2 {
					canvas.SetDot(gx, gy, tcell.ColorRed, 1)
				}
			}

			glyph, _, ok := canvas.Cell(0, 0)
			Expect(ok).To(BeTrue())
			Expect(glyph).To(Equal('█'))
		})

		It("uses a 2x2 sub-pixel grid per cell", func() {
			canvas := charts.NewMicroCanvas(10, 5, charts.SymbolBlock)

			Expect(canvas.GridWidth()).To(Equal(20))
			Expect(canvas.GridHeight()).To(Equal(10))
		})
	})

	Describe("dot merging and colour ownership", func() {
		It("ORs dots of intersecting series into one glyph", func() {
			canvas := charts.NewMicroCanvas(1, 1, charts.SymbolBraille)

			canvas.SetDot(0, 0, tcell.ColorRed, 1)    // top-left dot
			canvas.SetDot(1, 3, tcell.ColorBlue, 0.5) // bottom-right dot

			glyph, fg, ok := canvas.Cell(0, 0)
			Expect(ok).To(BeTrue())
			Expect(glyph).To(Equal(rune(0x2800 + 0x01 + 0x80)))
			// the higher ranked series keeps the cell colour
			Expect(fg).To(Equal(tcell.ColorRed))
		})

		It("assigns the cell colour to the highest valued series", func() {
			canvas := charts.NewMicroCanvas(1, 1, charts.SymbolBraille)

			canvas.SetDot(0, 0, tcell.ColorRed, 0.2)
			canvas.SetDot(1, 3, tcell.ColorBlue, 0.9)

			_, fg, _ := canvas.Cell(0, 0)
			Expect(fg).To(Equal(tcell.ColorBlue))
		})

		It("keeps the last writer on equal ranks", func() {
			canvas := charts.NewMicroCanvas(1, 1, charts.SymbolBraille)

			canvas.SetDot(0, 0, tcell.ColorRed, 0.5)
			canvas.SetDot(1, 3, tcell.ColorBlue, 0.5)

			_, fg, _ := canvas.Cell(0, 0)
			Expect(fg).To(Equal(tcell.ColorBlue))
		})
	})

	Describe("Bresenham line rasterizer", func() {
		It("produces a contiguous horizontal path without gaps", func() {
			canvas := charts.NewMicroCanvas(10, 1, charts.SymbolBraille)
			canvas.DrawLine(0, 1, 19, 1, tcell.ColorRed, 1)

			for gx := range 20 {
				glyph, _, ok := canvas.Cell(gx/2, 0)
				Expect(ok).To(BeTrue(), "grid column %d has no dot", gx)
				Expect(brailleColumnHasDot(glyph, gx%2)).To(
					BeTrue(), "grid column %d has no dot", gx)
			}
		})

		It("rasterises a steep descending line touching every grid column", func() {
			canvas := charts.NewMicroCanvas(10, 4, charts.SymbolBraille)
			canvas.DrawLine(0, 0, 19, 15, tcell.ColorRed, 1)

			for gx := range 20 {
				cellX, subCol := gx/2, gx%2
				// the line may pass through either cell row of cellX;
				// accept a dot in any row.
				found := false
				for cellY := range 4 {
					glyph, _, ok := canvas.Cell(cellX, cellY)
					if ok && brailleColumnHasDot(glyph, subCol) {
						found = true

						break
					}
				}

				Expect(found).To(BeTrue(), "grid column %d has no dot", gx)
			}
		})

		It("rasterises ascending and reversed lines identically", func() {
			upward := charts.NewMicroCanvas(5, 2, charts.SymbolBlock)
			reversed := charts.NewMicroCanvas(5, 2, charts.SymbolBlock)

			upward.DrawLine(0, 9, 9, 0, tcell.ColorRed, 1)
			reversed.DrawLine(9, 0, 0, 9, tcell.ColorRed, 1)

			for x := range 5 {
				for y := range 2 {
					upGlyph, _, upOK := upward.Cell(x, y)
					revGlyph, _, revOK := reversed.Cell(x, y)
					Expect(upOK).To(Equal(revOK))
					Expect(upGlyph).To(Equal(revGlyph))
				}
			}
		})

		It("draws a single dot for identical endpoints", func() {
			canvas := charts.NewMicroCanvas(2, 2, charts.SymbolBraille)
			canvas.DrawLine(1, 1, 1, 1, tcell.ColorRed, 1)

			// grid (1, 1) is sub-column 1, row 1 -> bit 0x10
			glyph, _, ok := canvas.Cell(0, 0)
			Expect(ok).To(BeTrue())
			Expect(glyph).To(Equal(rune(0x2800 + 0x10)))
		})
	})

	Describe("TTY fill rendering", func() {
		It("quantises a full column to full blocks", func() {
			canvas := charts.NewMicroCanvas(1, 4, charts.SymbolTTY)
			canvas.FillColumn(0, 1.0, tcell.ColorRed)

			for row := range 4 {
				glyph, fg, ok := canvas.Cell(0, row)
				Expect(ok).To(BeTrue())
				Expect(glyph).To(Equal('█'))
				Expect(fg).To(Equal(tcell.ColorRed))
			}
		})

		It("leaves an empty column blank", func() {
			canvas := charts.NewMicroCanvas(1, 4, charts.SymbolTTY)
			canvas.FillColumn(0, 0.0, tcell.ColorRed)

			for row := range 4 {
				_, _, ok := canvas.Cell(0, row)
				Expect(ok).To(BeFalse())
			}
		})

		It("paints a partial boundary cell with a shading glyph", func() {
			canvas := charts.NewMicroCanvas(1, 4, charts.SymbolTTY)
			// 0.625 fills the bottom two rows fully and half of row 1.
			canvas.FillColumn(0, 0.625, tcell.ColorRed)

			glyph, _, ok := canvas.Cell(0, 1)
			Expect(ok).To(BeTrue())
			Expect(glyph).To(Equal('▒')) // coverage 0.5 -> level 2

			glyph, _, ok = canvas.Cell(0, 2)
			Expect(ok).To(BeTrue())
			Expect(glyph).To(Equal('█'))

			glyph, _, ok = canvas.Cell(0, 3)
			Expect(ok).To(BeTrue())
			Expect(glyph).To(Equal('█'))

			_, _, ok = canvas.Cell(0, 0)
			Expect(ok).To(BeFalse())
		})

		It("lets later paints overwrite earlier ones", func() {
			canvas := charts.NewMicroCanvas(1, 1, charts.SymbolTTY)
			canvas.SetTTYLevel(0, 0, 4, tcell.ColorRed)
			canvas.SetTTYLevel(0, 0, 1, tcell.ColorBlue)

			glyph, fg, ok := canvas.Cell(0, 0)
			Expect(ok).To(BeTrue())
			Expect(glyph).To(Equal('░'))
			Expect(fg).To(Equal(tcell.ColorBlue))
		})

		It("rejects out of range levels and coordinates", func() {
			canvas := charts.NewMicroCanvas(1, 1, charts.SymbolTTY)

			canvas.SetTTYLevel(0, 0, 5, tcell.ColorRed)
			canvas.SetTTYLevel(-1, 0, 4, tcell.ColorRed)
			canvas.SetTTYLevel(0, 9, 4, tcell.ColorRed)

			_, _, ok := canvas.Cell(0, 0)
			Expect(ok).To(BeFalse())
		})
	})

	Describe("brailleFill sparkline table", func() {
		It("starts at the blank braille pattern", func() {
			Expect(charts.BrailleFillGlyph(0, 0)).To(Equal(rune(0x2800)))
		})

		It("fills both columns completely at level 4", func() {
			Expect(charts.BrailleFillGlyph(4, 4)).To(Equal(rune(0x28FF)))
		})

		It("fills single columns bottom-up", func() {
			// left column with 1 filled row = the bottom-left dot (bit 0x40)
			Expect(charts.BrailleFillGlyph(1, 0)).To(Equal(rune(0x2840)))
			// right column with 1 filled row = the bottom-right dot (bit 0x80)
			Expect(charts.BrailleFillGlyph(0, 1)).To(Equal(rune(0x2880)))
			// left column fully filled = bits 0x01|0x02|0x04|0x40
			Expect(charts.BrailleFillGlyph(4, 0)).To(Equal(rune(0x2847)))
		})

		It("accumulates rows monotonically", func() {
			previous := charts.BrailleFillGlyph(0, 0)
			for level := 1; level < 5; level++ {
				glyph := charts.BrailleFillGlyph(level, level)
				Expect(maskOf(glyph) & maskOf(previous)).To(Equal(maskOf(previous)))
				previous = glyph
			}
		})
	})
})
