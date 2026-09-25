package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lmSwitch is a switch device charger with an optional configured power
type lmSwitch struct {
	api.Charger
	rated float64
}

func (s *lmSwitch) Features() []api.Feature { return []api.Feature{api.SwitchDevice} }
func (s *lmSwitch) RatedPower() float64     { return s.rated }

type lmMeter struct{ power float64 }

func (m *lmMeter) CurrentPower() (float64, error) { return m.power, nil }

// newSwitchLoadpoint returns a switch loadpoint on a 10 kW circuit metered at
// the given grid power
func newSwitchLoadpoint(t *testing.T, rated, grid float64) (*Loadpoint, *lmMeter, api.Circuit) {
	t.Helper()
	lm.Reset()
	Voltage = 230

	m := &lmMeter{power: grid}
	c, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, m, 0)
	require.NoError(t, err)
	require.NoError(t, c.Update(nil))

	lp := &Loadpoint{
		log:        util.NewLogger("lp"),
		clock:      clock.NewMock(),
		circuit:    c,
		charger:    &lmSwitch{rated: rated},
		maxCurrent: 16,
	}

	return lp, m, c
}

func TestSwitchAllOrNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rated    float64 // configured power, 0 = not set
		grid     float64 // grid power with the switch off
		charging float64 // measured switch power, 0 = off
		wantOn   bool
	}{
		// switching on with the configured power
		{"3 kW fits into 4 kW", 3000, 6000, 0, true},
		{"3 kW does not fit into 1.6 kW", 3000, 8410, 0, false},
		{"exactly fits", 3000, 7000, 0, true},
		// without a configured power the nominal 16 A = 3680 W are assumed
		{"no power set, 3680 W do not fit into 3.5 kW", 0, 6500, 0, false},
		{"no power set, 3680 W fit into 4 kW", 0, 6000, 0, true},
		// running: the measurement counts, the 6 A minimum does not
		{"running within the limit", 3000, 9000, 3000, true},
		{"running into an overload", 3000, 11410, 3000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lp, _, _ := newSwitchLoadpoint(t, tc.rated, tc.grid)
			lp.chargePower = tc.charging

			got := lp.lmLimit(16)
			assert.Equal(t, tc.wantOn, got > 0, "limit %.3gA", got)
		})
	}
}

func TestSwitchPowerSources(t *testing.T) {
	lp, _, _ := newSwitchLoadpoint(t, 0, 0)

	// nothing known: nominal max current on one phase
	assert.Equal(t, 3680.0, lp.lmSwitchPower())

	// measured while on, remembered once off
	lp.chargePower = 2800
	assert.Equal(t, 2800.0, lp.lmSwitchPower())
	lp.chargePower = 0
	assert.Equal(t, 2800.0, lp.lmSwitchPower())

	// a configured power wins over the remembered measurement
	lp.charger = &lmSwitch{rated: 3000}
	assert.Equal(t, 3000.0, lp.lmSwitchPower())

	// ... but not over an actual measurement
	lp.chargePower = 2950
	assert.Equal(t, 2950.0, lp.lmSwitchPower())
}

// guardFor protects the given loadpoints for d
func guardFor(d time.Duration, lps ...*Loadpoint) {
	lm.SetGuardLookup(func(l lm.Load) time.Duration {
		for _, lp := range lps {
			if l == lp {
				return d
			}
		}
		return 0
	})
}

// cycle runs one limit check with the switch drawing its power while on
func switchCycle(t *testing.T, lp *Loadpoint, m *lmMeter, c api.Circuit, base float64) bool {
	t.Helper()

	if lp.enabled {
		lp.chargePower = 3000
	} else {
		lp.chargePower = 0
	}
	m.power = base + lp.chargePower
	require.NoError(t, c.Update(nil))

	on := lp.lmLimit(16) > 0
	lp.enabled = on

	return on
}

