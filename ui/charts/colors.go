package charts

import (
	"hash/fnv"

	"github.com/containers/podman-tui/ui/style"
	"github.com/gdamore/tcell/v2"
)

// colorAssigner hands out chart palette slots by first appearance.
//
// A pure name hash collides often at realistic series counts, so a new
// series always receives the next unused palette slot and the name is
// remembered for the assigner's lifetime: toggling metric, symbol mode or
// group-by never recolours a series. Only when all slots are taken does
// it fall back to a hash of the name.
type colorAssigner struct {
	byName map[string]int
	used   int
}

func newColorAssigner() *colorAssigner {
	return &colorAssigner{byName: make(map[string]int)}
}

// colorFor returns the colour for a series display name.
func (a *colorAssigner) colorFor(name string) tcell.Color {
	if slot, ok := a.byName[name]; ok {
		return style.ChartPalette[slot]
	}

	if a.used < len(style.ChartPalette) {
		slot := a.used
		a.byName[name] = slot
		a.used++

		return style.ChartPalette[slot]
	}

	return style.ChartPalette[fnv1a(name)%len(style.ChartPalette)]
}

// fnv1a returns the FNV-1a 32 bit hash of s.
func fnv1a(s string) int {
	h := fnv.New32a()

	_, _ = h.Write([]byte(s))

	return int(h.Sum32())
}
