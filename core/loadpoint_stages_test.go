package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/charger"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/grid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStagesLoadpoint returns a 3-phase loadpoint driving a 3 x 3 kW heater on a
// 10 kW circuit, with switches kept in a js vm
func newStagesLoadpoint(t *testing.T, vm string) (*Loadpoint, api.Charger, *lmMeter) {
	t.Helper()

	var stages []any
	for _, s := range []string{"s1", "s2", "s3"} {
		stages = append(stages, map[string]any{
			"enabled": map[string]any{"source": "js", "vm": vm, "script": "typeof " + s + " === 'undefined' ? false : " + s},
			"enable":  map[string]any{"source": "js", "vm": vm, "script": s + " = enable"},
		})
	}

	c, err := charger.NewSwitchStagesFromConfig(t.Context(), map[string]any{
		"stages":     stages,
		"stagepower": 3000,
		"features":   []any{"heating", "integrateddevice"},
	})
	require.NoError(t, err)

	m := &lmMeter{}
	ci, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, m, 0)
	require.NoError(t, err)

	lp := NewLoadpoint(util.NewLogger("lp"), nil)
	lp.clock = clock.NewMock()
	lp.wakeUpTimer = NewTimer()
	lp.circuit = ci
	lp.charger = c
	lp.phases = 3
	lp.minCurrent, lp.maxCurrent = 6, 16
	c.(loadpoint.Controller).LoadpointControl(lp)

	return lp, c, m
}

// stagesCycle runs one setLimit at full demand with the circuit metering base
// plus the heater, and returns the heater's power afterwards
func stagesCycle(t *testing.T, lp *Loadpoint, c api.Charger, m *lmMeter, base float64) float64 {
	t.Helper()

	p, err := c.(api.Meter).CurrentPower()
	require.NoError(t, err)
	lp.chargePower = p

	m.power = base + p
	require.NoError(t, lp.circuit.Update(nil))
	require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))

	p, err = c.(api.Meter).CurrentPower()
	require.NoError(t, err)
	return p
}

// TestStagesUpstreamLimits pins what the stepped heater relies on upstream: a
// power limiter's min and max power become the loadpoint's current range
func TestStagesUpstreamLimits(t *testing.T) {
	lp, _, _ := newStagesLoadpoint(t, "stageslimits")

	assert.InDelta(t, 3000.0/690, lp.effectiveMinCurrent(), 1e-9, "one stage, below the 6A configured")
	assert.InDelta(t, 9000.0/690, lp.effectiveMaxCurrent(), 1e-9, "all stages, below the 16A configured")
	assert.False(t, lp.chargerHasFeature(api.SwitchDevice), "goes through load management as a regulated load")
}

func TestStagesCircuitStepsDown(t *testing.T) {
	lp, c, m := newStagesLoadpoint(t, "stagescircuit")

	for _, tc := range []struct {
		name string
		base float64 // rest of the circuit
		want float64 // heater power
	}{
		{"7 kW free: two stages", 3000, 6000},
		{"10 kW free: all stages", 0, 9000},
		{"base rises to 4 kW: steps down to two, not off", 4000, 6000},
		{"base rises to 6.5 kW: one stage", 6500, 3000},
		{"base at 7.5 kW: off", 7500, 0},
		{"base back at 2 kW: on again with all that fits", 2000, 6000},
	} {
		got := stagesCycle(t, lp, c, m, tc.base)
		assert.Equal(t, tc.want, got, tc.name)
		assert.Equal(t, tc.want > 0, lp.enabled, tc.name)
	}
}

func TestStagesPvSurplus(t *testing.T) {
	// pv mode: the surplus decides the stages, never more than the surplus
	lp, c, _ := newStagesLoadpoint(t, "stagespv")
	lp.circuit = nil

	for _, tc := range []struct {
		surplus float64
		want    float64
	}{
		{3500, 3000},
		{6800, 6000},
		{9200, 9000},
		{5900, 3000},
	} {
		require.NoError(t, lp.setLimit(grid.PowerToCurrent(tc.surplus, 3)))
		p, err := c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		assert.Equal(t, tc.want, p, "%.0fW surplus", tc.surplus)
	}
}

