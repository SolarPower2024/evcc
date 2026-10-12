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
	m := lm.New()

	wallbox := &testLoad{title: "wallbox", prio: 0, power: 11000}
	c := newCircuit(t, 11000, wallbox)

	heater := &testLoad{title: "heater", prio: 0, circuit: c}

	// circuit is full, the heater gets nothing
	assert.Equal(t, 0.0, m.ValidatePower(heater, c, 0, 2000))

	// ... and the wallbox keeps everything, as nothing outranks it
	assert.Equal(t, 11000.0, m.ValidatePower(wallbox, c, 11000, 11000))
}

// TestHigherPriorityShedsLower verifies that a denied high-priority load pushes
// a lower-priority load down by exactly its unserved demand
func TestHigherPriorityShedsLower(t *testing.T) {
	m := lm.New()

	// lower priority is shed first
	wallbox := &testLoad{title: "wallbox", prio: 1, power: 11000}
	c := newCircuit(t, 11000, wallbox)

	heatpump := &testLoad{title: "heatpump", prio: 5, circuit: c}

	// the full circuit denies the heat pump, which records 2000W of demand
	assert.Equal(t, 0.0, m.ValidatePower(heatpump, c, 0, 2000))

	// the wallbox now has to give up those 2000W
	assert.Equal(t, 9000.0, m.ValidatePower(wallbox, c, 11000, 11000))

	// a probe sees the same reserve but records nothing
	assert.Equal(t, 9000.0, m.PeekPower(wallbox, c, 11000, 11000))
}

// TestSatisfiedDemandReleasesReserve verifies that the reserve disappears once
// the high-priority load gets what it asked for
func TestSatisfiedDemandReleasesReserve(t *testing.T) {
	m := lm.New()

	wallbox := &testLoad{title: "wallbox", prio: 1, power: 9000}
	heatpump := &testLoad{title: "heatpump", prio: 5, power: 2000}
	c := newCircuit(t, 11000, wallbox, heatpump)

	// the heat pump's 2000W now fit, so nothing is denied
	assert.Equal(t, 2000.0, m.ValidatePower(heatpump, c, 2000, 2000))

	// ... and the wallbox may use the remaining budget again
	assert.Equal(t, 9000.0, m.ValidatePower(wallbox, c, 9000, 11000))
}

// TestLowerPriorityIsNotProtected verifies that the reserve only works upwards
func TestLowerPriorityIsNotProtected(t *testing.T) {
	m := lm.New()

	heatpump := &testLoad{title: "heatpump", prio: 5, power: 11000}
	c := newCircuit(t, 11000, heatpump)

	wallbox := &testLoad{title: "wallbox", prio: 1, circuit: c}

	// the wallbox is denied and records demand ...
	assert.Equal(t, 0.0, m.ValidatePower(wallbox, c, 0, 2000))

	// ... but the higher-priority heat pump does not give way for it
	assert.Equal(t, 11000.0, m.ValidatePower(heatpump, c, 11000, 11000))
}

