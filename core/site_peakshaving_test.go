package core

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/request"
	jww "github.com/spf13/jwalterweatherman"
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

// TestPeakHandsBackWhenOff verifies that the free value is written once when
// peak shaving is switched off after it held the battery back, retried after a
// failed write, and written once more after peak shaving ran again
func TestPeakHandsBackWhenOff(t *testing.T) {
	sc := newScenario(t)

	var writes []float64
	fail := false

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

	// never controlled: nothing to hand back
	sc.cycle(50, 1000, 0)
	sc.cycle(50, 1000, 0)
	assert.Empty(t, writes)

	// peak shaving runs below the reserve and writes its setpoint every cycle
	require.NoError(t, sc.site.SetPeakShaving(true))
	sc.cycle(20, 7000, 0)
	sc.cycle(20, 7000, 0)
	assert.Equal(t, []float64{2000, 2000}, writes)
	assert.True(t, sc.site.peakOwned())

	// switching off hands back right away, the first write fails and is retried,
	// then nothing more is sent
	writes = nil
	fail = true
	require.NoError(t, sc.site.SetPeakShaving(false))
	assert.True(t, sc.site.peakOwned())
	sc.cycle(20, 7000, 0)
	sc.cycle(20, 7000, 0)
	sc.cycle(20, 7000, 0)
	assert.Equal(t, []float64{peak.DefaultFreeValue, peak.DefaultFreeValue}, writes)
	assert.False(t, sc.site.peakOwned())

	// and once more after peak shaving ran again
	writes = nil
	require.NoError(t, sc.site.SetPeakShaving(true))
	sc.cycle(20, 7000, 0)
	require.NoError(t, sc.site.SetPeakShaving(false))
	assert.Equal(t, []float64{2000, peak.DefaultFreeValue}, writes)
}

// TestPeakNoHandBackWithoutOwned verifies that an entity evcc never controlled is
// not written to: peak shaving off at the start, entity removed, the meters
// failing
func TestPeakNoHandBackWithoutOwned(t *testing.T) {
	sc := newScenario(t)
	clk := clock.NewMock()

	var writes []float64

	s := sc.site.peak()
	s.clock = clk
	s.enabled = false
	s.entity = "number.discharge"
	s.set = func(v float64) error { writes = append(writes, v); return nil }

	for range 5 {
		sc.cycle(20, 7000, 0)
		sc.cycle(50, 1000, 0)
	}
	require.NoError(t, sc.site.SetPeakShaving(false))
	assert.Empty(t, writes)

	// removed while off
	require.NoError(t, sc.site.SetPeakShavingEntity(""))
	assert.Empty(t, writes)

	// enabled but above the reserve and the meters failing: not controlled either
	s.set = func(v float64) error { writes = append(writes, v); return nil }
	s.enabled = true
	s.updated = clk.Now()
	clk.Add(10 * time.Minute)
	sc.site.peakCheckMeters()
	assert.True(t, s.metersLost)
	assert.Empty(t, writes)
	assert.False(t, sc.site.peakOwned())
}

// TestPeakHandBackAfterRestartWhenOwned verifies that a restart in the middle of
// a control still hands back: owned is stored, the free value is written once,
// and owned is cleared afterwards
func TestPeakHandBackAfterRestartWhenOwned(t *testing.T) {
	sc := newScenario(t)

	settings.SetBool(keys.PeakShavingOwned, true)
	sc.site.restorePeakSettings()
	assert.True(t, sc.site.peakOwned())

	var writes []float64

	s := sc.site.peak()
	s.enabled = false
	s.set = func(v float64) error { writes = append(writes, v); return nil }

	for range 3 {
		sc.cycle(50, 1000, 0)
	}

	assert.Equal(t, []float64{peak.DefaultFreeValue}, writes)
	assert.False(t, sc.site.peakOwned())

	stored, err := settings.Bool(keys.PeakShavingOwned)
	require.NoError(t, err)
	assert.False(t, stored)

	// the control after the hand back is owned again, and kept in the settings
	s.enabled = true
	sc.cycle(20, 7000, 0)
	assert.True(t, sc.site.peakOwned())

	stored, err = settings.Bool(keys.PeakShavingOwned)
	require.NoError(t, err)
	assert.True(t, stored)
}

