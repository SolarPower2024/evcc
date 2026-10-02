package lm_test

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/lm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotFollowingIsNotCountedOn replays a battery that keeps grid-charging at
// 6250W although it was allowed 3000W, on an overloaded circuit. The heater
// above it first keeps its power, counting on the battery to give way, and is
// cut once the battery was ignored for the set cycles.
func TestNotFollowingIsNotCountedOn(t *testing.T) {
	m := lm.New()
	m.SetFollowCycles(func() int { return 3 })

	battery := &testLoad{title: "battery", prio: 1, power: 6250}
	heater := &testLoad{title: "heater", prio: 5, power: 3000}
	c := newCircuit(t, 9000, battery, heater) // 9250W on a 9000W circuit

	now := time.Now()
	m.Record(battery, 6250, m.ValidatePower(battery, c, 6250, 3000), true, now)
	m.Record(heater, 3000, 3000, true, now)

	heaterAllowed := func() float64 { return m.PeekPower(heater, c, 3000, 3000) }
	require.Equal(t, 3000.0, heaterAllowed(), "the battery below is expected to give way")

	assert.Empty(t, m.CheckFollowing())
	assert.Empty(t, m.CheckFollowing())
	assert.Equal(t, 3000.0, heaterAllowed(), "not yet, 2 of 3 cycles")

	changes := m.CheckFollowing()
	require.Len(t, changes, 1)
	assert.Equal(t, lm.FollowChange{Load: battery, Ignored: true, Power: 6250, Allowed: 3000}, changes[0])
	assert.Equal(t, 2750.0, heaterAllowed(), "cut to the free headroom, which sheds an on/off load")

	// once it draws what it was allowed, it is counted on again
	battery.power = 3000
	changes = m.CheckFollowing()
	require.Len(t, changes, 1)
	assert.False(t, changes[0].Ignored)
	assert.Equal(t, 3000.0, heaterAllowed())
}

// TestNotFollowingNeedsOverload verifies that a load above its limit only counts
// while its circuit is overloaded, and that 0 cycles switches the check off
func TestNotFollowingNeedsOverload(t *testing.T) {
	m := lm.New()
	m.SetFollowCycles(func() int { return 1 })

	battery := &testLoad{title: "battery", prio: 1, power: 6250}
	c := newCircuit(t, 20000, battery)

	m.Record(battery, 6250, m.ValidatePower(battery, c, 6250, 3000), true, time.Now())
	assert.Empty(t, m.CheckFollowing(), "the circuit copes")

	c = newCircuit(t, 5000, battery)
	m.Record(battery, 6250, m.ValidatePower(battery, c, 6250, 3000), true, time.Now())
	m.SetFollowCycles(func() int { return 0 })
	assert.Empty(t, m.CheckFollowing(), "switched off")

	// a few hundred watts above the limit are measurement, not disobedience
	m.SetFollowCycles(func() int { return 1 })
	battery.power = 3250
	c = newCircuit(t, 3000, battery) // overloaded by 250W
	m.ValidatePower(battery, c, 3250, 3000)
	m.Record(battery, 6250, 3000, true, time.Now())
	assert.Empty(t, m.CheckFollowing())
}
