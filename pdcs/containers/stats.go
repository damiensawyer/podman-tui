package containers

import (
	"context"
	"errors"

	"github.com/containers/podman-tui/pdcs/registry"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/domain/entities"
	"github.com/rs/zerolog/log"
)

// ErrStatsStreamNoContainers is returned when a stats stream is requested
// without any container id.
var ErrStatsStreamNoContainers = errors.New("no containers to stream stats for")

// Stats returns live stream of containers stats result.
func Stats(id string, opts *containers.StatsOptions) (chan entities.ContainerStatsReport, error) {
	log.Debug().Msgf("pdcs: podman container stats %s", id)

	conn, err := registry.GetConnection()
	if err != nil {
		return nil, err
	}

	if opts == nil {
		opts = new(containers.StatsOptions)
	}

	if opts.Interval == nil {
		// the server defaults to one report every five seconds when the
		// interval is unset; match the one second UI refresh instead.
		interval := 1
		opts.Interval = &interval
	}

	statReportChan, err := containers.Stats(conn, []string{id}, opts)
	if err != nil {
		return nil, err
	}

	return statReportChan, nil
}

// StatsStream returns a streaming stats channel for the given containers.
//
// The context must derive from registry.GetConnection(), because the
// bindings fetch the HTTP client from it; the caller owns the context and
// cancels it to stop the stream. After cancellation the bindings goroutine
// sends one final report with Error != nil on the unbuffered channel and
// then closes it, so readers must keep consuming until the channel is
// closed instead of returning on cancel.
func StatsStream(ctx context.Context, ids []string, intervalSec int) (<-chan entities.ContainerStatsReport, error) {
	log.Debug().Msgf("pdcs: podman container stats stream %v", ids)

	if len(ids) == 0 {
		return nil, ErrStatsStreamNoContainers
	}

	opts := new(containers.StatsOptions)

	stream := true
	opts.Stream = &stream
	opts.Interval = &intervalSec

	statReportChan, err := containers.Stats(ctx, ids, opts)
	if err != nil {
		return nil, err
	}

	return statReportChan, nil
}