// testLogger returns a logger writing every line to the buffer
func testLogger(buf *bytes.Buffer) *util.Logger {
	return &util.Logger{Notepad: jww.NewNotepad(jww.LevelTrace, jww.LevelTrace, buf, io.Discard, "", 0)}
}

// TestPeakWriteErrorLoggedOnce verifies that the same error is logged once as an
// error and then at debug level, a different one is logged again, and a write
// landing again is logged once
func TestPeakWriteErrorLoggedOnce(t *testing.T) {
	var buf bytes.Buffer

	site := &Site{log: testLogger(&buf)}

	var err error
	set := func(float64) error { return err }

	count := func(level string) int { return strings.Count(buf.String(), level) }

	err = errors.New("rejected")
	for range 10 {
		assert.False(t, site.writeOutput("peak shaving", set, 2000))
	}
	assert.Equal(t, 1, count("ERROR"))
	assert.Equal(t, 9, count("DEBUG"))

	// another error is a new line
	err = errors.New("unreachable")
	for range 3 {
		site.writeOutput("peak shaving", set, 2000)
	}
	assert.Equal(t, 2, count("ERROR"))

	// another output has its own state
	site.writeOutput("grid charge power", set, 0)
	assert.Equal(t, 3, count("ERROR"))

	// a landing write is reported once
	err = nil
	for range 3 {
		assert.True(t, site.writeOutput("peak shaving", set, 2000))
	}
	assert.Equal(t, 1, count("INFO"))
	assert.Contains(t, buf.String(), "peak shaving: write 2000W ok again")

	// and the next failure is an error again
	err = errors.New("unreachable")
	site.writeOutput("peak shaving", set, 2000)
	assert.Equal(t, 4, count("ERROR"))
}

// statusError returns the error of a failed request with the response body
func statusError(t *testing.T, code int, path, body string) error {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+path, nil)
	require.NoError(t, err)

	_, err = (&request.Helper{Client: srv.Client()}).DoBody(req)
	require.Error(t, err)

	return err
}

// TestPeakWriteErrorBody verifies that the response of the server is part of the
// logged error, on one line and shortened, and that a general 500 of Home
// Assistant points to its log
func TestPeakWriteErrorBody(t *testing.T) {
	err := statusError(t, http.StatusInternalServerError, "/api/services/number/set_value", "500 Internal Server Error\n\nServer got itself in trouble")
	text := writeErrorText(err)
	assert.Contains(t, text, "unexpected status: 500")
	assert.Contains(t, text, ": 500 Internal Server Error Server got itself in trouble")
	assert.NotContains(t, text, "\n")
	assert.True(t, strings.HasSuffix(text, " (details in the Home Assistant log)"), text)

	// shortened
	err = statusError(t, http.StatusBadRequest, "/api/services/number/set_value", strings.Repeat("x", 500))
	text = writeErrorText(err)
	assert.Contains(t, text, strings.Repeat("x", maxErrorBody)+"...")
	assert.NotContains(t, text, strings.Repeat("x", maxErrorBody+1))
	assert.NotContains(t, text, "Home Assistant log", "only a 500")

	// a 500 elsewhere is no service call
	err = statusError(t, http.StatusInternalServerError, "/api/states/number.x", "")
	assert.NotContains(t, writeErrorText(err), "Home Assistant log")

	// a plain error stays as it is
	assert.Equal(t, "boom", writeErrorText(errors.New("boom")))
}

