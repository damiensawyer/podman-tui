package style

import (
	"github.com/gdamore/tcell/v2"
)

// ChartPalette holds the 12 high contrast chart series colours for dark
// backgrounds. They are defined as hex values because several conventional
// colour names (amber, bright green, ...) do not exist in tcell.
//
// This file is shared by all build targets: the chart code depends on the
// palette being available on every platform.
var ChartPalette = []tcell.Color{ //nolint:mnd
	tcell.NewHexColor(0x00D7FF), // cyan
	tcell.NewHexColor(0x5FFF5F), // green
	tcell.NewHexColor(0xFFAF00), // orange
	tcell.NewHexColor(0xFF5FD7), // magenta
	tcell.NewHexColor(0x5F87FF), // blue
	tcell.NewHexColor(0xFFFF5F), // yellow
	tcell.NewHexColor(0xFF87AF), // pink
	tcell.NewHexColor(0x00AF87), // teal
	tcell.NewHexColor(0xAFFF00), // lime
	tcell.NewHexColor(0xAF87FF), // violet
	tcell.NewHexColor(0xFFD700), // amber
	tcell.NewHexColor(0xFF7F50), // coral
}