// TestShedGuardSwitch: a protected heater shed in an overload stays off for the
// guard time, although the power is back right after
func TestShedGuardSwitch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protected bool
	}{
		{"protected", true},
		{"not protected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lp, m, c := newSwitchLoadpoint(t, 3000, 0)
			clk := lp.clock.(*clock.Mock)

			if tc.protected {
				guardFor(5*time.Minute, lp)
			}

			lp.enabled = true
			require.True(t, switchCycle(t, lp, m, c, 5000), "runs")

			// the base load jumps: 12 kW + 3 kW heater on a 10 kW circuit
			require.False(t, switchCycle(t, lp, m, c, 12000), "shed")

			// the base load is back down, 3 kW would fit again
			clk.Add(10 * time.Second)
			if !tc.protected {
				assert.True(t, switchCycle(t, lp, m, c, 5000), "back on in the next cycle")
				return
			}

			assert.False(t, switchCycle(t, lp, m, c, 5000), "held off")
			clk.Add(4*time.Minute + 49*time.Second)
			assert.False(t, switchCycle(t, lp, m, c, 5000), "still held off just before the end")
			clk.Add(time.Second)
			assert.True(t, switchCycle(t, lp, m, c, 5000), "back on after 5 minutes")
		})
	}
}

// TestShedGuardOnlyAfterShed: a switch that could not start because there was
// no room was not shed and may start as soon as there is
func TestShedGuardOnlyAfterShed(t *testing.T) {
	lp, m, c := newSwitchLoadpoint(t, 3000, 0)
	guardFor(5*time.Minute, lp)

	assert.False(t, switchCycle(t, lp, m, c, 8000), "no room to start")
	assert.True(t, switchCycle(t, lp, m, c, 5000), "starts right away")
}

// TestShedGuardWallbox: a wallbox pushed below its minimum current is shed and
// held off like a switch, while being throttled is not a shed
func TestShedGuardWallbox(t *testing.T) {
	lp, m, c := newSwitchLoadpoint(t, 0, 0)
	lp.charger = &lmWallbox{}
	lp.phases = 3
	lp.minCurrent = 6
	lp.enabled = true
	clk := lp.clock.(*clock.Mock)

	guardFor(5*time.Minute, lp)

	wallbox := func(base float64) float64 {
		m.power = base + lp.chargePower
		require.NoError(t, c.Update(nil))
		return lp.lmLimit(16)
	}

	// 16 A on three phases, throttled to what fits: not a shed
	lp.chargePower = 11040
	assert.InDelta(t, 13.0, wallbox(1000), 0.5, "throttled")
	assert.Zero(t, lm.Guarded(lp, clk.Now()))

	// not even the 6 A minimum fits anymore: shed
	lp.chargePower = 4140
	assert.Less(t, wallbox(7000), 6.0, "shed")
	assert.Equal(t, 5*time.Minute, lm.Guarded(lp, clk.Now()))

	// power is back, the guard holds it off
	lp.enabled, lp.chargePower = false, 0
	assert.Zero(t, wallbox(1000), "held off")

	clk.Add(5 * time.Minute)
	assert.InDelta(t, 13.0, wallbox(1000), 0.5, "back after the guard, 9 kW fit")
}

// lmWallbox is a current controlled charger
type lmWallbox struct{ api.Charger }

// TestLmStatusSwitch: what the overview shows for a switch through an overload
// with the shed guard, and the events it logs
func TestLmStatusSwitch(t *testing.T) {
	lp, m, c := newSwitchLoadpoint(t, 3000, 0)
	lp.title = "Heizstab"
	clk := lp.clock.(*clock.Mock)
	guardFor(3*time.Minute, lp)

	state := func() string {
		st, _, _, _ := lmLoadpointState(lp, lp.chargePower, clk.Now())
		return st
	}

	// no room to start: waiting, with what it needs and what is free
	assert.False(t, switchCycle(t, lp, m, c, 8000))
	st, requested, allowed, _ := lmLoadpointState(lp, lp.chargePower, clk.Now())
	assert.Equal(t, lmStateWaiting, st)
	assert.Equal(t, 3000.0, requested)
	assert.Equal(t, 2000.0, allowed)

	// room: running
	assert.True(t, switchCycle(t, lp, m, c, 5000))
	lp.chargePower = 3000
	assert.Equal(t, lmStateRunning, state())

	// overload: shed and held off
	assert.False(t, switchCycle(t, lp, m, c, 12000))
	st, _, _, until := lmLoadpointState(lp, 0, clk.Now())
	assert.Equal(t, lmStateShed, st)
	if assert.NotNil(t, until) {
		assert.Equal(t, clk.Now().Add(3*time.Minute), *until)
	}

	ev := lm.Events()
	if assert.Len(t, ev, 1) {
		assert.Equal(t, lm.EventShed, ev[0].Type)
		assert.Equal(t, "Heizstab", ev[0].Load)
		assert.Equal(t, 3.0, ev[0].A, "guard minutes")
		assert.Equal(t, 15000.0, ev[0].B, "circuit power when shed")
	}

	// after the guard, with no demand: off
	clk.Add(3 * time.Minute)
	lp.chargePower = 0
	lp.lmLimit(0)
	assert.Equal(t, lmStateOff, state())
}

