package core

import (
	"errors"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
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

// TestPeakAllowed covers the budget of the 15 minute window
func TestPeakAllowed(t *testing.T) {
	const limit = 5000.0
	window := lm.PeakWindow.Seconds()

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
			assert.InDelta(t, tc.want, peakAllowed(limit, tc.usedWs, tc.elapsed), 0.001)
		})
	}
}

// peakWindowSite returns a site with a mock clock at the given minute of a window
func peakWindowSite(t *testing.T, minute int) (*Site, *clock.Mock) {
	t.Helper()
	lm.Reset()
	t.Cleanup(lm.Reset)

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 9, 25, 10, minute, 0, 0, time.UTC))

	site := &Site{log: util.NewLogger("test")}
	site.peakShaving.clock = clk
	site.peak().limit = 5000

	return site, clk
}

// sample runs the window update every 30s for the given duration
func sample(site *Site, clk *clock.Mock, grid float64, d time.Duration) {
	for end := clk.Now().Add(d); clk.Now().Before(end); {
		clk.Add(30 * time.Second)
		site.updatePeakWindow(grid)
	}
}

// TestPeakWindowBudget verifies that energy not drawn earlier in the window
// allows more later, and the limits on that
func TestPeakWindowBudget(t *testing.T) {
	site, clk := peakWindowSite(t, 0)
	s := site.peak()

	site.updatePeakWindow(0)
	assert.Equal(t, 5000.0, s.allowed)

	// 5 minutes without drawing anything
	sample(site, clk, 0, 5*time.Minute)
	assert.InDelta(t, 7500, s.allowed, 0.001)

	// a 9kW spike needs only what exceeds the allowed power
	assert.Equal(t, 1500.0, peakSetpoint(9000, 0, s.allowed))

	// drawing exactly the allowed power keeps it where it is
	sample(site, clk, 7500, 2*time.Minute)
	assert.InDelta(t, 7500, s.allowed, 0.001)

	// the cap: 10 minutes without drawing would allow 15kW, at most 2 x 5kW
	site, clk = peakWindowSite(t, 0)
	s = site.peak()
	site.updatePeakWindow(0)
	sample(site, clk, 0, 10*time.Minute)
	assert.Equal(t, 10000.0, s.allowed)
}

// TestPeakWindowFreeze verifies that from the freeze minute on the allowed
// power no longer grows but still falls
func TestPeakWindowFreeze(t *testing.T) {
	site, clk := peakWindowSite(t, 0)
	s := site.peak()
	require.NoError(t, site.SetLmAdvanced("peakCap", 10))

	site.updatePeakWindow(0)
	sample(site, clk, 0, 12*time.Minute)
	assert.InDelta(t, 25000, s.allowed, 0.001, "12 minutes unused leave 75kWmin for 3 minutes")

	// without the freeze it would be 37.5kW one minute later
	sample(site, clk, 0, time.Minute)
	assert.InDelta(t, 25000, s.allowed, 0.001)

	// 40kW for a minute would leave 35kW for the last minute, still capped by the freeze
	sample(site, clk, 40000, time.Minute)
	assert.InDelta(t, 25000, s.allowed, 0.001)

	// drawing more than allowed still lowers it: 60kW for 30s leaves 10kW for the last 30s
	sample(site, clk, 60000, 30*time.Second)
	assert.InDelta(t, 10000, s.allowed, 0.001)

	// a new window starts on track again
	sample(site, clk, 5000, time.Minute)
	assert.Equal(t, 5000.0, s.allowed)
}

// TestPeakWindowUnmetered verifies that the part of a window evcc did not see
// counts at the limit, and that a sample from the previous window carries over
func TestPeakWindowUnmetered(t *testing.T) {
	// evcc started 5 minutes into the window: nothing is known to be left over
	site, clk := peakWindowSite(t, 5)
	s := site.peak()
	site.updatePeakWindow(0)
	assert.Equal(t, 5000.0, s.allowed)

	sample(site, clk, 0, 5*time.Minute)
	assert.InDelta(t, 10000, s.allowed, 0.001, "5 unused minutes for the last 5")

	// crossing into the next window, the 30s since the boundary are metered
	clk.Set(time.Date(2026, 9, 25, 10, 14, 50, 0, time.UTC))
	site.updatePeakWindow(0)
	clk.Set(time.Date(2026, 9, 25, 10, 15, 20, 0, time.UTC))
	site.updatePeakWindow(6000)

	assert.Equal(t, time.Date(2026, 9, 25, 10, 15, 0, 0, time.UTC), s.meteredFrom)
	assert.InDelta(t, 6000*20, s.windowWs, 0.001)
	assert.InDelta(t, 6000, s.windowAvg, 0.001)

	// after a longer gap the next window is not metered from its start
	clk.Set(time.Date(2026, 9, 25, 10, 33, 0, 0, time.UTC))
	site.updatePeakWindow(0)
	assert.Equal(t, clk.Now(), s.meteredFrom)
	assert.Equal(t, 5000.0, s.allowed)
}

