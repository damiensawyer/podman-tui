package charts

import "github.com/gdamore/tcell/v2"

// SymbolMode selects the terminal symbol set used to paint the canvas.
type SymbolMode int

const (
	// SymbolBraille uses U+2800..U+28FF braille glyphs: 2x4 sub-pixel dots
	// per character cell. Highest resolution mode.
	SymbolBraille SymbolMode = iota
	// SymbolBlock uses quadrant block glyphs: 2x2 sub-pixel dots per
	// character cell. Medium resolution mode.
	SymbolBlock
	// SymbolTTY uses shading glyphs which have no sub-cell geometry.
	// Fallback for raw /dev/tty consoles and non UTF-8 terminals.
	SymbolTTY
)

const (
	// subCellCols is the number of horizontal sub-pixel columns per cell
	// in braille and block modes.
	subCellCols = 2
	// brailleRows is the number of vertical sub-pixel rows per cell in
	// braille mode.
	brailleRows = 4
	// blockRows is the number of vertical sub-pixel rows per cell in
	// block mode.
	blockRows = 2
	// brailleBase is the first rune of the braille pattern block
	// (U+2800 BRAILLE PATTERN BLANK).
	brailleBase = 0x2800
	// ttyLevels is the number of TTY shading levels.
	ttyLevels = 5
	// ttyLevelMax is the highest TTY shading level (full block).
	ttyLevelMax = 4
)

// brailleDot maps a sub-cell (column, row) to its braille dot bitmask.
// Column 0 uses bits 0,1,2,6 and column 1 uses bits 3,4,5,7; rows are
// ordered top to bottom.
//
//nolint:mnd
var brailleDot = [2][4]rune{
	{0x01, 0x02, 0x04, 0x40},
	{0x08, 0x10, 0x20, 0x80},
}

// quadrant maps a 4-bit dot mask (bit0=top-left, bit1=top-right,
// bit2=bottom-left, bit3=bottom-right) to its block glyph.
//
//nolint:mnd
var quadrant = [16]rune{
	' ', '▘', '▝', '▀',
	'▖', '▌', '▞', '▛',
	'▗', '▚', '▐', '▜',
	'▄', '▙', '▟', '█',
}

// ttyFill maps a coverage level 0..4 to its shading glyph.
var ttyFill = [ttyLevels]rune{' ', '░', '▒', '▓', '█'}

// brailleFill maps the filled-from-bottom row levels (0..4) of the left and
// right sub-columns to a braille glyph. It backs the legend sparklines,
// which read better as filled area charts than as lines.
var brailleFill [ttyLevels][ttyLevels]rune

func init() {
	for left := range brailleFill {
		for right := range brailleFill[left] {
			brailleFill[left][right] = BrailleFillGlyph(left, right)
		}
	}
}

// BrailleFillGlyph returns the braille glyph whose left and right
// sub-columns are filled from the bottom with the given row levels
// (0..4). It is used by legend sparklines.
func BrailleFillGlyph(left, right int) rune {
	mask := brailleBase

	for row := brailleRows - left; row < brailleRows; row++ {
		mask |= int(brailleDot[0][row])
	}

	for row := brailleRows - right; row < brailleRows; row++ {
		mask |= int(brailleDot[1][row])
	}

	return rune(mask)
}

// MicroCanvas is a character-cell drawing surface with sub-cell resolution,
// in the spirit of btop's symbol engine.
//
// In braille and block modes every cell holds a dot bitmask and single
// sub-pixel dots are OR-ed into it, so intersecting line series merge
// instead of occluding each other. In TTY mode the shading glyphs have no
// sub-cell geometry, so every cell instead holds a quantised coverage level
// and columns are painted as filled areas.
//
// A MicroCanvas is not safe for concurrent use; the owning Chart serialises
// all access with its own mutex.
type MicroCanvas struct {
	width  int // cell width
	height int // cell height
	mode   SymbolMode

	masks []uint8       // per-cell dot mask (dot modes) or coverage level (TTY)
	fg    []tcell.Color // per-cell foreground owner colour
	ranks []float64     // per-cell rank of the foreground owner (dot modes)
}

// NewMicroCanvas returns a blank canvas of width x height character cells
// rendered with the given symbol mode.
func NewMicroCanvas(width, height int, mode SymbolMode) *MicroCanvas {
	size := width * height

	canvas := MicroCanvas{
		width:  width,
		height: height,
		mode:   mode,
		masks:  make([]uint8, size),
		fg:     make([]tcell.Color, size),
	}

	if mode != SymbolTTY {
		canvas.ranks = make([]float64, size)
	}

	return &canvas
}

// Width returns the canvas width in character cells.
func (c *MicroCanvas) Width() int {
	return c.width
}

// Height returns the canvas height in character cells.
func (c *MicroCanvas) Height() int {
	return c.height
}

// GridWidth returns the canvas width in sub-pixel dots.
func (c *MicroCanvas) GridWidth() int {
	return c.width * subCellCols
}

// GridHeight returns the canvas height in sub-pixel dots.
func (c *MicroCanvas) GridHeight() int {
	return c.height * c.dotRows()
}