func TestStagesGiveWayToHigherPriority(t *testing.T) {
	// the heater runs at full power when a wallbox with a higher priority asks
	// for 10A on 3 phases: the heater steps down to what is left, it is not shed
	heater, c, m := newStagesLoadpoint(t, "stagesprio")
	heater.priority = 1

	wb := &fakeCharger{}
	wallbox := NewLoadpoint(util.NewLogger("wallbox"), nil)
	wallbox.clock = clock.NewMock()
	wallbox.wakeUpTimer = NewTimer()
	wallbox.circuit = heater.circuit
	wallbox.charger = wb
	wallbox.phases = 3
	wallbox.priority = 5
	wallbox.minCurrent, wallbox.maxCurrent = 6, 10

	wallbox.lmOwn = heater.lmm() // one load management for both

	heaterPower := func() float64 {
		p, err := c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		return p
	}

	// one cycle of either loadpoint against the metered circuit
	run := func(lp *Loadpoint) {
		heater.chargePower = heaterPower()
		wallbox.chargePower = 0
		if wb.enabled {
			wallbox.chargePower = grid.CurrentToPower(float64(wb.current), 3)
		}
		m.power = heater.chargePower + wallbox.chargePower
		require.NoError(t, heater.circuit.Update(nil))
		require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))
	}

	run(heater)
	assert.Equal(t, 9000.0, heaterPower(), "alone: all stages")

	run(wallbox)
	assert.False(t, wb.enabled, "1 kW left, below the wallbox minimum: records its demand")

	run(heater)
	assert.Equal(t, 3000.0, heaterPower(), "gives way down to one stage, 10 kW - 6.9 kW")
	assert.True(t, heater.enabled, "not shed")

	run(wallbox)
	assert.True(t, wb.enabled)
	assert.Equal(t, int64(10), wb.current)

	// stable: neither loses ground
	run(heater)
	assert.Equal(t, 3000.0, heaterPower())
	run(wallbox)
	assert.Equal(t, int64(10), wb.current)
}

// TestStagesPublished pins the flags the ui reads to label the modes Off/Smart/On
// and to leave a loadpoint with assumed power out of the energy flow
func TestStagesPublished(t *testing.T) {
	lp, c, _ := newStagesLoadpoint(t, "stagespublished")
	lp.chargeMeter = newChargeMeter(c)

	ui := make(chan util.Param, 2)
	lp.uiChan = ui
	lp.publishStages()
	assert.Equal(t, util.Param{Key: keys.ChargerStages, Val: true}, <-ui)
	assert.Equal(t, util.Param{Key: keys.ChargePowerEstimated, Val: true}, <-ui)

	lp.charger = nil
	lp.chargeMeter = newChargeMeter(&lmMeter{power: 6000})
	lp.publishStages()
	assert.Equal(t, util.Param{Key: keys.ChargerStages, Val: false}, <-ui)
	assert.Equal(t, util.Param{Key: keys.ChargePowerEstimated, Val: false}, <-ui)
}

// TestStagesEstimatedPower verifies that without a power sensor the assumed power
// drives control but stays out of the home consumption, energy and sessions
func TestStagesEstimatedPower(t *testing.T) {
	lp, c, _ := newStagesLoadpoint(t, "stagesestimated")
	lp.chargeMeter = newChargeMeter(c)

	require.True(t, lp.powerEstimated())
	assert.Equal(t, 0.0, lp.meteredPower(6000))

	ui := make(chan util.Param, 2)
	lp.uiChan = ui
	lp.publishChargePower(6000)
	assert.Equal(t, util.Param{Key: keys.ChargePower, Val: 0.0}, <-ui)
	assert.Equal(t, util.Param{Key: keys.ChargePowerEstimate, Val: 6000.0}, <-ui)

	// a loadpoint meter measures
	lp.chargeMeter = newChargeMeter(&lmMeter{power: 6000})
	assert.False(t, lp.powerEstimated())
	assert.Equal(t, 6000.0, lp.meteredPower(6000))

	lp.publishChargePower(6000)
	assert.Equal(t, util.Param{Key: keys.ChargePower, Val: 6000.0}, <-ui)
	assert.Equal(t, util.Param{Key: keys.ChargePowerEstimate, Val: 0.0}, <-ui)

	// with a power sensor the heater measures itself
	measured, err := charger.NewSwitchStagesFromConfig(t.Context(), map[string]any{
		"stages": []any{map[string]any{
			"enabled": map[string]any{"source": "const", "value": "true"},
			"enable":  map[string]any{"source": "js", "vm": "stagesestimated2", "script": "x = enable"},
		}},
		"stagepower": 2000,
		"power":      map[string]any{"source": "const", "value": "1500"},
	})
	require.NoError(t, err)
	lp.chargeMeter = newChargeMeter(measured)
	assert.False(t, lp.powerEstimated())
}

