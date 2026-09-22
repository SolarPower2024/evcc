package lm_test

import (
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLoad is a circuit load with a fixed power and priority
type testLoad struct {
	title   string
	prio    int
	power   float64
	current float64
	circuit api.Circuit
}

func (l *testLoad) GetTitle() string            { return l.title }
func (l *testLoad) LmPriority() int             { return l.prio }
func (l *testLoad) GetCircuit() api.Circuit     { return l.circuit }
func (l *testLoad) GetChargePower() float64     { return l.power }
func (l *testLoad) GetMaxPhaseCurrent() float64 { return l.current }

var (
	_ api.CircuitLoad = (*testLoad)(nil)
	_ lm.Load         = (*testLoad)(nil)
)

// newCircuit returns a meterless circuit loaded to the given power
func newCircuit(t *testing.T, maxPower float64, loads ...api.CircuitLoad) api.Circuit {
	t.Helper()

	c, err := circuit.New(util.NewLogger("test"), "test", 0, maxPower, nil, 0)
	require.NoError(t, err)

	for _, l := range loads {
		l.(*testLoad).circuit = c
	}
	require.NoError(t, c.Update(loads))

	return c
}

// TestEqualPrioritiesAreUpstream verifies that loads on the same priority see
// plain circuit behaviour, i.e. nothing is reserved
func TestEqualPrioritiesAreUpstream(t *testing.T) {
	lm.Reset()

	wallbox := &testLoad{title: "wallbox", prio: 0, power: 11000}
	c := newCircuit(t, 11000, wallbox)

	heater := &testLoad{title: "heater", prio: 0, circuit: c}

	// circuit is full, the heater gets nothing
	assert.Equal(t, 0.0, lm.ValidatePower(heater, c, 0, 2000))

	// ... and the wallbox keeps everything, as nothing outranks it
	assert.Equal(t, 11000.0, lm.ValidatePower(wallbox, c, 11000, 11000))
}

// TestHigherPriorityShedsLower verifies that a denied high-priority load pushes
// a lower-priority load down by exactly its unserved demand
func TestHigherPriorityShedsLower(t *testing.T) {
	lm.Reset()

	// lower lmpriority is shed first
	wallbox := &testLoad{title: "wallbox", prio: 1, power: 11000}
	c := newCircuit(t, 11000, wallbox)

	heatpump := &testLoad{title: "heatpump", prio: 5, circuit: c}

	// the full circuit denies the heat pump, which records 2000W of demand
	assert.Equal(t, 0.0, lm.ValidatePower(heatpump, c, 0, 2000))

	// the wallbox now has to give up those 2000W
	assert.Equal(t, 9000.0, lm.ValidatePower(wallbox, c, 11000, 11000))

	// a probe sees the same reserve but records nothing
	assert.Equal(t, 9000.0, lm.PeekPower(wallbox, c, 11000, 11000))
}

// TestSatisfiedDemandReleasesReserve verifies that the reserve disappears once
// the high-priority load gets what it asked for
func TestSatisfiedDemandReleasesReserve(t *testing.T) {
	lm.Reset()

	wallbox := &testLoad{title: "wallbox", prio: 1, power: 9000}
	heatpump := &testLoad{title: "heatpump", prio: 5, power: 2000}
	c := newCircuit(t, 11000, wallbox, heatpump)

	// the heat pump's 2000W now fit, so nothing is denied
	assert.Equal(t, 2000.0, lm.ValidatePower(heatpump, c, 2000, 2000))

	// ... and the wallbox may use the remaining budget again
	assert.Equal(t, 9000.0, lm.ValidatePower(wallbox, c, 9000, 11000))
}

// TestLowerPriorityIsNotProtected verifies that the reserve only works upwards
func TestLowerPriorityIsNotProtected(t *testing.T) {
	lm.Reset()

	heatpump := &testLoad{title: "heatpump", prio: 5, power: 11000}
	c := newCircuit(t, 11000, heatpump)

	wallbox := &testLoad{title: "wallbox", prio: 1, circuit: c}

	// the wallbox is denied and records demand ...
	assert.Equal(t, 0.0, lm.ValidatePower(wallbox, c, 0, 2000))

	// ... but the higher-priority heat pump does not give way for it
	assert.Equal(t, 11000.0, lm.ValidatePower(heatpump, c, 11000, 11000))
}

// TestRecoveryFavoursHigherPriority walks through the cycles of a shed and the
// subsequent recovery. evcc updates one loadpoint per cycle, so each step here
// is one cycle: a load is validated, draws what it was granted, and the circuit
// is re-measured.
func TestRecoveryFavoursHigherPriority(t *testing.T) {
	lm.Reset()

	// the wallbox grabbed the whole budget before the heat pump asked for anything
	wallbox := &testLoad{title: "wallbox", prio: 1, power: 11000}
	heatpump := &testLoad{title: "heatpump", prio: 5, power: 0}
	c := newCircuit(t, 11000, wallbox, heatpump)

	// cycle: validate the load, let it draw the result, re-measure the circuit
	run := func(l *testLoad, want float64) float64 {
		t.Helper()
		got := lm.ValidatePower(l, c, l.power, want)
		l.power = got
		require.NoError(t, c.Update([]api.CircuitLoad{wallbox, heatpump}))
		return got
	}

	// the circuit is full, so the heat pump is denied and records its demand
	assert.Equal(t, 0.0, run(heatpump, 4000))

	// the wallbox has to give up exactly that demand, even though it is not
	// asking for less itself
	assert.Equal(t, 7000.0, run(wallbox, 11000))

	// the freed power goes to the heat pump, not back to the wallbox
	assert.Equal(t, 4000.0, run(heatpump, 4000))

	// the allocation is stable: the wallbox no longer loses ground
	assert.Equal(t, 7000.0, run(wallbox, 11000))
	assert.Equal(t, 4000.0, run(heatpump, 4000))

	// the heat pump switches off and stops demanding ...
	assert.Equal(t, 0.0, run(heatpump, 0))

	// ... so the wallbox takes the full budget back
	assert.Equal(t, 11000.0, run(wallbox, 11000))
}

// TestCurrentReserveIsSeparate verifies that current and power demand are
// tracked independently, as a circuit may limit either or both
func TestCurrentReserveIsSeparate(t *testing.T) {
	lm.Reset()

	c, err := circuit.New(util.NewLogger("test"), "test", 16, 0, nil, 0)
	require.NoError(t, err)

	wallbox := &testLoad{title: "wallbox", prio: 1, current: 16, circuit: c}
	require.NoError(t, c.Update([]api.CircuitLoad{wallbox}))

	heater := &testLoad{title: "heater", prio: 5, circuit: c}

	// the current limit denies the heater 6A
	assert.Equal(t, 0.0, lm.ValidateCurrent(heater, c, 0, 6))

	// the wallbox gives up those 6A ...
	assert.Equal(t, 10.0, lm.ValidateCurrent(wallbox, c, 16, 16))

	// ... while the unconfigured power limit stays unreserved
	assert.Equal(t, 11000.0, lm.ValidatePower(wallbox, c, 11000, 11000))
}

// TestPriorityLookupOverrides verifies that a priority set via the lookup wins
// over the load's own, and that loads the lookup does not know keep theirs
func TestPriorityLookupOverrides(t *testing.T) {
	lm.Reset()
	defer lm.Reset()

	wallbox := &testLoad{title: "wallbox", prio: 5}
	heater := &testLoad{title: "heater", prio: 3}

	lm.SetPriorityLookup(func(l lm.Load) (int, bool) {
		if l == lm.Load(wallbox) {
			return 1, true
		}
		return 0, false
	})

	assert.Equal(t, 1, lm.Priority(wallbox))
	assert.Equal(t, 3, lm.Priority(heater))

	// the overridden priority is what shedding acts on: the heater now outranks
	// the wallbox, although by their own priorities it would be the other way round
	wallbox.power = 11000
	c := newCircuit(t, 11000, wallbox)
	heater.circuit = c

	assert.Equal(t, 0.0, lm.ValidatePower(heater, c, 0, 2000))
	assert.Equal(t, 9000.0, lm.ValidatePower(wallbox, c, 11000, 11000))
}

// TestForgetReleasesReservation verifies that a load which stopped asking for
// power no longer throttles loads below it
func TestForgetReleasesReservation(t *testing.T) {
	lm.Reset()

	wallbox := &testLoad{title: "wallbox", prio: 1, power: 11000}
	c := newCircuit(t, 11000, wallbox)

	battery := &testLoad{title: "battery", prio: 5, circuit: c}

	// the denied battery reserves 5000W, the wallbox has to give way
	assert.Equal(t, 0.0, lm.ValidatePower(battery, c, 0, 5000))
	assert.Equal(t, 6000.0, lm.ValidatePower(wallbox, c, 11000, 11000))

	// once the battery no longer wants to charge, the wallbox gets it all back
	lm.Forget(battery)
	assert.Equal(t, 11000.0, lm.ValidatePower(wallbox, c, 11000, 11000))
}

// TestOnOffLoadGetsItsWholeNeed is the regression test for a higher-priority
// load that can only switch on in full, such as a heater or the battery. With
// 2000W free it takes nothing of a 3000W need, so the lower-priority wallbox has
// to leave the whole 3000W free, not just the 1000W that were missing.
func TestOnOffLoadGetsItsWholeNeed(t *testing.T) {
	lm.Reset()

	wallbox := &testLoad{title: "wallbox", prio: 1, power: 9000}
	c := newCircuit(t, 11000, wallbox)

	heater := &testLoad{title: "heater", prio: 5, circuit: c}

	// all or nothing: capped below its need, the heater stays off
	assert.Less(t, lm.ValidatePower(heater, c, 0, 3000), 3000.0)

	// the wallbox makes room for the full 3000W
	allowed := lm.ValidatePower(wallbox, c, 9000, 9000)
	assert.Equal(t, 8000.0, allowed)

	// next cycle the heater fits
	wallbox.power = allowed
	require.NoError(t, c.Update([]api.CircuitLoad{wallbox, heater}))
	assert.Equal(t, 3000.0, lm.ValidatePower(heater, c, 0, 3000))
}
