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
	lm    *lm.Manager
	c     api.Circuit
	meter *gridMeter
	base  float64
	loads []*simLoad
	order []int // visiting order, one load per cycle
	next  int
}

func newSim(t *testing.T, base float64) *sim {
	t.Helper()

	m := &gridMeter{}
	c, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, m, 0)
	require.NoError(t, err)

	mk := func(title string, prio int, rated, min float64) *simLoad {
		return &simLoad{testLoad: testLoad{title: title, prio: prio, circuit: c}, rated: rated, min: min}
	}

	return &sim{
		t: t, lm: lm.New(), c: c, meter: m, base: base,
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
		allowed := s.lm.ValidatePower(l, s.c, l.power, l.rated)
		if allowed < l.min {
			allowed = 0
		}
		l.power = allowed
		return
	}

	// switch: its full power or nothing
	if allowed := s.lm.ValidatePower(l, s.c, l.power, l.rated); allowed < l.rated {
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

// level is a circuit below a parent. circuit has no exported way to set a
// parent, so a level checks its own limit and then the parent's, as upstream
// does.
type level struct {
	*circuit.Circuit
	parent api.Circuit
}

func (l *level) GetParent() api.Circuit { return l.parent }

func (l *level) ValidatePower(old, new float64) float64 {
	return l.parent.ValidatePower(old, l.Circuit.ValidatePower(old, new))
}

func (l *level) ValidateCurrent(old, new float64) float64 {
	return l.parent.ValidateCurrent(old, l.Circuit.ValidateCurrent(old, new))
}

// nested is a house circuit with the sibling circuits garage and heating
type nested struct {
	house           *circuit.Circuit
	garage, heating *level
}

// newNested returns a nested setup, a limit of 0 leaves a level unlimited
func newNested(t *testing.T, house, garage [2]float64) *nested {
	t.Helper()

	log := util.NewLogger("test")

	h, err := circuit.New(log, "house", house[0], house[1], nil, 0)
	require.NoError(t, err)

	mk := func(title string, limit [2]float64) *level {
		c, err := circuit.New(log, title, limit[0], limit[1], nil, 0)
		require.NoError(t, err)

		l := &level{Circuit: c, parent: h}
		h.RegisterChild(l)

		return l
	}

	return &nested{house: h, garage: mk("garage", garage), heating: mk("heating", [2]float64{})}
}

// load returns a load of the given priority on the circuit. A circuit sums up
// the loads whose circuit it is, which is the bare circuit of a level.
func (n *nested) load(title string, prio int, c api.Circuit, power, current float64) *testLoad {
	if l, ok := c.(*level); ok {
		c = l.Circuit
	}
	return &testLoad{title: title, prio: prio, power: power, current: current, circuit: c}
}

func (n *nested) update(t *testing.T, loads ...*testLoad) {
	t.Helper()

	cl := make([]api.CircuitLoad, 0, len(loads))
	for _, l := range loads {
		cl = append(cl, l)
	}
	require.NoError(t, n.house.Update(cl))
}

// TestOverloadNotRelievedBySibling: the garage is overloaded by a load that
// load management does not control. A lower priority load in the heating
// circuit next to it does not relieve the garage, so the wallbox is cut at once
// as upstream would, instead of keeping its power for good.
func TestOverloadNotRelievedBySibling(t *testing.T) {
	t.Run("power", func(t *testing.T) {
		n := newNested(t, [2]float64{0, 30000}, [2]float64{0, 11000})
		m := lm.New()

		wallbox := n.load("wallbox", 5, n.garage, 8000, 0)
		other := n.load("other", 0, n.garage, 6000, 0) // not managed
		heater := n.load("heater", 1, n.heating, 5000, 0)
		n.update(t, wallbox, other, heater)

		// 14 kW in a 11 kW garage, the heater is registered with its draw
		m.ValidatePower(heater, n.heating, 5000, 5000)

		assert.Equal(t, 5000.0, m.ValidatePower(wallbox, n.garage, 8000, 8000))
	})

	t.Run("current", func(t *testing.T) {
		n := newNested(t, [2]float64{35, 0}, [2]float64{16, 0})
		m := lm.New()

		wallbox := n.load("wallbox", 5, n.garage, 0, 13)
		other := n.load("other", 0, n.garage, 0, 6) // not managed
		heater := n.load("heater", 1, n.heating, 0, 13)
		n.update(t, wallbox, other, heater)

		// 19 A on 16 A in the garage
		m.ValidateCurrent(heater, n.heating, 13, 13)

		assert.Equal(t, 10.0, m.ValidateCurrent(wallbox, n.garage, 13, 13))
	})
}

// TestOverloadNotRelievedByParent: a lower priority load directly on the parent
// circuit does not relieve an overloaded child either
func TestOverloadNotRelievedByParent(t *testing.T) {
	n := newNested(t, [2]float64{35, 0}, [2]float64{16, 0})
	m := lm.New()

	wallbox := n.load("wallbox", 5, n.garage, 0, 13)
	other := n.load("other", 0, n.garage, 0, 6) // not managed
	heater := n.load("heater", 1, n.house, 0, 13)
	n.update(t, wallbox, other, heater)

	m.ValidateCurrent(heater, n.house, 13, 13)

	assert.Equal(t, 10.0, m.ValidateCurrent(wallbox, n.garage, 13, 13))
}

// TestOverloadRelievedBySameLevel: loads below that draw through the overloaded
// level cover the excess, so shedding still starts at the bottom
func TestOverloadRelievedBySameLevel(t *testing.T) {
	n := newNested(t, [2]float64{35, 0}, [2]float64{16, 0})
	m := lm.New()

	wallbox := n.load("wallbox", 5, n.garage, 0, 13)
	heater := n.load("heater", 1, n.garage, 0, 6)
	n.update(t, wallbox, heater)

	// 19 A on 16 A, the heater beside the wallbox draws 6 A and covers the 3 A
	m.ValidateCurrent(heater, n.garage, 6, 6)

	assert.Equal(t, 13.0, m.ValidateCurrent(wallbox, n.garage, 13, 13), "the wallbox keeps what it draws")
	assert.Equal(t, 3.0, m.ValidateCurrent(heater, n.garage, 6, 6), "the lowest load gives way")
}

// TestOverloadRelievedBySiblingOfParent: the overload sits one level up, on the
// house circuit. There a lower priority load in the neighbouring circuit does
// relieve it, as it draws through the house.
func TestOverloadRelievedBySiblingOfParent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		heaterPrio int
		want       float64
	}{
		{"lower priority covers the excess", 1, 13},
		{"higher priority does not", 9, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newNested(t, [2]float64{25, 0}, [2]float64{16, 0})
			m := lm.New()

			wallbox := n.load("wallbox", 5, n.garage, 0, 13)
			heater := n.load("heater", tc.heaterPrio, n.heating, 0, 13)
			n.update(t, wallbox, heater)

			// 26 A on 25 A at the house
			m.ValidateCurrent(heater, n.heating, 13, 13)

			assert.Equal(t, tc.want, m.ValidateCurrent(wallbox, n.garage, 13, 13))
		})
	}
}

// TestReserveNeedsReliefOnItsOwnLevel: the wallbox is held back by the garage
// limit. The heater in the neighbouring circuit cannot make room in the garage,
// so the wallbox's demand reserves nothing and the heater keeps its power.
func TestReserveNeedsReliefOnItsOwnLevel(t *testing.T) {
	n := newNested(t, [2]float64{0, 7000}, [2]float64{0, 5000})
	m := lm.New()

	wallbox := n.load("wallbox", 5, n.garage, 0, 0)
	other := n.load("other", 0, n.garage, 4000, 0) // not managed
	heater := n.load("heater", 1, n.heating, 2000, 0)
	n.update(t, wallbox, other, heater)

	// 4 kW in the garage leave 1 kW, the wallbox's demand of 3 kW is denied
	m.ValidatePower(heater, n.heating, 2000, 2000)
	assert.Equal(t, 1000.0, m.ValidatePower(wallbox, n.garage, 0, 3000))

	assert.Equal(t, 2000.0, m.ValidatePower(heater, n.heating, 2000, 2000), "shedding the heater would not help the wallbox")
}