// TestStagesPvHysteresis pins the hook in pvMaxCurrent: a surplus hovering around
// a stage boundary does not toggle the stages, they go only after the grid
// import has stayed at the disable threshold for the disable delay
func TestStagesPvHysteresis(t *testing.T) {
	lp, c, _ := newStagesLoadpoint(t, "stageshysteresis")
	lp.circuit = nil
	lp.Disable.Delay = 3 * time.Minute
	clk := lp.clock.(*clock.Mock)

	// all three stages run
	require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))
	require.NoError(t, c.Enable(true))
	lp.enabled = true

	heater := func() float64 {
		p, err := c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		return p
	}
	require.Equal(t, 9000.0, heater())

	// one cycle at the given grid power, the heater draws what it is switched to
	cycle := func(site float64) float64 {
		lp.chargePower = heater()
		current := lp.pvMaxCurrent(site, 0, false, false)
		require.NoError(t, lp.setLimit(current))
		return heater()
	}

	// the surplus hovers around the third stage for 20 minutes: 500 W import, 500 W export
	for i := range 40 {
		site := -500.0
		if i%2 == 0 {
			site = 500
		}
		assert.Equal(t, 9000.0, cycle(site), "cycle %d", i)
		clk.Add(30 * time.Second)
	}

	// a shortfall that stays: the stage goes with the disable delay, not before
	for range 5 {
		assert.Equal(t, 9000.0, cycle(500))
		clk.Add(30 * time.Second)
	}
	assert.Equal(t, 9000.0, cycle(500), "2.5 min")
	clk.Add(30 * time.Second)
	assert.Equal(t, 6000.0, cycle(500), "3 min: one stage down, not more")

	// stays there, the surplus covers two stages
	for range 5 {
		clk.Add(30 * time.Second)
		assert.Equal(t, 6000.0, cycle(500))
	}
}

func TestStagesPvHysteresisThreshold(t *testing.T) {
	// a grid import below the disable threshold is tolerated, the stages stay
	lp, c, _ := newStagesLoadpoint(t, "stageshysteresisthreshold")
	lp.circuit = nil
	lp.Disable.Delay = time.Minute
	lp.Disable.Threshold = 600
	clk := lp.clock.(*clock.Mock)

	require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))
	require.NoError(t, c.Enable(true))
	lp.enabled = true

	cycle := func(site float64) float64 {
		p, err := c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		lp.chargePower = p

		require.NoError(t, lp.setLimit(lp.pvMaxCurrent(site, 0, false, false)))

		p, err = c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		return p
	}

	for range 10 {
		assert.Equal(t, 9000.0, cycle(500), "tolerated")
		clk.Add(30 * time.Second)
	}

	assert.Equal(t, 9000.0, cycle(700), "at the threshold: the delay starts")
	clk.Add(time.Minute)
	assert.Equal(t, 6000.0, cycle(700))
}

