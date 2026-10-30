//go:build !linux

package hoststats

// Reader produces host CPU usage samples on platforms with a local
// procfs. On non linux targets every Sample reports ErrUnavailable;
// remote podman connections are ssh:// based anyway.
type Reader struct{}

// NewReader returns a new host CPU reader.
func NewReader() *Reader {
	return &Reader{}
}

// Sample returns the host CPU usage in percent since the previous call.
func (r *Reader) Sample() (float64, error) {
	return 0, ErrUnavailable
}
