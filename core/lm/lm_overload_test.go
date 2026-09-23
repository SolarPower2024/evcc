package lm_test

// Replays of an overload as seen in a user's log: three 3 kW switches and a car
// on a 10 kW circuit metered at the grid, with evcc updating one loadpoint per
// cycle. Priorities as in the log: lp-4 > lp-3 > lp-2 > lp-1.

import (
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gridMeter struct{ power float64 }

func (m *gridMeter) CurrentPower() (float64, error) { return m.power, nil }

// simLoad is a switch (all or nothing) or, with min > 0, a regulated car
type simLoad struct {
	testLoad
	rated float64 // switch: power when on
	min   float64 // car: minimum power, max is rated
}

type sim struct {
	t     *testing.T
	c     api.Circuit
	meter *gridMeter
	base  float64
	loads []*simLoad
	order []int // visiting order, one load per cycle
	next  int
}

func newSim(t *testing.T, base float64) *sim {
	t.Helper()
	lm.Reset()

	m := &gridMeter{}
	c, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, m, 0)
	require.NoError(t, err)

	mk := func(title string, prio int, rated, min float64) *simLoad {
		return &simLoad{testLoad: testLoad{title: title, prio: prio, circuit: c}, rated: rated, min: min}
	}

	return &sim{
		t: t, c: c, meter: m, base: base,
		loads: []*simLoad{
			mk("lp-1", 1, 3680, 1380), // car, lowest
			mk("lp-2", 2, 3000, 0),
			mk("lp-3", 3, 3000, 0),
			mk("lp-4", 4, 3000, 0), // highest
		},
		order: []int{1, 2, 3, 0}, // lp-2, lp-3, lp-4, lp-1 as in the log
	}
}

func (s *sim) grid() float64 {
	res := s.base
	for _, l := range s.loads {
		res += l.power
	}
	return res
}

// cycle measures the circuit and updates the next load in turn
func (s *sim) cycle() {
	s.meter.power = s.grid()

	loads := make([]api.CircuitLoad, 0, len(s.loads))
	for _, l := range s.loads {
		loads = append(loads, l)
	}
	require.NoError(s.t, s.c.Update(loads))

	l := s.loads[s.order[s.next%len(s.order)]]
	s.next++

	if l.min > 0 {
		// regulated: takes what it gets down to its minimum
		allowed := lm.ValidatePower(l, s.c, l.power, l.rated)
		if allowed < l.min {
			allowed = 0
		}
		l.power = allowed
		return
	}

	// switch: its full power or nothing
	if allowed := lm.ValidatePower(l, s.c, l.power, l.rated); allowed < l.rated {
		l.power = 0
	} else {
		l.power = l.rated
	}
}

func (s *sim) on(i int) bool { return s.loads[i].power > 0 }

// TestOverloadShedsLowestFirst: the house load rises by 3 kW, exactly one switch
// has to go. It must be lp-2, the lowest switch, although lp-3 and lp-4 are
// updated first.
func TestOverloadShedsLowestFirst(t *testing.T) {
	s := newSim(t, 100)
	s.loads[0].rated = 0 // car not charging in this one

	for range 12 {
		s.cycle()
	}
	require.True(t, s.on(1) && s.on(2) && s.on(3), "all switches on: %.0fW", s.grid())

	s.base = 4000 // 4000 + 9000 = 13 kW on a 10 kW circuit
	s.next = 1    // lp-3 is updated first after the jump, as in the log

	for i := range 8 {
		s.cycle()
		assert.True(t, s.on(3), "cycle %d: highest priority stays on", i)
		assert.True(t, s.on(2), "cycle %d: lp-3 stays on, lp-2 below it covers the excess", i)
	}

	assert.False(t, s.on(1), "lp-2 shed")
	assert.LessOrEqual(t, s.grid(), 10000.0)
}

// TestOverloadFromLog replays the log: all three switches on, then the house
// load jumps to 8410 W. Only 1590 W are left, so no 3 kW switch fits. The run
// has to end without overload, and the free 1590 W go to the car instead of
// being held for switches that cannot run anyway.
func TestOverloadFromLog(t *testing.T) {
	s := newSim(t, 100)
	s.loads[0].rated = 0 // car not yet charging

	for range 12 {
		s.cycle()
	}
	require.Equal(t, 9100.0, s.grid())

	s.base = 8410
	s.next = 1 // lp-3 is updated first after the jump, as in the log

	var overloaded int
	for range 24 {
		s.cycle()
		if s.grid() > 10000 {
			overloaded++
		}
	}

	assert.False(t, s.on(1) || s.on(2) || s.on(3), "no 3 kW switch fits into 1590 W")
	assert.LessOrEqual(t, s.grid(), 10000.0, "overload resolved")
	assert.LessOrEqual(t, overloaded, 4, "resolved within one round of updates")

	// the car asks for power: 1590 W are free and no switch could use them
	s.loads[0].rated = 3680
	for range 8 {
		s.cycle()
	}
	assert.Equal(t, 1590.0, s.loads[0].power, "car gets the idle headroom")
	assert.LessOrEqual(t, s.grid(), 10000.0)

	// the house load drops again: lp-4 has to get its 3 kW, the car makes room
	s.base = 7000
	for range 12 {
		s.cycle()
	}
	assert.True(t, s.on(3), "highest priority switch back on")
	assert.LessOrEqual(t, s.grid(), 10000.0)
}