// TestStagesPvDisableHold pins the hook in front of the disable branch of
// pvMaxCurrent: a surplus hovering between three stages and below the first one
// does not drop the heater to one stage, it goes there once the grid import has
// stayed at the disable threshold for the disable delay, and off after evcc's
// disable timer on top, which decides with the import at one stage
func TestStagesPvDisableHold(t *testing.T) {
	lp, c, _ := newStagesLoadpoint(t, "stagesdisablehold")
	lp.circuit = nil
	lp.Disable.Delay = 3 * time.Minute
	clk := lp.clock.(*clock.Mock)

	require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))
	require.NoError(t, c.Enable(true))
	lp.enabled = true

	cycle := func(site float64) float64 {
		p, err := c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		lp.chargePower = p

		require.NoError(t, lp.setLimit(lp.pvMaxCurrent(site, 0, false, false)))

		p, err = c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		return p
	}
	require.Equal(t, 9000.0, cycle(-1000))

	// 20 minutes: 1 kW export, then a cloud with 7 kW import, the heater runs on
	// all three stages instead of falling to one and climbing back
	for i := range 40 {
		site := -1000.0
		if i%2 == 0 {
			site = 7000
		}
		assert.Equal(t, 9000.0, cycle(site), "cycle %d", i)
		clk.Add(30 * time.Second)
	}

	// the cloud stays: all stages run through the disable delay, then one stage
	for range 5 {
		assert.Equal(t, 9000.0, cycle(7000))
		clk.Add(30 * time.Second)
	}
	assert.Equal(t, 9000.0, cycle(7000), "2.5 min")
	clk.Add(30 * time.Second)
	assert.Equal(t, 3000.0, cycle(7000), "3 min: one stage")
	assert.True(t, lp.enabled)

	// the import stays at one stage: off after evcc's disable delay, not held again
	for range 5 {
		clk.Add(30 * time.Second)
		assert.Equal(t, 3000.0, cycle(7000))
	}
	clk.Add(30 * time.Second)
	assert.Equal(t, 0.0, cycle(7000), "3 min later: off")
	assert.False(t, lp.enabled)
}

// TestStagesPvDisableHoldPartialSolar pins that evcc decides with the grid import
// at one stage: with a solar share below 1 or a disable threshold of its own, an
// import that one stage may cause does not switch the heater off
func TestStagesPvDisableHoldPartialSolar(t *testing.T) {
	lp, c, _ := newStagesLoadpoint(t, "stagesdisableholdpartial")
	lp.circuit = nil
	lp.Disable.Delay = 3 * time.Minute
	lp.solarShare = 0.5 // up to 1.5 kW import at one stage
	clk := lp.clock.(*clock.Mock)

	require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))
	require.NoError(t, c.Enable(true))
	lp.enabled = true

	cycle := func(site float64) float64 {
		p, err := c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		lp.chargePower = p

		require.NoError(t, lp.setLimit(lp.pvMaxCurrent(site, 0, false, false)))

		p, err = c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		return p
	}
	require.Equal(t, 9000.0, cycle(-1000))

	// 2 kW surplus: seven kW import at three stages, one kW at one stage
	for range 6 {
		assert.Equal(t, 9000.0, cycle(7000))
		clk.Add(30 * time.Second)
	}
	assert.Equal(t, 3000.0, cycle(7000), "3 min: one stage")

	// one kW import is within the 1.5 kW the solar share allows: it runs on
	for range 40 {
		clk.Add(30 * time.Second)
		assert.Equal(t, 3000.0, cycle(1000))
	}
	assert.True(t, lp.enabled)

	// more import than that: off after the disable delay
	for range 6 {
		clk.Add(30 * time.Second)
		assert.Equal(t, 3000.0, cycle(2000))
	}
	clk.Add(30 * time.Second)
	assert.Equal(t, 0.0, cycle(2000), "3 min: off")
	assert.False(t, lp.enabled)
}

func TestStagesPvDisableHoldThreshold(t *testing.T) {
	// a grid import below the disable threshold is tolerated, the stages stay
	lp, c, _ := newStagesLoadpoint(t, "stagesdisableholdthreshold")
	lp.circuit = nil
	lp.Disable.Delay = time.Minute
	lp.Disable.Threshold = 8000
	clk := lp.clock.(*clock.Mock)

	require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))
	require.NoError(t, c.Enable(true))
	lp.enabled = true

	cycle := func(site float64) float64 {
		p, err := c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		lp.chargePower = p

		require.NoError(t, lp.setLimit(lp.pvMaxCurrent(site, 0, false, false)))

		p, err = c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		return p
	}

	for range 10 {
		assert.Equal(t, 9000.0, cycle(7000), "tolerated")
		clk.Add(30 * time.Second)
	}

	assert.Equal(t, 9000.0, cycle(8100), "at the threshold: the delay starts")
	clk.Add(time.Minute)
	assert.Equal(t, 3000.0, cycle(8100), "after the delay: one stage")
	clk.Add(time.Minute)
	assert.Equal(t, 0.0, cycle(8100), "after the delay again: off")
}

