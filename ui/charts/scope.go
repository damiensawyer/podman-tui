package charts

import (
	"slices"
)

// Scope selects what the load chart is drawn for.
type Scope int

const (
	// ScopeSystem draws the whole system: all pods and containers of the
	// connected destination.
	ScopeSystem Scope = iota
	// ScopePod draws a single selected pod.
	ScopePod
	// ScopeContainer draws a single selected container.
	ScopeContainer
)

// ScopeTarget identifies the pod or container a dialog scope points at.
// Only the fields matching the scope are meaningful.
type ScopeTarget struct {
	PodID         string
	PodName       string
	ContainerID   string
	ContainerName string
}

// GroupMode selects what an individual curve represents.
type GroupMode int

const (
	// GroupTotal draws one aggregate line.
	GroupTotal GroupMode = iota
	// GroupPod draws one line per pod (system scope only).
	GroupPod
	// GroupContainer draws one line per container (system and pod
	// scopes).
	GroupContainer
	// GroupProcess draws one line per top process (milestone 6).
	GroupProcess
)

// availableGroups returns the group modes the scope supports, in cycling
// order. Process mode (CPU only) is available at every scope.
func (s Scope) availableGroups() []GroupMode {
	switch s {
	case ScopeSystem:
		return []GroupMode{GroupTotal, GroupPod, GroupContainer, GroupProcess}
	case ScopePod:
		return []GroupMode{GroupTotal, GroupContainer, GroupProcess}
	case ScopeContainer:
		return []GroupMode{GroupTotal, GroupProcess}
	default:
		return []GroupMode{GroupTotal}
	}
}

// resolves maps a requested group mode to one the scope actually
// supports.
func (s Scope) resolve(group GroupMode) GroupMode {
	if slices.Contains(s.availableGroups(), group) {
		return group
	}

	return GroupTotal
}

// Series identifiers.
const (
	// totalSeriesID is the fixed id of the aggregate series.
	totalSeriesID = "total"
	// podSeriesPrefix prefixes pod aggregate series ids.
	podSeriesPrefix = "pod:"
	// cntSeriesPrefix prefixes container series ids.
	cntSeriesPrefix = "cnt:"
	// procSeriesPrefix prefixes per process series ids.
	procSeriesPrefix = "proc:"
	// otherSeriesID is the series aggregating processes beyond the top N.
	otherSeriesID = "other"
	// evictionTicks is the number of consecutive ticks a series may be
	// absent from the container listing before it is evicted.
	evictionTicks = 60
	// processTopN is the number of processes kept as individual series;
	// the remainder is aggregated into the "other" series.
	processTopN = 12
	// processPollRate is the number of collector ticks between process
	// table polls: the top endpoint is one round trip per container, so
	// polling runs on a relaxed cycle.
	processPollRate = 4
)