// TestPeakEntityRemovedHandsBack verifies that removing the entities while peak
// shaving and grid charging run leaves neither on its last setpoint: the
// discharge entity gets the free value, the charge power entity zero
func TestPeakEntityRemovedHandsBack(t *testing.T) {
	sc := newScenario(t)
	sc.withDynamicCharge()

	s := sc.site.peak()
	s.entity = "number.discharge"
	s.chargeEntity = "number.charge"

	// below the reserve and grid charging
	assert.True(t, sc.cycle(20, 1000, 0))
	assert.Equal(t, 0.0, val(sc.peak))
	assert.Equal(t, 4000.0, val(sc.charge))

	require.NoError(t, sc.site.SetPeakShavingEntity(""))
	assert.Equal(t, peak.DefaultFreeValue, val(sc.peak))
	assert.False(t, sc.site.GetPeakShaving())

	require.NoError(t, sc.site.SetPeakShavingChargeEntity(""))
	assert.Equal(t, 0.0, val(sc.charge))
	assert.False(t, sc.site.chargePowerControlled())
	assert.Equal(t, 0.0, s.chargeSetpoint, "overview setpoint")
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

// TestPeakMetersLost verifies that a setpoint does not stay in the entity while
// the meters fail: after peak.MaxGap the free value is written once and grid
// charging pauses, until the meters are back
func TestPeakMetersLost(t *testing.T) {
	sc := newScenario(t)
	clk := clock.NewMock()

	var writes []float64

	s := sc.site.peak()
	s.clock = clk
	s.set = func(v float64) error { writes = append(writes, v); return nil }

	// below the reserve, no peak: no discharge, soc-based grid charging runs
	assert.True(t, sc.cycle(20, 3000, 0))
	assert.Equal(t, []float64{0}, writes)

	// the meters fail: updatePeakShaving no longer runs, the rest of the cycle does
	failed := func() bool {
		clk.Add(30 * time.Second)
		charge := sc.site.batteryGridChargeRequested(sc.rate)
		sc.site.updateBatteryModePeakAware(charge, false, sc.rate)
		return charge
	}

	// up to peak.MaxGap nothing changes
	writes = nil
	for range 4 {
		assert.True(t, failed())
	}
	assert.Empty(t, writes)
	assert.True(t, sc.site.peakShavingActive())

	// beyond it: free value once, no grid charging
	assert.False(t, failed())
	assert.False(t, failed())
	assert.Equal(t, []float64{peak.DefaultFreeValue}, writes)
	assert.False(t, sc.site.peakShavingActive())

	// meters back: the setpoint again
	writes = nil
	clk.Add(30 * time.Second)
	sc.cycle(20, 3000, 0)
	assert.Equal(t, []float64{0}, writes)
	assert.False(t, s.metersLost)
	assert.True(t, sc.site.peakShavingActive())
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

// TestNumberWrite verifies that a value the entity already holds is not written
// again, while an unavailable entity and a real change are
func TestNumberWrite(t *testing.T) {
	r := peak.Range{Min: 0, Max: 15360, Step: 10}

	tc := []struct {
		name      string
		current   string
		val       float64
		up        bool
		tolerance float64
		want      float64
		write     bool
	}{
		{"unchanged free value", "10000.0", 10000, true, 0, 10000, false},
		{"rounded onto the held value", "4540", 4531, true, 0, 4540, false},
		{"one step changed", "4540", 4545, true, 0, 4550, true},
		{"within tolerance", "4540", 4570, true, 50, 4570, false},
		{"beyond tolerance", "4540", 4600, true, 50, 4600, true},
		{"stop always lands", "40", 0, false, 50, 0, true},
		{"unavailable", "unavailable", 4540, true, 0, 4540, true},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			val, write := numberWrite(r, tc.current, tc.val, tc.up, tc.tolerance)
			assert.Equal(t, tc.want, val)
			assert.Equal(t, tc.write, write)
		})
	}
}