func TestStagesPvHysteresisOtherChargers(t *testing.T) {
	// only a heater in stages is held
	lp := NewLoadpoint(util.NewLogger("lp"), nil)
	lp.clock = clock.NewMock()
	lp.enabled = true
	lp.charger = &fakeCharger{}

	assert.Equal(t, 5.0, lp.holdStage(5, 1000, 0, 6))
}

// stagesLoad stands in for a load on the circuit
type stagesLoad struct {
	circuit        api.Circuit
	power, current float64
}

func (l *stagesLoad) GetChargePower() float64     { return l.power }
func (l *stagesLoad) GetMaxPhaseCurrent() float64 { return l.current }
func (l *stagesLoad) GetCircuit() api.Circuit     { return l.circuit }

// TestStagesPhaseCurrent pins what a heater with one stage per phase draws on a
// phase: its stage current in full, not the mean over the phases. A stage then
// is not switched on into a circuit that has no room for it on a phase.
func TestStagesPhaseCurrent(t *testing.T) {
	lp, c, _ := newStagesLoadpoint(t, "stagesphasecurrent")

	ci, err := circuit.New(util.NewLogger("test"), "heating", 16, 0, nil, 0)
	require.NoError(t, err)
	lp.circuit = ci
	lp.chargeMeter = newChargeMeter(c)

	foreign := &stagesLoad{circuit: ci, power: 2300, current: 10}

	// the heater reports what it draws as the charge currents
	update := func() {
		t.Helper()

		i1, i2, i3, err := c.(api.PhaseCurrents).Currents()
		require.NoError(t, err)
		lp.chargeCurrents = []float64{i1, i2, i3}

		p, err := c.(api.Meter).CurrentPower()
		require.NoError(t, err)
		lp.chargePower = p

		require.NoError(t, ci.Update([]api.CircuitLoad{foreign, lp}))
	}

	// 10A elsewhere on 16A: a stage draws 13A on a phase and does not fit,
	// the mean current of 4.3A offered for one stage would have
	update()
	require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))
	p, err := c.(api.Meter).CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 0.0, p, "no stage")
	assert.False(t, lp.enabled)

	// 2A elsewhere: room for a stage on a phase, all three run
	foreign.current = 2
	update()
	require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))
	update()

	p, err = c.(api.Meter).CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 9000.0, p)
	assert.True(t, lp.enabled)
	assert.InDelta(t, 13.04, lp.GetMaxPhaseCurrent(), 0.01, "three stages: 13A on each phase")
	assert.InDelta(t, 15.04, ci.GetMaxPhaseCurrent(), 0.01)

	// the running stages are counted on the phase: 5A more elsewhere leaves
	// no room, and fewer stages would not draw less on a phase
	foreign.current = 7
	update()
	require.NoError(t, lp.setLimit(lp.effectiveMaxCurrent()))
	p, err = c.(api.Meter).CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 0.0, p, "off")
}

// TestStagesPhaseCurrentKeepsPhases pins that the phase currents do not make the
// loadpoint take the stages that are on for its active phases: the conversion of
// power and current of the whole heater would be on one phase
func TestStagesPhaseCurrentKeepsPhases(t *testing.T) {
	lp, c, _ := newStagesLoadpoint(t, "stagesphasecurrentphases")
	lp.circuit = nil
	lp.chargeMeter = newChargeMeter(c)

	require.NoError(t, lp.setLimit(lp.effectiveMinCurrent()))
	require.NoError(t, c.Enable(true))
	lp.enabled = true
	lp.status = api.StatusC

	i1, i2, i3, err := c.(api.PhaseCurrents).Currents()
	require.NoError(t, err)
	lp.chargeCurrents = []float64{i1, i2, i3}
	lp.phasesFromChargeCurrents()

	p, err := c.(api.Meter).CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 3000.0, p, "one stage")
	assert.Equal(t, 3, lp.ActivePhases())
	assert.InDelta(t, 3000.0/690, lp.effectiveMinCurrent(), 1e-9)
	assert.InDelta(t, 9000.0/690, lp.effectiveMaxCurrent(), 1e-9)
}
