package charts

import (
	"github.com/gdamore/tcell/v2"
)

// WindowSize is the number of samples kept per series. Collection runs at
// one sample per second (utils.RefreshInterval), so the window spans the
// last 60 seconds.
const WindowSize = 60

// Series is a named measurement curve backed by a fixed size ring buffer.
// Samples are pushed by the collector once per second and read by the
// Chart renderer; access is serialised by the owning Chart's mutex.
type Series struct {
	id    string
	name  string
	color tcell.Color

	buf  []float64
	head int // next write index
	size int // number of valid samples, capped at WindowSize
}

// NewSeries returns a series with the given id, display name and colour.
func NewSeries(id, name string, color tcell.Color) *Series {
	return &Series{
		id:    id,
		name:  name,
		color: color,
		buf:   make([]float64, WindowSize),
	}
}

// ID returns the unique series identifier.
func (s *Series) ID() string {
	return s.id
}

// Name returns the series display name.
func (s *Series) Name() string {
	return s.name
}

// SetName updates the series display name, e.g. after a container rename.
func (s *Series) SetName(name string) {
	s.name = name
}

// Color returns the series colour.
func (s *Series) Color() tcell.Color {
	return s.color
}

// Push appends one sample, overwriting the oldest once the window is full.
func (s *Series) Push(value float64) {
	s.buf[s.head] = value
	s.head = (s.head + 1) % WindowSize

	if s.size < WindowSize {
		s.size++
	}
}

// Len returns the number of valid samples in the window.
func (s *Series) Len() int {
	return s.size
}

// At returns the i-th oldest sample of the window.
func (s *Series) At(i int) float64 {
	if i < 0 || i >= s.size {
		return 0
	}

	start := (s.head - s.size + WindowSize) % WindowSize

	return s.buf[(start+i)%WindowSize]
}

// Last returns the most recent sample.
func (s *Series) Last() float64 {
	if s.size == 0 {
		return 0
	}

	return s.At(s.size - 1)
}

// Max returns the largest sample of the window, or 0 when empty.
func (s *Series) Max() float64 {
	maxValue := 0.0

	for i := range s.size {
		maxValue = max(maxValue, s.At(i))
	}

	return maxValue
}

// Reset discards all samples, keeping the series identity.
func (s *Series) Reset() {
	s.head = 0
	s.size = 0
}
