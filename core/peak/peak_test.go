package peak_test

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/peak"
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
		// fractions are dropped, some number entities reject them
		{"rounded to whole watts", 6234.6, 0, 1235},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, peak.Setpoint(tc.gridPower, tc.batteryPwr, limit))
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
	setpoint := peak.Setpoint(demand, 0, limit)
	assert.Equal(t, 3000.0, setpoint)

	// the controller applies it, so the grid drops by that amount. Feeding the
	// new grid value back must yield the same setpoint, not zero.
	for range 5 {
		grid := demand - setpoint
		setpoint = peak.Setpoint(grid, setpoint, limit)
		assert.Equal(t, 3000.0, setpoint)
	}
}

// TestPeakSetpointFollowsDemand verifies the setpoint tracks a changing load
// while the battery is already discharging
func TestPeakSetpointFollowsDemand(t *testing.T) {
	const limit = 5000.0

	// battery discharging 3000 W, house load rises from 8000 to 10000 W
	setpoint := peak.Setpoint(10000-3000, 3000, limit)
	assert.Equal(t, 5000.0, setpoint)

	// ... and drops to 6000 W
	setpoint = peak.Setpoint(6000-5000, 5000, limit)
	assert.Equal(t, 1000.0, setpoint)

	// ... and below the limit, the battery is released
	setpoint = peak.Setpoint(4000-1000, 1000, limit)
	assert.Equal(t, 0.0, setpoint)
}

// TestPeakAllowed covers the budget of the 15 minute window
func TestPeakAllowed(t *testing.T) {
	const limit = 5000.0
	window := peak.Window.Seconds()

	tc := []struct {
		name    string
		usedWs  float64
		elapsed time.Duration
		want    float64
	}{
		{"window start", 0, 0, 5000},
		{"on track", limit * 300, 5 * time.Minute, 5000},
		{"nothing drawn for 5 minutes", 0, 5 * time.Minute, 7500},
		{"10kW for 5 minutes", 10000 * 300, 5 * time.Minute, 2500},
		{"budget spent", limit * window, 10 * time.Minute, 0},
		{"overspent", limit * window * 2, 10 * time.Minute, 0},
		// the last cycle reaches into the next window, which starts on track
		{"on track, 10s left", limit * 890, 890 * time.Second, 5000},
		{"budget spent, 10s left", limit * window, 890 * time.Second, limit * 20 / 30},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, peak.Allowed(limit, tc.usedWs, tc.elapsed), 0.001)
		})
	}
}

// TestReserved covers the reserve hysteresis: held at or below the reserve,
// released from reserve + hysteresis on, unchanged in between
func TestReserved(t *testing.T) {
	assert.True(t, peak.Reserved(false, 30, 30, 2), "at the reserve")
	assert.True(t, peak.Reserved(true, 31, 30, 2), "in the band: stays held")
	assert.False(t, peak.Reserved(false, 31, 30, 2), "in the band: stays free")
	assert.False(t, peak.Reserved(true, 32, 30, 2), "released")
}
