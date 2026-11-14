package lm

// Starting loads: what load management granted a load and it does not draw yet
// still counts in the circuit. A car takes about 20 s to draw after it was given
// its current, a heater's contactors follow the switch. Until then the circuit
// measures the old value, and the next load would be granted the same gap.

import (
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
)

const (
	// DefaultStartTime is how long a granted increase counts as not yet drawn.
	// Cars were measured to draw their full current after 30-60 s.
	DefaultStartTime = time.Minute

	// a remainder below this counts as drawn, a grant has to exceed the last one
	// by this to start the time anew
	startSlackCurrent = 1.0   // A
	startSlackPower   = 300.0 // W
)

// start is what a load was last granted and since when
type start struct {
	circuit      api.Circuit
	current      float64
	power        float64
	currentSince time.Time
	powerSince   time.Time
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

// granted reports whether a grant counts as given: a load that takes all or
// nothing, like a heater or the battery, got its request in full; a regulated
// load got what gets it going.
func (d Demand) granted(old, new, granted float64) bool {
	if d.Regulated {
		return granted >= old+max(d.Min, 0)
	}
	return granted >= new
}

// noteCurrent records the current a load was granted. Nothing granted ends the
// reserve; a grant above the last one by more than the slack starts the time
// anew, the same grant again every cycle does not extend it.
func (m *Manager) noteCurrent(l Load, c api.Circuit, granted float64) {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	if granted <= 0 {
		if s, ok := m.started[l]; ok {
			s.current = 0
			m.dropEmpty(l, s)
		}
		return
	}

	s := m.startOf(l)
	if granted > s.current+startSlackCurrent {
		s.currentSince = m.startNow()
	}
	s.current = granted
	s.circuit = c
}

// notePower is noteCurrent for power
func (m *Manager) notePower(l Load, c api.Circuit, granted float64) {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	if granted <= 0 {
		if s, ok := m.started[l]; ok {
			s.power = 0
			m.dropEmpty(l, s)
		}
		return
	}

	s := m.startOf(l)
	if granted > s.power+startSlackPower {
		s.powerSince = m.startNow()
	}
	s.power = granted
	s.circuit = c
}

// startOf is called with startMu held
func (m *Manager) startOf(l Load) *start {
	s, ok := m.started[l]
	if !ok {
		s = new(start)
		m.started[l] = s
	}
	return s
}

// dropEmpty is called with startMu held
func (m *Manager) dropEmpty(l Load, s *start) {
	if s.current <= 0 && s.power <= 0 {
		delete(m.started, l)
	}
}

// forgetStart drops what a load was granted
func (m *Manager) forgetStart(l Load) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	delete(m.started, l)
}

// StartReserve returns the power and current that other loads granted an
// increase and do not draw yet, on the circuits load l on circuit c shares a
// limit with. It only reads.
func (m *Manager) StartReserve(l Load, c api.Circuit) (power, current float64) {
	return m.startReserve(c, l)
}

// startReserve sums up, over all other loads that draw through a limit shared
// with c and were granted an increase less than DefaultStartTime ago, what they
// were granted less what they draw. A remainder below the slack counts as drawn.
// A grant that ran out stays recorded, so asking for the same again does not
// start the time anew; it ends with a grant of nothing or Forget.
func (m *Manager) startReserve(c api.Circuit, self Load) (power, current float64) {
	m.startMu.Lock()
	now := m.startNow()

	type pending struct {
		load         Load
		power, curr  float64
		powerOn, cOn bool
	}

	var list []pending
	for l, s := range m.started {
		if l == self || !competes(c, s.circuit) {
			continue
		}

		p := pending{load: l}
		if s.power > 0 && now.Sub(s.powerSince) < DefaultStartTime {
			p.power, p.powerOn = s.power, true
		}
		if s.current > 0 && now.Sub(s.currentSince) < DefaultStartTime {
			p.curr, p.cOn = s.current, true
		}
		if p.powerOn || p.cOn {
			list = append(list, p)
		}
	}
	m.startMu.Unlock()

	// what the loads draw is asked without holding the lock
	for _, p := range list {
		if p.powerOn {
			if rest := p.power - p.load.GetChargePower(); rest >= startSlackPower {
				power += rest
			}
		}
		if p.cOn {
			if rest := p.curr - p.load.GetMaxPhaseCurrent(); rest >= startSlackCurrent {
				current += rest
			}
		}
	}

	return power, current
}

// grantedAmount returns what counts as granted of a request: the grant if it
// is one, else nothing, as a load that did not get going draws nothing and
// reserves nothing
func (d Demand) grantedAmount(old, new, granted float64) float64 {
	if !d.granted(old, new, granted) {
		return 0
	}
	return granted
}