// TestLmStatusWallboxThrottled: a wallbox limited below its request but above
// its minimum is throttled, logged once
func TestLmStatusWallboxThrottled(t *testing.T) {
	lp, m, c := newSwitchLoadpoint(t, 0, 0)
	lp.title = "Wallbox"
	lp.charger = &lmWallbox{}
	lp.phases = 3
	lp.minCurrent = 6
	lp.enabled = true
	lp.chargePower = 11040

	for range 2 {
		m.power = 1000 + lp.chargePower
		require.NoError(t, c.Update(nil))
		lp.lmLimit(16)
	}

	st, requested, allowed, _ := lmLoadpointState(lp, lp.chargePower, lp.clock.Now())
	assert.Equal(t, lmStateThrottled, st)
	assert.Equal(t, 11040.0, requested)
	assert.Equal(t, 9000.0, allowed)

	ev := lm.Events()
	if assert.Len(t, ev, 1, "logged once, not every cycle") {
		assert.Equal(t, lm.EventThrottled, ev[0].Type)
		assert.Equal(t, 11040.0, ev[0].A)
		assert.Equal(t, "Wallbox", ev[0].Load)
		assert.Equal(t, 9000.0, ev[0].B)
	}
}

// lmLimit runs the circuit check of setLimit, without switching the charger.
// TestSetLimitUsesLmCircuit pins that setLimit gives the same result.
func (lp *Loadpoint) lmLimit(current float64) float64 {
	circuit := lp.lmCircuit()

	currentLimit := circuit.ValidateCurrent(lp.actualMaxChargeCurrent(), current)
	activePhases := lp.ActivePhases()
	powerLimit := circuit.ValidatePower(lp.chargePower, currentToPower(current, activePhases))
	limited := lp.roundedCurrent(min(currentLimit, powerToCurrent(powerLimit, activePhases)))

	circuit.done(current, limited)
	return limited
}

// fakeCharger switches without talking to a device
type fakeCharger struct {
	enabled  bool
	current  int64
	features []api.Feature
	rated    float64
}

func (c *fakeCharger) Status() (api.ChargeStatus, error) { return api.StatusC, nil }
func (c *fakeCharger) Enabled() (bool, error)            { return c.enabled, nil }
func (c *fakeCharger) Enable(v bool) error               { c.enabled = v; return nil }
func (c *fakeCharger) MaxCurrent(v int64) error          { c.current = v; return nil }
func (c *fakeCharger) Features() []api.Feature           { return c.features }
func (c *fakeCharger) RatedPower() float64               { return c.rated }

// TestSetLimitUsesLmCircuit is the contract with upstream's setLimit: its circuit
// check goes through lmCircuit, so priorities, switch devices and the shed guard
// apply to the real control path
func TestSetLimitUsesLmCircuit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		charger  *fakeCharger
		phases   int
		grid     float64 // circuit power with the loadpoint off
		wantOn   bool
		wantAmps int64
	}{
		{"switch fits", &fakeCharger{features: []api.Feature{api.SwitchDevice}, rated: 3000}, 1, 6000, true, 16},
		// upstream alone would switch on with 1.6 kW to spare, above the minimum current
		{"switch does not fit", &fakeCharger{features: []api.Feature{api.SwitchDevice}, rated: 3000}, 1, 8410, false, 0},
		{"wallbox throttled to what fits", &fakeCharger{}, 3, 1000, true, 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lm.Reset()
			Voltage = 230

			m := &lmMeter{power: tc.grid}
			c, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, m, 0)
			require.NoError(t, err)
			require.NoError(t, c.Update(nil))

			lp := NewLoadpoint(util.NewLogger("lp"), nil)
			lp.clock = clock.NewMock()
			lp.wakeUpTimer = NewTimer()
			lp.circuit = c
			lp.charger = tc.charger
			lp.phases = tc.phases
			lp.minCurrent, lp.maxCurrent = 6, 16

			require.NoError(t, lp.setLimit(16))
			assert.Equal(t, tc.wantOn, tc.charger.enabled)
			if tc.wantOn {
				assert.Equal(t, tc.wantAmps, tc.charger.current)
			}

			d, ok := lm.LastDecision(lp)
			require.True(t, ok, "the decision is recorded for the overview")
			assert.Positive(t, d.Requested)
		})
	}
}
