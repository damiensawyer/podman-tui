// Package charts implements the load chart feature: a live multi-series
// CPU/memory chart drawn on a single shared canvas with common axes.
//
// The package is split in three layers:
//
//   - canvas.go: MicroCanvas, a character-cell drawing surface with sub-cell
//     resolution (braille/block/TTY symbol modes).
//   - series.go, chart.go: the Chart tview primitive with per-series ring
//     buffers, axes and legend.
//   - collector.go: background sampler which feeds the chart from podman
//     container stats streams.
//
// The canvas and chart layers contain no podman specific dependencies so
// they can be reused for other visualisations.
package charts