// TestRecoveryFavoursHigherPriority walks through the cycles of a shed and the
// subsequent recovery. evcc updates one loadpoint per cycle, so each step here
// is one cycle: a load is validated, draws what it was granted, and the circuit
// is re-measured.
func TestRecoveryFavoursHigherPriority(t *testing.T) {
	m := lm.New()

	// the wallbox grabbed the whole budget before the heat pump asked for anything
	wallbox := &testLoad{title: "wallbox", prio: 1, power: 11000}
	heatpump := &testLoad{title: "heatpump", prio: 5, power: 0}
	c := newCircuit(t, 11000, wallbox, heatpump)

	// cycle: validate the load, let it draw the result, re-measure the circuit
	run := func(l *testLoad, want float64) float64 {
		t.Helper()
		got := m.ValidatePower(l, c, l.power, want)
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
	m := lm.New()

	c, err := circuit.New(util.NewLogger("test"), "test", 16, 0, nil, 0)
	require.NoError(t, err)

	wallbox := &testLoad{title: "wallbox", prio: 1, current: 16, circuit: c}
	require.NoError(t, c.Update([]api.CircuitLoad{wallbox}))

	heater := &testLoad{title: "heater", prio: 5, circuit: c}

	// the current limit denies the heater 6A
	assert.Equal(t, 0.0, m.ValidateCurrent(heater, c, 0, 6))

	// the wallbox gives up those 6A ...
	assert.Equal(t, 10.0, m.ValidateCurrent(wallbox, c, 16, 16))

	// ... while the unconfigured power limit stays unreserved
	assert.Equal(t, 11000.0, m.ValidatePower(wallbox, c, 11000, 11000))
}

// TestPriorityLookupOverrides verifies that a priority set via the lookup wins
// over the load's own, and that loads the lookup does not know keep theirs
func TestPriorityLookupOverrides(t *testing.T) {
	m := lm.New()

	wallbox := &testLoad{title: "wallbox", prio: 5}
	heater := &testLoad{title: "heater", prio: 3}

	m.SetPriorityLookup(func(l lm.Load) (int, bool) {
		if l == lm.Load(wallbox) {
			return 1, true
		}
		return 0, false
	})

	assert.Equal(t, 1, m.Priority(wallbox))
	assert.Equal(t, 3, m.Priority(heater))

	// the overridden priority is what shedding acts on: the heater now outranks
	// the wallbox, although by their own priorities it would be the other way round
	wallbox.power = 11000
	c := newCircuit(t, 11000, wallbox)
	heater.circuit = c

	assert.Equal(t, 0.0, m.ValidatePower(heater, c, 0, 2000))
	assert.Equal(t, 9000.0, m.ValidatePower(wallbox, c, 11000, 11000))
}

// TestForgetReleasesReservation verifies that a load which stopped asking for
// power no longer throttles loads below it
func TestForgetReleasesReservation(t *testing.T) {
	m := lm.New()

	wallbox := &testLoad{title: "wallbox", prio: 1, power: 11000}
	c := newCircuit(t, 11000, wallbox)

	battery := &testLoad{title: "battery", prio: 5, circuit: c}

	// the denied battery reserves 5000W, the wallbox has to give way
	assert.Equal(t, 0.0, m.ValidatePower(battery, c, 0, 5000))
	assert.Equal(t, 6000.0, m.ValidatePower(wallbox, c, 11000, 11000))

	// once the battery no longer wants to charge, the wallbox gets it all back
	m.Forget(battery)
	assert.Equal(t, 11000.0, m.ValidatePower(wallbox, c, 11000, 11000))
}

// TestOnOffLoadGetsItsWholeNeed is the regression test for a higher-priority
// load that can only switch on in full, such as a heater or the battery. With
// 2000W free it takes nothing of a 3000W need, so the lower-priority wallbox has
// to leave the whole 3000W free, not just the 1000W that were missing.
func TestOnOffLoadGetsItsWholeNeed(t *testing.T) {
	m := lm.New()

	wallbox := &testLoad{title: "wallbox", prio: 1, power: 9000}
	c := newCircuit(t, 11000, wallbox)

	heater := &testLoad{title: "heater", prio: 5, circuit: c}

	// all or nothing: capped below its need, the heater stays off
	assert.Less(t, m.ValidatePower(heater, c, 0, 3000), 3000.0)

	// the wallbox makes room for the full 3000W
	allowed := m.ValidatePower(wallbox, c, 9000, 9000)
	assert.Equal(t, 8000.0, allowed)

	// next cycle the heater fits
	wallbox.power = allowed
	require.NoError(t, c.Update([]api.CircuitLoad{wallbox, heater}))
	assert.Equal(t, 3000.0, m.ValidatePower(heater, c, 0, 3000))
}

// countingCircuit counts how often the circuit is asked
type countingCircuit struct {
	api.Circuit
	power, current int
}

func (c *countingCircuit) ValidatePower(old, new float64) float64 {
	c.power++
	return c.Circuit.ValidatePower(old, new)
}

func (c *countingCircuit) ValidateCurrent(old, new float64) float64 {
	c.current++
	return c.Circuit.ValidateCurrent(old, new)
}

// TestValidateAsksCircuitOnce: without a reserve the circuit is asked once per
// request, not again for the same answer (each answer is a log line upstream)
func TestValidateAsksCircuitOnce(t *testing.T) {
	m := lm.New()

	c := &countingCircuit{Circuit: newCircuit(t, 11000)}
	load := &testLoad{title: "wallbox", circuit: c}

	assert.Equal(t, 2000.0, m.ValidatePower(load, c, 0, 2000))
	assert.Equal(t, 1, c.power)

	assert.Equal(t, 8.0, m.ValidateCurrent(load, c, 0, 8))
	assert.Equal(t, 1, c.current)
}

// TestStandingDemand: a load that is on but draws nothing stands for a fixed
// demand, whatever the circuit denied of its request
func TestStandingDemand(t *testing.T) {
	m := lm.New()

	low := &testLoad{title: "low", prio: 1, power: 10500}
	c := newCircuit(t, 11000, low)
	high := &testLoad{title: "high", prio: 5, circuit: c}

	// 500W are free, of 2000W asked: the standing 1000W count, not the denied 1500W
	assert.Equal(t, 500.0, m.RequestPower(high, c, 0, 2000, lm.Demand{Standing: 1000}))
	assert.Equal(t, 10000.0, m.ValidatePower(low, c, 10500, 10500))

	// the low one gave way, 1000W are free and the request for them fits: the
	// standing demand stays
	low.power = 10000
	require.NoError(t, c.Update([]api.CircuitLoad{low, high}))
	assert.Equal(t, 1000.0, m.RequestPower(high, c, 0, 1000, lm.Demand{Standing: 1000}))
	assert.Equal(t, 10000.0, m.ValidatePower(low, c, 10000, 10500), "the low one is not allowed back to 10500")

	// the plain request that fits demands nothing, the low one takes the power
	assert.Equal(t, 1000.0, m.ValidatePower(high, c, 0, 1000))
	assert.Equal(t, 10500.0, m.ValidatePower(low, c, 10000, 10500))
}

// TestRegulatedLoadGetsItsMinimum: a wallbox of high priority that cannot get its
// whole demand freed is not left with nothing. The circuit has 11kW, 2kW of
// them are base load, the low priority load draws 9kW and the wallbox asks for
// 11.04kW with a minimum of 4.14kW. The low load gives way and the wallbox gets
// what is left, which is above its minimum.
func TestRegulatedLoadGetsItsMinimum(t *testing.T) {
	var base *testLoad

	setup := func() (*lm.Manager, api.Circuit, *testLoad, *testLoad) {
		low := &testLoad{title: "low", prio: 1, power: 9000}
		base = &testLoad{title: "base", prio: 0, power: 2000}
		c := newCircuit(t, 11000, low, base)

		return lm.New(), c, low, &testLoad{title: "high", prio: 5, circuit: c}
	}

	t.Run("regulated", func(t *testing.T) {
		m, c, low, high := setup()
		d := lm.Demand{Regulated: true, Min: 4140}

		assert.Equal(t, 0.0, m.RequestPower(high, c, 0, 11040, d), "nothing free yet")

		// the low load leaves what it can ...
		assert.Equal(t, 0.0, m.ValidatePower(low, c, 9000, 9000))
		low.power = 0
		require.NoError(t, c.Update([]api.CircuitLoad{low, high, base}))

		// ... so that the wallbox starts
		assert.Equal(t, 9000.0, m.RequestPower(high, c, 0, 11040, d))

		// it keeps going with those 9kW, and the low load does not take them back
		high.power = 9000
		require.NoError(t, c.Update([]api.CircuitLoad{low, high, base}))
		assert.Equal(t, 9000.0, m.RequestPower(high, c, 9000, 11040, lm.Demand{Regulated: true}))
		assert.Equal(t, 0.0, m.ValidatePower(low, c, 0, 9000))
	})

	t.Run("all or nothing", func(t *testing.T) {
		m, c, low, high := setup()

		// the whole 11.04kW cannot be freed, so the low load keeps what it has
		assert.Equal(t, 0.0, m.ValidatePower(high, c, 0, 11040))
		assert.Equal(t, 9000.0, m.ValidatePower(low, c, 9000, 9000))
	})

	t.Run("not even the minimum", func(t *testing.T) {
		m, c, low, high := setup()

		// 2kW are free and the low load draws 1kW: shedding it does not reach the
		// minimum of 4.14kW, so it is not shed for nothing
		low.power = 1000
		other := &testLoad{title: "other", power: 8000, circuit: c}
		require.NoError(t, c.Update([]api.CircuitLoad{low, high, other}))

		assert.Equal(t, 2000.0, m.RequestPower(high, c, 0, 11040, lm.Demand{Regulated: true, Min: 4140}))
		assert.Equal(t, 1000.0, m.ValidatePower(low, c, 1000, 1000))
	})
}
