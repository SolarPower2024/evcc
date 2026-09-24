package lm

// What load management decided, for the overview in the ui: each load's last
// request and what it was allowed, and a short log of the events worth showing
// (shed, throttled, grid charging paused). Nothing here feeds back into the
// decisions themselves.

import (
	"sync"
	"time"
)

// Decision is a load's last request against its circuit
type Decision struct {
	Requested float64   // power asked for in W, 0 = nothing
	Allowed   float64   // power the circuit allowed in W
	Throttled bool      // allowed less than requested, but running
	At        time.Time // when
}

// Event types
const (
	EventShed             = "shed"             // a running load was switched off, A = guard minutes, B = circuit power
	EventThrottled        = "throttled"        // a load was limited, A = requested, B = allowed
	EventGridChargePaused = "gridChargePaused" // a peak paused battery grid charging, A = demand, B = peak limit
	EventGridChargeDenied = "gridChargeDenied" // the circuit denied battery grid charging, A = allowed, B = wanted
	EventPeak             = "peak"             // the battery started to cover a peak, A = demand, B = peak limit
)

// Event is an entry of the load management log
type Event struct {
	At   time.Time `json:"at"`
	Type string    `json:"type"`
	Load string    `json:"load,omitempty"`
	A    float64   `json:"a"`
	B    float64   `json:"b"`
}

// maxEvents is how many events the log keeps
const maxEvents = 20

var (
	statusMu  sync.Mutex
	decisions = make(map[Load]Decision)
	events    []Event
)

// Record keeps a load's request and what it was allowed. It returns whether the
// load just became throttled: running, but allowed less than it asked for.
func Record(l Load, requested, allowed float64, running bool, now time.Time) (throttledNow bool) {
	statusMu.Lock()
	defer statusMu.Unlock()

	// a few watts are rounding, not throttling
	throttled := running && requested > 0 && allowed < requested-50

	prev := decisions[l]
	decisions[l] = Decision{Requested: requested, Allowed: allowed, Throttled: throttled, At: now}

	return throttled && !prev.Throttled
}

// LastDecision returns a load's last recorded request
func LastDecision(l Load) (Decision, bool) {
	statusMu.Lock()
	defer statusMu.Unlock()

	d, ok := decisions[l]
	return d, ok
}

// AddEvent adds an event to the log, dropping the oldest beyond maxEvents
func AddEvent(e Event) {
	statusMu.Lock()
	defer statusMu.Unlock()

	events = append(events, e)
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
}

// Events returns the log, newest first
func Events() []Event {
	statusMu.Lock()
	defer statusMu.Unlock()

	res := make([]Event, len(events))
	for i, e := range events {
		res[len(events)-1-i] = e
	}

	return res
}

func resetStatus() {
	statusMu.Lock()
	defer statusMu.Unlock()
	clear(decisions)
	events = nil
}
