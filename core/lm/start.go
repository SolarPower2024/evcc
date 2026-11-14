package lm

// Starting loads: what a load was granted and does not draw yet still counts in
// the circuit. A car takes up to a minute to draw its full current after it was
// given it. Until then the circuit measures the old value, and the next load
// would be granted the same gap.
//
// A grant is noted by the load once its request is decided (NoteStart), not by
// the requests themselves: a loadpoint's current and power checks are only
// combined afterwards, and a load that ends up off reserves nothing.

import (
	"math"
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
)

const (
	// DefaultStartTime is how long a granted increase counts as not yet drawn.
	// Cars were measured to draw their full current after 30-60 s.
	DefaultStartTime = time.Minute

	// a remainder below this counts as drawn, a grant has to exceed the highest
	// one since the load went on by this to start the time anew
	startSlackCurrent = 1.0   // A
	startSlackPower   = 300.0 // W
)

// start is what a load was last granted, the highest grant since it went on and
// since when the time runs
type start struct {
	circuit                  api.Circuit
	current, power           float64
	peakCurrent, peakPower   float64
	currentSince, powerSince time.Time
}

// starts is the Manager's state of the loads that were granted an increase
type starts struct {
	startMu sync.Mutex
	started map[Load]*start
	clock   func() time.Time
}

// SetClock sets the time source of the start reserve, for tests
func (m *Manager) SetClock(f func() time.Time) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if f == nil {
		f = time.Now
	}
	m.clock = f
}

// startNow is called with startMu held
func (m *Manager) startNow() time.Time {
	if m.clock == nil {
		return time.Now()
	}
	return m.clock()
}

// NoteStart records what load l on circuit c was finally granted: its current
// and its power, 0 for off. Off ends the reserve. A grant above the highest one
// since the load went on by more than the slack starts the time anew; the same
// grant again, or one that goes down and back up, does not.
func (m *Manager) NoteStart(l Load, c api.Circuit, current, power float64) {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	if c == nil || (current <= 0 && power <= 0) {
		delete(m.started, l)
		return
	}

	s, ok := m.started[l]
	if !ok {
		s = new(start)
		m.started[l] = s
	}
	now := m.startNow()

	s.circuit = c
	s.current, s.power = max(current, 0), max(power, 0)

	if s.current > s.peakCurrent+startSlackCurrent {
		s.currentSince = now
	}
	if s.power > s.peakPower+startSlackPower {
		s.powerSince = now
	}
	s.peakCurrent = max(s.peakCurrent, s.current)
	s.peakPower = max(s.peakPower, s.power)
}

// forgetStart drops what a load was granted
func (m *Manager) forgetStart(l Load) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	delete(m.started, l)
}

// pendingStart is a load whose grant is still running, copied out under the lock
type pendingStart struct {
	load               Load
	circuit            api.Circuit
	current, power     float64
	currentOn, powerOn bool
}

// pendingStarts returns the other loads on a limit shared with c whose grant is
// still running. A grant that ran out stays recorded, so the same grant again
// does not start the time anew; it ends with off or Forget.
func (m *Manager) pendingStarts(c api.Circuit, self Load) []pendingStart {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	now := m.startNow()
	var res []pendingStart

	for l, s := range m.started {
		if l == self || !competes(c, s.circuit) {
			continue
		}

		p := pendingStart{load: l, circuit: s.circuit, current: s.current, power: s.power}
		p.currentOn = s.current > 0 && now.Sub(s.currentSince) < DefaultStartTime
		p.powerOn = s.power > 0 && now.Sub(s.powerSince) < DefaultStartTime

		if p.currentOn || p.powerOn {
			res = append(res, p)
		}
	}

	return res
}

// drawn returns what a starting load draws as a level of the circuit sees it. A
// level with a meter measures what flows: a load drawing hardly any power draws
// no current, whatever it reports (a charger without phase currents reports its
// offer). A level without a meter adds up what the loads report, so the offer
// counts there already.
func (p pendingStart) drawn(level api.Circuit) (power, current float64) {
	power = p.load.GetChargePower()
	if level.HasMeter() && power < startSlackPower {
		return power, 0
	}
	return power, p.load.GetMaxPhaseCurrent()
}

// rest returns what of the grant is not drawn yet on the level, 0 below the slack
func (p pendingStart) rest(level api.Circuit) (power, current float64) {
	drawnPower, drawnCurrent := p.drawn(level)

	if p.powerOn {
		if r := p.power - drawnPower; r >= startSlackPower {
			power = r
		}
	}
	if p.currentOn {
		if r := p.current - drawnCurrent; r >= startSlackCurrent {
			current = r
		}
	}

	return power, current
}

// levelReserve sums up the rest of the starting loads drawing through the level
func levelReserve(list []pendingStart, level api.Circuit) (power, current float64) {
	for _, p := range list {
		if !under(p.circuit, level) {
			continue
		}
		pw, cur := p.rest(level)
		power += pw
		current += cur
	}
	return power, current
}

// StartReserve returns the power and current that other loads were granted and
// do not draw yet, on the levels load l on circuit c shares with them; the
// tightest level of the chain counts most, here the sum over the chain's
// competitors is given, for the log. It only reads.
func (m *Manager) StartReserve(l Load, c api.Circuit) (power, current float64) {
	if c == nil {
		return 0, 0
	}

	list := m.pendingStarts(c, l)
	for _, p := range list {
		// the loads on c's chain, as the closest shared level sees them
		for level := c; level != nil; level = level.GetParent() {
			if under(p.circuit, level) {
				pw, cur := p.rest(level)
				power += pw
				current += cur
				break
			}
		}
	}

	return power, current
}

// startCap returns the highest value a request may get with the starting loads'
// rest counted on every level of c's chain they draw through; +Inf without a
// limiting level. Read straight from the levels, so nothing is logged.
func (m *Manager) startCap(c api.Circuit, self Load, old float64, isPower bool) float64 {
	list := m.pendingStarts(c, self)
	if len(list) == 0 {
		return math.Inf(1)
	}

	res := math.Inf(1)
	for level := c; level != nil; level = level.GetParent() {
		reservePower, reserveCurrent := levelReserve(list, level)

		if isPower {
			if limit := level.EffectiveMaxPower(); limit > 0 && reservePower > 0 {
				res = min(res, old+limit-level.GetChargePower()-reservePower)
			}
		} else {
			if limit := level.GetMaxCurrent(); limit > 0 && reserveCurrent > 0 {
				res = min(res, old+limit-level.GetMaxPhaseCurrent()-reserveCurrent)
			}
		}
	}

	return res
}

// applyStartCap cuts a result to the start cap, never below what the load draws
// now
func (m *Manager) applyStartCap(c api.Circuit, self Load, old, new, res float64, isPower bool) float64 {
	limit := m.startCap(c, self, old, isPower)
	if math.IsInf(limit, 1) {
		return res
	}
	return min(res, max(min(old, new), limit))
}
