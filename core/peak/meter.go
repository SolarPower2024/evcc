package peak

import (
	"math"
	"time"
)

// Sample is one cycle's reading
type Sample struct {
	Now          time.Time
	GridPower    float64 // W, import positive
	BatteryPower float64 // W, discharging positive
	Energy       float64 // grid import counter in kWh, if Source is a counter
	Source       string  // where Energy comes from, SourcePower without a counter
}

// Settings limit the allowed power
type Settings struct {
	Limit  float64       // W
	Freeze time.Duration // from this minute of the window on it no longer grows
	Cap    float64       // at most this multiple of the limit
}

// Completed is a finished window metered from its start
type Completed struct {
	Start    time.Time
	DrawnWs  float64 // grid energy drawn
	DemandWs float64 // the same without the battery
}

// State is the running window after a sample
type State struct {
	Start       time.Time
	MeteredFrom time.Time // later than Start after a restart or a gap
	DrawnWs     float64   // grid energy drawn in the window so far
	Avg         float64   // average grid power since MeteredFrom in W
	Allowed     float64   // grid power that keeps the window average at the limit in W
	Source      string    // where this sample's energy came from
	Stale       bool      // the counter stopped updating, grid power used for the rest of the window
	StaleNow    bool      // ... and did so with this sample
	Completed   *Completed
}

// End returns the end of the window
func (s State) End() time.Time {
	return s.Start.Add(Window)
}

// Meter tracks the grid energy of the running window. The energy drawn comes
// from an import counter when the sample has one, else from the grid power.
// What it did not see, after a start or a gap, counts at the limit: assuming
// less could spend a budget that was already used. Not safe for concurrent use.
type Meter struct {
	windowStart time.Time
	meteredFrom time.Time
	windowWs    float64
	demandWs    float64
	lastSample  time.Time
	avg         float64 // average grid power since meteredFrom, kept while nothing is metered yet
	frozen      float64 // allowed power at the freeze minute
	isFrozen    bool

	source       string    // source of the last sample
	lastEnergy   float64   // counter at the last sample in kWh, valid if source is a counter
	unmovedWs    float64   // drawn according to the grid power while the counter stood still
	unmovedSince time.Time // the counter has not moved since
	stale        bool
}

// drawn returns the energy drawn since the last sample in Ws. A counter is used
// when it was read now and last time from the same source, the grid power fills
// in otherwise.
func (m *Meter) drawn(now time.Time, imported, energy float64, source string) float64 {
	powerWs := imported * now.Sub(m.lastSample).Seconds()

	if source == SourcePower || source != m.source || m.stale {
		return powerWs
	}

	switch d := (energy - m.lastEnergy) * 3600e3; {
	case d < 0:
		// counter reset or replaced
		return powerWs

	case d > 0:
		m.unmovedWs, m.unmovedSince = 0, time.Time{}
		return d
	}

	// the counter stands still: nothing drawn, or it is late and catches up
	// with its next step. If it does not, the grid power takes over.
	if powerWs == 0 {
		return 0
	}
	if m.unmovedSince.IsZero() {
		m.unmovedSince = m.lastSample
	}
	m.unmovedWs += powerWs

	if m.unmovedWs < staleWs || now.Sub(m.unmovedSince) <= MaxGap {
		return 0
	}

	m.stale = true
	return m.unmovedWs
}

// Update takes the sample of a cycle and returns the window's state. A window
// that ends with it and was metered from its start is returned as Completed,
// with the battery power added back for the demand without it.
func (m *Meter) Update(s Sample, set Settings) State {
	now := s.Now
	source := s.Source

	// clock-aligned windows, matching how the meter registers them
	start := now.Truncate(Window)

	// only the import direction contributes to the demand peak
	imported := math.Max(0, s.GridPower)

	var res State

	var drawn, demand float64
	if !m.lastSample.IsZero() {
		wasStale := m.stale
		drawn = m.drawn(now, imported, s.Energy, source)
		demand = max(0, drawn+s.BatteryPower*now.Sub(m.lastSample).Seconds())
		res.StaleNow = m.stale && !wasStale
	}

	if !m.windowStart.Equal(start) {
		// a sample from the previous window carries over: the part of the
		// interval since the boundary is metered, the rest was the last window's
		if !m.lastSample.IsZero() && m.lastSample.Before(start) && now.Sub(m.lastSample) <= MaxGap {
			after := now.Sub(start).Seconds() / now.Sub(m.lastSample).Seconds()

			// only a window metered from its start counts for the statistics
			if m.meteredFrom.Equal(m.windowStart) && !m.windowStart.IsZero() {
				res.Completed = &Completed{m.windowStart, m.windowWs + drawn*(1-after), m.demandWs + demand*(1-after)}
			}

			m.meteredFrom = start
			m.windowWs, m.demandWs = drawn*after, demand*after
		} else {
			m.meteredFrom = now
			m.windowWs, m.demandWs = 0, 0
		}

		m.windowStart = start
		m.isFrozen = false
		m.stale = false
		m.unmovedWs, m.unmovedSince = 0, time.Time{}
	} else {
		m.windowWs += drawn
		m.demandWs += demand
	}

	m.lastSample = now
	m.source, m.lastEnergy = source, s.Energy
	if m.stale {
		source = SourcePower
	}

	if metered := now.Sub(m.meteredFrom).Seconds(); metered > 0 {
		m.avg = m.windowWs / metered
	}

	used := m.windowWs + set.Limit*m.meteredFrom.Sub(start).Seconds()
	elapsed := now.Sub(start)
	allowed := min(Allowed(set.Limit, used, elapsed), set.Limit*set.Cap)

	if elapsed >= set.Freeze {
		if !m.isFrozen {
			m.frozen, m.isFrozen = allowed, true
		}
		allowed = min(allowed, m.frozen)
	}

	res.Start, res.MeteredFrom, res.DrawnWs = start, m.meteredFrom, m.windowWs
	res.Avg, res.Allowed, res.Source, res.Stale = m.avg, allowed, source, m.stale

	return res
}