// energyStep advances the clock by 30s and updates the window with the grid
// power and the grid meter's counter, nil = meter without counter
func energyStep(site *Site, clk *clock.Mock, grid float64, meter *float64) {
	clk.Add(30 * time.Second)
	site.setPeakGridEnergy(meter)
	site.updatePeakWindow(grid)
}

func kWh(v float64) *float64 { return &v }

// TestPeakWindowSources verifies the order of the energy sources: grid meter,
// Home Assistant sensor, grid power
func TestPeakWindowSources(t *testing.T) {
	site, clk := peakWindowSite(t, 0)
	s := site.peak()

	var entityReads int
	entity := 50.0
	s.energyGet = func() (float64, error) { entityReads++; return entity, nil }

	// the grid meter's counter wins: 0.05kWh in 30s is 6kW, whatever the power says
	energyStep(site, clk, 0, kWh(100))
	energyStep(site, clk, 0, kWh(100.05))
	assert.InDelta(t, 180000, s.windowWs, 0.001)
	assert.Equal(t, peakSourceMeter, s.source)
	assert.Zero(t, entityReads, "the sensor is not read while the meter has a counter")

	// without a meter counter the sensor is used, the first reading is a baseline
	// and the interval in between comes from the grid power
	energyStep(site, clk, 1000, nil)
	assert.InDelta(t, 180000+30000, s.windowWs, 0.001)
	entity = 50.025
	energyStep(site, clk, 0, nil)
	assert.InDelta(t, 210000+90000, s.windowWs, 0.001)
	assert.Equal(t, peakSourceEntity, s.source)

	// a failed read falls back to the grid power for that interval, the next
	// reading is a new baseline so nothing is counted twice
	s.energyGet = func() (float64, error) { return 0, errors.New("unavailable") }
	energyStep(site, clk, 2000, nil)
	assert.InDelta(t, 300000+60000, s.windowWs, 0.001)
	assert.Equal(t, peakSourcePower, s.source)

	s.energyGet = func() (float64, error) { return 50.2, nil }
	energyStep(site, clk, 2000, nil)
	assert.InDelta(t, 360000+60000, s.windowWs, 0.001)

	// a counter going backwards is reset or replaced, the grid power fills in
	s.energyGet = func() (float64, error) { return 1, nil }
	energyStep(site, clk, 4000, nil)
	assert.InDelta(t, 420000+120000, s.windowWs, 0.001)
}

// TestPeakWindowLateCounter verifies that a counter updating less often than
// evcc samples is followed, and that one which stopped is replaced by the grid
// power for the rest of the window
func TestPeakWindowLateCounter(t *testing.T) {
	site, clk := peakWindowSite(t, 0)
	s := site.peak()

	// the counter moves every 90s only, by 0.15kWh = 6kW
	energyStep(site, clk, 6000, kWh(10))
	energyStep(site, clk, 6000, kWh(10))
	energyStep(site, clk, 6000, kWh(10))
	assert.Zero(t, s.windowWs, "a late counter is not replaced right away")
	energyStep(site, clk, 6000, kWh(10.15))
	assert.InDelta(t, 540000, s.windowWs, 0.001)

	// it stops: after 2 minutes the grid power takes over, including what was
	// drawn while it stood still
	for range 4 {
		energyStep(site, clk, 6000, kWh(10.15))
	}
	assert.False(t, s.stale)
	assert.InDelta(t, 540000, s.windowWs, 0.001)

	energyStep(site, clk, 6000, kWh(10.15))
	assert.True(t, s.stale)
	assert.InDelta(t, 540000+5*180000, s.windowWs, 0.001)

	// its catching up later is not counted a second time
	energyStep(site, clk, 6000, kWh(10.5))
	assert.InDelta(t, 540000+6*180000, s.windowWs, 0.001)

	// the next window tries the counter again
	clk.Set(time.Date(2026, 9, 25, 10, 14, 50, 0, time.UTC))
	site.setPeakGridEnergy(kWh(11))
	site.updatePeakWindow(6000)
	energyStep(site, clk, 0, kWh(11.05))
	assert.False(t, s.stale)
	assert.Zero(t, s.windowWs, "the step across the boundary may hold the backlog, the grid power counts")

	energyStep(site, clk, 0, kWh(11.1))
	assert.InDelta(t, 180000, s.windowWs, 0.001)
	assert.Equal(t, peakSourceMeter, s.source)
}
