package core

import (
	"errors"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	assert.Equal(t, []float64{peak.DefaultFreeValue, peak.DefaultFreeValue}, writes)

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
	assert.Equal(t, []float64{peak.DefaultFreeValue}, writes)
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

// peakWindowSite returns a site with a mock clock at the given minute of a window
func peakWindowSite(t *testing.T, minute int) (*Site, *clock.Mock) {
	t.Helper()

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 9, 25, 10, minute, 0, 0, time.UTC))

	site := &Site{log: util.NewLogger("test")}
	site.custom.peak.clock = clk
	site.peak().limit = 5000

	return site, clk
}

// energyStep advances the clock by 30s and updates the window with the grid
// power and the grid meter's counter, nil = meter without counter
func energyStep(site *Site, clk *clock.Mock, grid float64, meter *float64) {
	clk.Add(30 * time.Second)
	site.setPeakGridEnergy(meter)
	site.updatePeakWindow(grid, 0)
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
	assert.InDelta(t, 180000, s.window.DrawnWs, 0.001)
	assert.Equal(t, peak.SourceMeter, s.window.Source)
	assert.Zero(t, entityReads, "the sensor is not read while the meter has a counter")

	// without a meter counter the sensor is used, the first reading is a baseline
	// and the interval in between comes from the grid power
	energyStep(site, clk, 1000, nil)
	assert.InDelta(t, 180000+30000, s.window.DrawnWs, 0.001)
	entity = 50.025
	energyStep(site, clk, 0, nil)
	assert.InDelta(t, 210000+90000, s.window.DrawnWs, 0.001)
	assert.Equal(t, peak.SourceEntity, s.window.Source)

	// a failed read falls back to the grid power for that interval, the next
	// reading is a new baseline so nothing is counted twice
	s.energyGet = func() (float64, error) { return 0, errors.New("unavailable") }
	energyStep(site, clk, 2000, nil)
	assert.InDelta(t, 300000+60000, s.window.DrawnWs, 0.001)
	assert.Equal(t, peak.SourcePower, s.window.Source)

	s.energyGet = func() (float64, error) { return 50.2, nil }
	energyStep(site, clk, 2000, nil)
	assert.InDelta(t, 360000+60000, s.window.DrawnWs, 0.001)

	// a counter going backwards is reset or replaced, the grid power fills in
	s.energyGet = func() (float64, error) { return 1, nil }
	energyStep(site, clk, 4000, nil)
	assert.InDelta(t, 420000+120000, s.window.DrawnWs, 0.001)
}

// TestPeakReserveKeepsExternalMode: below the reserve peak shaving keeps the
// battery in normal mode, but a mode set from outside through the api is left
// to upstream, which applies it
func TestPeakReserveKeepsExternalMode(t *testing.T) {
	sc := newScenario(t)
	sc.site.lms().socChargeEnabled = false

	// below the reserve, hold (e.g. from discharge control) gives way to normal
	sc.site.SetBatteryMode(api.BatteryHold)
	sc.cycle(25, 3000, 0)
	assert.Equal(t, api.BatteryNormal, sc.mode())

	// an external mode wins
	sc.site.Lock()
	sc.site.batteryModeExternal = api.BatteryHold
	sc.site.Unlock()
	sc.cycle(25, 3000, 0)
	assert.Equal(t, api.BatteryHold, sc.mode())
}
