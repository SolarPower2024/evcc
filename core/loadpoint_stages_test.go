package core

import (
	"testing"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/charger"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStagesLoadpoint returns a 3-phase loadpoint driving a 3 x 3 kW heater on a
// 10 kW circuit, with switches kept in a js vm
func newStagesLoadpoint(t *testing.T, vm string) (*Loadpoint, api.Charger, *lmMeter) {
	t.Helper()
	Voltage = 230

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
		require.NoError(t, lp.setLimit(powerToCurrent(tc.surplus, 3)))
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
			wallbox.chargePower = currentToPower(float64(wb.current), 3)
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