// dotRows returns the number of vertical sub-pixel rows per cell.
func (c *MicroCanvas) dotRows() int {
	if c.mode == SymbolBraille {
		return brailleRows
	}

	if c.mode == SymbolBlock {
		return blockRows
	}

	return 1
}

// SetDot ORs a single sub-pixel dot into the cell that contains the grid
// coordinate (gx, gy). Out of range coordinates are ignored.
//
// rank resolves foreground colour ownership when several series write dots
// into the same cell: the colour of the highest ranked dot wins, so the
// series with the largest value in the column keeps its colour visible.
func (c *MicroCanvas) SetDot(gx, gy int, fg tcell.Color, rank float64) {
	if c.mode == SymbolTTY {
		return
	}

	dotRows := c.dotRows()
	if gx < 0 || gx >= c.GridWidth() || gy < 0 || gy >= c.GridHeight() {
		return
	}

	cx, cy := gx/subCellCols, gy/dotRows
	subCol, row := gx%subCellCols, gy%dotRows

	var bit rune
	if c.mode == SymbolBraille {
		bit = brailleDot[subCol][row]
	} else {
		bit = 1 << (row*subCellCols + subCol) //nolint:mnd
	}

	idx := cy*c.width + cx
	c.masks[idx] |= uint8(bit)

	if len(c.fg) == 0 || c.fg[idx] == tcell.ColorDefault || rank >= c.ranks[idx] {
		c.fg[idx] = fg
		c.ranks[idx] = rank
	}
}

// DrawLine rasterises a Bresenham line between the grid coordinates
// (gx0, gy0) and (gx1, gy1) as single sub-pixel dots. Colour ownership is
// resolved exactly like SetDot.
func (c *MicroCanvas) DrawLine(gx0, gy0, gx1, gy1 int, fg tcell.Color, rank float64) {
	dx := gx1 - gx0
	dy := gy1 - gy0

	stepX, stepY := 1, 1
	if dx < 0 {
		stepX = -1
	}

	if dy < 0 {
		stepY = -1
	}

	dx, dy = abs(dx), -abs(dy)
	err := dx + dy

	for {
		c.SetDot(gx0, gy0, fg, rank)

		if gx0 == gx1 && gy0 == gy1 {
			break
		}

		e2 := 2 * err //nolint:mnd
		if e2 >= dy {
			err += dy
			gx0 += stepX
		}

		if e2 <= dx {
			err += dx
			gy0 += stepY
		}
	}
}

// SetTTYLevel overwrites the coverage level 0..4 of a cell in TTY mode.
// Later calls overwrite earlier ones: callers paint series back-to-front
// so overlapping areas show the smaller series on top.
func (c *MicroCanvas) SetTTYLevel(x, y, level int, fg tcell.Color) {
	if c.mode != SymbolTTY || level < 0 || level > ttyLevelMax {
		return
	}

	if x < 0 || x >= c.width || y < 0 || y >= c.height {
		return
	}

	idx := y*c.width + x
	c.masks[idx] = uint8(level)
	c.fg[idx] = fg
}

// FillColumn paints the coverage of a single canvas column in TTY mode.
// The normalised height h (0.0..1.0) is quantised per cell row into one of
// the ttyFill shading levels, painting full rows below the value and a
// partial glyph on the boundary row.
func (c *MicroCanvas) FillColumn(x int, h float64, fg tcell.Color) {
	if c.mode != SymbolTTY || x < 0 || x >= c.width {
		return
	}

	for row := range c.height {
		bandBottom := float64(c.height-1-row) / float64(c.height)
		bandTop := float64(c.height-row) / float64(c.height)

		coverage := (min(h, bandTop) - bandBottom) / (bandTop - bandBottom)
		level := max(int(coverage*float64(ttyLevelMax)+0.5), 0) //nolint:mnd

		c.SetTTYLevel(x, row, level, fg)
	}
}

// Cell returns the glyph and foreground colour of a character cell. The
// third return value is false for cells without any dot or fill, in which
// case the caller should paint the cell with its own default style.
func (c *MicroCanvas) Cell(x, y int) (rune, tcell.Color, bool) {
	if x < 0 || x >= c.width || y < 0 || y >= c.height {
		return ' ', tcell.ColorDefault, false
	}

	idx := y*c.width + x

	if c.masks[idx] == 0 {
		return ' ', tcell.ColorDefault, false
	}

	switch c.mode {
	case SymbolBraille:
		return rune(brailleBase + int(c.masks[idx])), c.fg[idx], true
	case SymbolBlock:
		return quadrant[c.masks[idx]], c.fg[idx], true
	case SymbolTTY:
		level := min(int(c.masks[idx]), ttyLevelMax)
		return ttyFill[level], c.fg[idx], true
	default:
		return ' ', tcell.ColorDefault, false
	}
}

// Clear resets every cell to blank.
func (c *MicroCanvas) Clear() {
	clear(c.masks)
	clear(c.fg)
	clear(c.ranks)
}

// abs returns the absolute value of x.
func abs(x int) int {
	if x < 0 {
		return -x
	}

	return x
}
