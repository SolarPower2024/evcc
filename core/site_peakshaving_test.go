package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

// TestPeakChargeFits covers the grid charge gate. Blocking grid charging for as
// long as the reserve is armed would deadlock - the reserve could then only be
// refilled from pv - so the gate asks whether charging would create a peak.
func TestPeakChargeFits(t *testing.T) {
	const (
		limit  = 5000.0
		charge = 3000.0
	)

	tc := []struct {
		name                  string
		gridPower, batteryPwr float64
		want                  bool
	}{
		// the night case the deadlock used to break: low base load, so the
		// reserve can be refilled even while peak shaving is armed
		{"low base load at night", 500, 0, true},
		{"exactly at the limit", 2000, 0, true},
		{"one watt over", 2001, 0, false},
		{"high base load", 4000, 0, false},
		// an already running charge must not make the gate flip: the grid value
		// contains it, the battery power takes it back out
		{"already charging, still fits", 3500, -3000, true},
		{"already charging, no longer fits", 7500, -3000, false},
		// a discharging battery makes the grid value understate the demand, so
		// the gate has to look past it
		{"battery discharging, still fits", 0, 1000, true},
		{"battery hiding a load that does not fit", 1000, 2000, false},
		{"battery masking a high load", 2500, 3000, false},
		// exporting leaves plenty of room
		{"exporting to grid", -4000, 0, true},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, peakChargeFits(tc.gridPower, tc.batteryPwr, charge, limit))
		})
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
