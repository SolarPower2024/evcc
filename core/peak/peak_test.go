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

// TestRangeFit covers the step rounding in both directions and the clamp to min
// and max
func TestRangeFit(t *testing.T) {
	fronius := peak.Range{Min: 0, Max: 10100, Step: 10}

	tc := []struct {
		name  string
		r     peak.Range
		value float64
		up    bool
		want  float64
	}{
		{"on a step", fronius, 4530, false, 4530},
		{"charge rounds down", fronius, 4537, false, 4530},
		{"discharge rounds up", fronius, 4531, true, 4540},
		{"on a step stays when rounding up", fronius, 4540, true, 4540},
		{"free value above max", fronius, 10000, true, 10000},
		{"above max", peak.Range{Min: 0, Max: 5120, Step: 10}, 10000, true, 5120},
		{"zero stays zero", fronius, 0, false, 0},
		{"below min", peak.Range{Min: 100, Max: 5000, Step: 50}, 0, false, 100},
		{"steps counted from min", peak.Range{Min: 5, Max: 1000, Step: 10}, 22, false, 15},
		{"fractional step", peak.Range{Min: 0, Max: 100, Step: 0.1}, 12.34, false, 12.3},
		{"no attributes", peak.Range{}, 4537.4, false, 4537.4},
		{"step only", peak.Range{Step: 100}, 4537, false, 4500},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, tc.r.Fit(tc.value, tc.up), 1e-9)
		})
	}
}

// TestRangeUnchanged covers the tolerance, the half step and the exact min and max
func TestRangeUnchanged(t *testing.T) {
	fronius := peak.Range{Min: 0, Max: 15360, Step: 10}

	tc := []struct {
		name                       string
		r                          peak.Range
		current, target, tolerance float64
		want                       bool
	}{
		{"equal", fronius, 4540, 4540, 0, true},
		{"one step without tolerance", fronius, 4540, 4550, 0, false},
		{"reported a little off", fronius, 4541, 4540, 0, true},
		{"below tolerance", fronius, 4540, 4580, 50, true},
		{"at tolerance", fronius, 4540, 4590, 50, false},
		{"free value below tolerance", fronius, 9980, 10000, 50, true},
		{"stop is exact", fronius, 30, 0, 50, false},
		{"stop reported a little off", fronius, 1, 0, 50, true},
		{"max is exact", fronius, 15330, 15360, 50, false},
		{"no attributes", peak.Range{}, 4540, 4541, 0, false},
		{"no attributes, tolerance", peak.Range{}, 4540, 4541, 10, true},
		{"no attributes, stop is exact", peak.Range{}, 5, 0, 10, false},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.r.Unchanged(tc.current, tc.target, tc.tolerance))
		})
	}
}
