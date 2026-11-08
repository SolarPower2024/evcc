package lm

// What load management decided, for the overview in the ui: each load's last
// request and what it was allowed, and a short log of the events worth showing
// (shed, throttled, grid charging paused, the peak status of the battery). Nothing
// here feeds back into the decisions themselves.

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
	EventGridChargePaused = "gridChargePaused" // a peak paused battery grid charging, A = demand, B = grid power allowed
	EventGridChargeDenied = "gridChargeDenied" // the circuit denied battery grid charging, A = allowed, B = wanted
	EventPeak             = "peak"             // the battery started to cover a peak, A = demand, B = peak limit
	EventNotFollowing     = "notFollowing"     // a load keeps drawing more than allowed, A = power, B = allowed

	// the peak status of the battery page, see core/site_peak_status.go
	EventPeakEnd      = "peakEnd"      // a peak is over, A = most the battery gave in W, B = duration in s
	EventReserveBelow = "reserveBelow" // the soc fell below the reserve, A = soc, B = reserve
	EventReserveAbove = "reserveAbove" // the soc rose above the reserve, A = soc, B = reserve
	EventBatteryMode  = "batteryMode"  // evcc changed the battery mode, Load = the mode
	EventPeakSwitch   = "peakSwitch"   // peak shaving was switched, A = 1 on, 0 off
	EventMeter        = "meter"        // the grid meter failed or is back, A = 1 outage, 0 back
	EventStorage      = "storage"      // the storage is not reachable or connected again, A = 1 lost, 0 connected
)

// PeakEvent reports whether an event type belongs to the peak status
func PeakEvent(typ string) bool {
	switch typ {
	case EventPeak, EventPeakEnd, EventReserveBelow, EventReserveAbove, EventBatteryMode,
		EventPeakSwitch, EventMeter, EventStorage, EventGridChargePaused:
		return true
	}

	return false
}

// Event is an entry of the load management log
type Event struct {
	At   time.Time `json:"at"`
	Type string    `json:"type"`
	Load string    `json:"load,omitempty"`
	A    float64   `json:"a"`
	B    float64   `json:"b"`
}

const (
	// maxEvents and maxEventAge are what the log keeps; it survives a restart
	maxEvents   = 300
	maxEventAge = 7 * 24 * time.Hour

	// overviewEvents is how many events the overview publishes
	overviewEvents = 20
)

// status is the Manager's record for the overview
type status struct {
	statusMu  sync.Mutex
	decisions map[Load]Decision
	events    []Event
	dirty     bool // events changed since the last Snapshot
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

// AddEvent adds an event to the log, dropping the oldest beyond maxEvents and
// those older than maxEventAge
func (m *Manager) AddEvent(e Event) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	m.events = trimEvents(append(m.events, e), e.At)
	m.dirty = true
}

// trimEvents drops the events older than maxEventAge before now and the oldest
// beyond maxEvents. Without a time only the number counts.
func trimEvents(events []Event, now time.Time) []Event {
	if !now.IsZero() {
		i := 0
		for i < len(events) && now.Sub(events[i].At) > maxEventAge {
			i++
		}
		events = events[i:]
	}

	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}

	return events
}

// Events returns the newest events for the overview, newest first
func (m *Manager) Events() []Event {
	return m.newest(overviewEvents, nil)
}

// AllEvents returns the whole log, newest first. With a filter only the events
// it accepts.
func (m *Manager) AllEvents(accept func(Event) bool) []Event {
	return m.newest(maxEvents, accept)
}

func (m *Manager) newest(limit int, accept func(Event) bool) []Event {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	res := make([]Event, 0, min(limit, len(m.events)))
	for i := len(m.events) - 1; i >= 0 && len(res) < limit; i-- {
		if accept == nil || accept(m.events[i]) {
			res = append(res, m.events[i])
		}
	}

	return res
}

// Snapshot returns the log oldest first for storing and whether it changed since
// the last snapshot
func (m *Manager) Snapshot() ([]Event, bool) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	changed := m.dirty
	m.dirty = false

	return append([]Event(nil), m.events...), changed
}

// Restore loads a stored log, oldest first. What was logged before stays on top.
func (m *Manager) Restore(events []Event, now time.Time) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	all := append(append([]Event(nil), events...), m.events...)
	m.events = trimEvents(all, now)
}
