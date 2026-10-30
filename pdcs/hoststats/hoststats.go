// Package hoststats reads host CPU usage locally for unix:// podman
// connections, preferring cgroup v2 cpu.stat and falling back to a
// /proc/stat delta.
//
// The package compiles on every target: on platforms without a local
// procfs (Windows, macOS) Sample simply reports ErrUnavailable. Remote
// podman connections use ssh:// anyway, where the load chart reports the
// podman container total instead of host wide CPU.
package hoststats

import "errors"

// ErrUnavailable is returned when host CPU usage cannot be read on the
// current platform.
var ErrUnavailable = errors.New("host cpu statistics are not available on this platform")
