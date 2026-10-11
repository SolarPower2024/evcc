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
	GlitchNow   bool      // this sample's counter reading was not taken as it was, see Meter
	Completed   *Completed
}

// End returns the end of the window
func (s State) End() time.Time {
	return s.Start.Add(Window)
}

// Meter tracks the grid energy of the running window. The energy drawn comes
// from an import counter when the sample has one, else from the grid power.
// What it did not see, after a start or a gap, counts at the limit: assuming
// less could spend a budget that was already used. A counter reading lower than
// the last is taken for a glitch (a sensor reporting 0 for a moment) until the
// next reading confirms it, one further ahead than any grid connection draws is
// no measurement. The grid power counts meanwhile. Not safe for concurrent use.
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
	gapped       bool // a gap inside the window was counted at the limit

	// a reading below the last one is a reset, a replaced counter or a glitch (a
	// sensor reporting 0 for a moment). It only becomes the new base once the next
	// reading confirms it; back at the old level it never was one.
	pending       bool
	pendingEnergy float64 // the lower reading in kWh
	pendingWs     float64 // drawn according to the grid power while it waited
	glitch        bool    // the last sample's reading was not taken as it was
}

// rebase takes this reading as the base for the next step
func (m *Meter) rebase(source string, energy float64) {
	m.source, m.lastEnergy = source, energy
	m.pending, m.pendingWs = false, 0
}

// drawn returns the energy drawn since the last sample in Ws. A counter is used
// when it was read now and last time from the same source, the grid power fills
// in otherwise: for a gap longer than MaxGap the power after it says nothing
// about the gap, it counts at the limit. A reading the counter cannot have
// reached, or one lower than the last, does not count until it is confirmed.
func (m *Meter) drawn(now time.Time, imported, energy float64, source string, limit float64) float64 {
	interval := now.Sub(m.lastSample)
	powerWs := imported * interval.Seconds()
	m.glitch = false

	// what counts where the counter does not tell
	fallback := func() float64 {
		if interval > MaxGap {
			m.gapped = true
			return limit * interval.Seconds()
		}
		return powerWs
	}

	if source == SourcePower || source != m.source || m.stale {
		m.rebase(source, energy)
		return fallback()
	}

	// grid power counted for a lower reading that turned out to be a glitch
	var counted float64
	back := false

	if m.pending {
		switch {
		case energy < m.pendingEnergy:
			// still falling: keep waiting
			fb := fallback()
			m.pendingEnergy, m.pendingWs = energy, m.pendingWs+fb
			return fb

		case energy >= m.lastEnergy:
			// back at the old level: the counter covers what the grid power counted
			counted, back = m.pendingWs, true

		default:
			// the lower level holds: a reset or a replaced counter, continue from it
			m.lastEnergy = m.pendingEnergy
		}

		m.pending, m.pendingWs = false, 0
	}

	d := (energy - m.lastEnergy) * 3600e3

	if d < 0 {
		fb := fallback()
		m.pending, m.pendingEnergy, m.pendingWs = true, energy, fb
		m.glitch = true
		return fb
	}

	// no grid connection draws this much: not a measurement, the grid power
	// counts (also what the counter was late with) and the next step starts here
	if d > maxGridPower*max(interval, time.Second).Seconds() {
		drawn := fallback() + m.unmovedWs
		m.unmovedWs, m.unmovedSince = 0, time.Time{}
		m.rebase(source, energy)
		m.glitch = true
		return drawn
	}

	m.lastEnergy = energy

	if back {
		m.unmovedWs, m.unmovedSince = 0, time.Time{}
		return max(0, d-counted)
	}

	if d > 0 {
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
// with the battery power added back for the demand without it (as far as it
// went to the house, not to the grid).
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
		drawn = m.drawn(now, imported, s.Energy, source, set.Limit)
		// what the battery discharged into the grid was not the house's demand. With
		// a counter this is an approximation: the import counter knows no export.
		demand = max(0, drawn+(s.BatteryPower+min(0, s.GridPower))*now.Sub(m.lastSample).Seconds())
		res.StaleNow = m.stale && !wasStale
		res.GlitchNow = m.glitch
	} else {
		m.rebase(source, s.Energy)
	}

	if !m.windowStart.Equal(start) {
		// a sample from the previous window carries over: the part of the
		// interval since the boundary is metered, the rest was the last window's
		if !m.lastSample.IsZero() && m.lastSample.Before(start) && now.Sub(m.lastSample) <= MaxGap {
			after := now.Sub(start).Seconds() / now.Sub(m.lastSample).Seconds()

			// only a window metered from its start, without a gap, counts for the statistics
			if m.meteredFrom.Equal(m.windowStart) && !m.windowStart.IsZero() && !m.gapped {
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
		m.stale, m.gapped = false, false
		m.unmovedWs, m.unmovedSince = 0, time.Time{}
	} else {
		m.windowWs += drawn
		m.demandWs += demand
	}

	m.lastSample = now
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
