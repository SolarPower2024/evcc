package core

import (
	"errors"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPeakSetpoint covers the sign handling and the clamp
func TestPeakSetpoint(t *testing.T) {
	const limit = 5000.0

	tc := []struct {
		name                  string
		gridPower, batteryPwr float64
		want                  float64
	}{
		{"below the limit", 3000, 0, 0},
		{"exactly at the limit", 5000, 0, 0},
		{"above the limit, battery idle", 8000, 0, 3000},
		{"exporting to grid", -2000, 0, 0},
		// battery discharging: the grid value is already reduced by it
		{"battery already covering the excess", 5000, 3000, 3000},
		{"battery covering part of it", 6000, 1000, 2000},
		{"battery covering more than needed", 4000, 3000, 2000},
		// battery charging counts against the demand, not for it
		{"charging from pv", 2000, -3000, 0},
		{"charging pushes grid over the limit", 8000, -3000, 0},
		// fractions are dropped, some number entities reject them
		{"rounded to whole watts", 6234.6, 0, 1235},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, peakSetpoint(tc.gridPower, tc.batteryPwr, limit))
		})
	}
}

// TestPeakSetpointIsStable is the regression test for the feedback loop: once
// the battery covers the excess, the setpoint must stay put instead of
// collapsing to zero and letting the peak return.
func TestPeakSetpointIsStable(t *testing.T) {
	const (
		limit  = 5000.0
		demand = 8000.0 // what the house draws, independent of the battery
	)

	// start with the battery idle
	setpoint := peakSetpoint(demand, 0, limit)
	assert.Equal(t, 3000.0, setpoint)

	// the controller applies it, so the grid drops by that amount. Feeding the
	// new grid value back must yield the same setpoint, not zero.
	for range 5 {
		grid := demand - setpoint
		setpoint = peakSetpoint(grid, setpoint, limit)
		assert.Equal(t, 3000.0, setpoint)
	}
}

// TestPeakSetpointFollowsDemand verifies the setpoint tracks a changing load
// while the battery is already discharging
func TestPeakSetpointFollowsDemand(t *testing.T) {
	const limit = 5000.0

	// battery discharging 3000 W, house load rises from 8000 to 10000 W
	setpoint := peakSetpoint(10000-3000, 3000, limit)
	assert.Equal(t, 5000.0, setpoint)

	// ... and drops to 6000 W
	setpoint = peakSetpoint(6000-5000, 5000, limit)
	assert.Equal(t, 1000.0, setpoint)

	// ... and below the limit, the battery is released
	setpoint = peakSetpoint(4000-1000, 1000, limit)
	assert.Equal(t, 0.0, setpoint)
}

// TestPeakPausesGridCharge verifies that grid charging gives way to a running
// demand peak, stays off for the hold-off, and is never blocked by the charge
// power itself
func TestPeakPausesGridCharge(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}

	s := site.peak()
	s.limit = 5000
	s.set = func(float64) error { return nil }

	// peak shaving off: nothing to give way to
	s.demand = 8000
	assert.False(t, site.peakPausesGridCharge())

	s.enabled = true

	// the charger alone may exceed the limit, only the demand without it counts
	s.demand = 1000
	assert.False(t, site.peakPausesGridCharge())

	// a peak pauses charging ...
	s.demand = 6000
	assert.True(t, site.peakPausesGridCharge())

	// ... and it stays paused for the hold-off after the peak is over
	s.demand = 1000
	assert.True(t, site.peakPausesGridCharge())

	// once the hold-off has run out, charging may resume
	s.chargePause = time.Now().Add(-time.Second)
	assert.False(t, site.peakPausesGridCharge())
}

// TestPeakHandsBackWhenOff verifies that the free value is written once while
// peak shaving is off, retried after a failed write, and written once more after
// peak shaving ran again
func TestPeakHandsBackWhenOff(t *testing.T) {
	sc := newScenario(t)

	var writes []float64
	fail := true

	s := sc.site.peak()
	s.enabled = false
	s.set = func(v float64) error {
		writes = append(writes, v)
		if fail {
			fail = false
			return errors.New("home assistant restarting")
		}
		return nil
	}

	sc.cycle(50, 1000, 0)
	sc.cycle(50, 1000, 0)
	sc.cycle(50, 1000, 0)
	sc.cycle(50, 1000, 0)

	// first write fails, second lands, then nothing more is sent
	assert.Equal(t, []float64{lm.DefaultFreeValue, lm.DefaultFreeValue}, writes)

	// peak shaving runs below the reserve and writes its setpoint every cycle
	writes = nil
	require.NoError(t, sc.site.SetPeakShaving(true))
	sc.cycle(20, 7000, 0)
	sc.cycle(20, 7000, 0)
	assert.Equal(t, []float64{2000, 2000}, writes)

	// switching off hands back right away, the following cycles send nothing
	writes = nil
	require.NoError(t, sc.site.SetPeakShaving(false))
	sc.cycle(20, 7000, 0)
	sc.cycle(20, 7000, 0)
	assert.Equal(t, []float64{lm.DefaultFreeValue}, writes)
}

// TestBatteryChargeSetpoint verifies the controlled grid charge power: trimmed
// to the room below the peak limit, zero below the minimum, and the full
// expected power while peak shaving is off
func TestBatteryChargeSetpoint(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}

	s := site.peak()
	s.limit = 5000
	s.chargePower = 6250
	s.set = func(float64) error { return nil }

	tc := []struct {
		name    string
		enabled bool
		demand  float64
		want    float64
	}{
		{"peak shaving off, full power", false, 1000, 6250},
		{"room below the limit", true, 1000, 4000},
		{"plenty of room, capped at the charge power", true, -3000, 6250},
		{"less than the minimum left", true, 4700, 0},
		{"demand above the limit", true, 6000, 0},
		{"fractions dropped", true, 1234.6, 3765},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			s.enabled = tc.enabled
			s.demand = tc.demand
			assert.Equal(t, tc.want, site.batteryChargeSetpoint())
		})
	}

	// without any charge power to go by, nothing is charged
	s.chargePower = 0
	s.enabled = false
	assert.Equal(t, 0.0, site.batteryChargeSetpoint())
}

// TestChargeValueWrittenEveryCycle verifies that the charge setpoint reaches the
// entity every time, also when unchanged
func TestChargeValueWrittenEveryCycle(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}

	var writes []float64

	s := site.peak()
	s.chargeSet = func(v float64) error {
		writes = append(writes, v)
		return nil
	}

	site.writeChargeValue(4000)
	site.writeChargeValue(4000)
	site.writeChargeValue(0)
	site.writeChargeValue(0)

	assert.Equal(t, []float64{4000, 4000, 0, 0}, writes)
	assert.True(t, site.chargePowerControlled())
}

// TestPeakValueWrittenEveryCycle replays the log of a peak shaving run with
// constant values: 8160W grid, 6250W battery, soc below the reserve. The
// unchanged setpoints have to be written in every cycle, so a value changed in
// Home Assistant does not stick.
func TestPeakValueWrittenEveryCycle(t *testing.T) {
	sc := newScenario(t)

	var writes, charges []float64

	s := sc.site.peak()
	s.limit = 10000
	s.set = func(v float64) error { writes = append(writes, v); return nil }
	s.chargeSet = func(v float64) error { charges = append(charges, v); return nil }

	for range 3 {
		sc.cycle(20, 8160, 6250)
	}

	assert.Equal(t, []float64{4410, 4410, 4410}, writes)
	assert.Equal(t, []float64{0, 0, 0}, charges, "the peak pauses grid charging")
}
