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
	EventGridChargeDenied = "gridChargeDenied" // evcc's circuit check holds battery grid charging, A = 0, B = wanted
	EventPeak             = "peak"             // the battery started to cover a peak, A = demand, B = peak limit
	EventNotFollowing     = "notFollowing"     // a load keeps drawing more than allowed, A = power, B = allowed
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

// status is the Manager's record for the overview
type status struct {
	statusMu  sync.Mutex
	decisions map[Load]Decision
	events    []Event
}

// Record keeps a load's request and what it was allowed. It returns whether the
// load just became throttled: running, but allowed less than it asked for.
func (m *Manager) Record(l Load, requested, allowed float64, running bool, now time.Time) (throttledNow bool) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	// a few watts are rounding, not throttling
	throttled := running && requested > 0 && allowed < requested-50

	prev := m.decisions[l]
	m.decisions[l] = Decision{Requested: requested, Allowed: allowed, Throttled: throttled, At: now}

	return throttled && !prev.Throttled
}

// LastDecision returns a load's last recorded request
func (m *Manager) LastDecision(l Load) (Decision, bool) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	d, ok := m.decisions[l]
	return d, ok
}

// AddEvent adds an event to the log, dropping the oldest beyond maxEvents
func (m *Manager) AddEvent(e Event) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	m.events = append(m.events, e)
	if len(m.events) > maxEvents {
		m.events = m.events[len(m.events)-maxEvents:]
	}
}

// Events returns the log, newest first
func (m *Manager) Events() []Event {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	res := make([]Event, len(m.events))
	for i, e := range m.events {
		res[len(m.events)-1-i] = e
	}

	return res
}
