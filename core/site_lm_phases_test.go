package core

import (
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// childCircuit puts a parent above a circuit
type childCircuit struct {
	api.Circuit
	parent api.Circuit
}

func (c childCircuit) GetParent() api.Circuit { return c.parent }

// Without the battery's phases, grid charging stays off on a circuit with a
// current limit, on the circuit or above it. They are not needed for a circuit
// with a power limit only.
func TestBatteryPhasesRequired(t *testing.T) {
	site, main, sub := lmSwitchCircuits(t, 3500)
	p := site.peak()
	p.chargePower = 2500

	assign := func(c api.Circuit, name string) {
		p.circuit = name
		lms := site.lms()
		lms.batteryCircuitRef = name
		lms.batteryCircuit = c
		lms.adv.Phases = nil
	}

	// main: 32 A and 10 kW, no phases set
	assign(main, "main")
	assert.True(t, site.batteryPhasesMissing())
	assert.False(t, site.batteryCircuitAllows())
	assert.Zero(t, site.batteryChargeSetpoint())

	// set, the 2.5 kW fit
	require.NoError(t, site.SetLmAdvanced("phases", 1))
	assert.False(t, site.batteryPhasesMissing())
	assert.True(t, site.batteryCircuitAllows())
	assert.Equal(t, 2500.0, site.batteryChargeSetpoint())

	// garage: a power limit only, nothing needed
	assign(sub, "garage")
	assert.False(t, site.batteryPhasesMissing())
	assert.True(t, site.batteryCircuitAllows())

	// garage below main with its current limit: needed again
	assign(childCircuit{Circuit: sub, parent: main}, "garage")
	assert.True(t, site.batteryPhasesMissing())
	assert.False(t, site.batteryCircuitAllows())
	require.NoError(t, site.SetLmAdvanced("phases", 3))
	assert.True(t, site.batteryCircuitAllows())

	// no circuit: not part of load management
	assign(nil, "")
	assert.False(t, site.batteryPhasesMissing())
}

// A restart with a battery on a circuit with a current limit and no phases set
// publishes phasesMissing: the circuit is back only after the load management
// settings, which publish first.
func TestBatteryPhasesMissingPublishedAfterRestore(t *testing.T) {
	a, _, _ := lmSwitchCircuits(t, 3500)
	_ = settings.Delete(keys.LmAdvanced)
	settings.SetString(keys.PeakShavingCircuit, "main")
	useLogFileDir(t)

	ch := make(chan util.Param, 1000)
	b := &Site{log: a.log, batteryMeters: a.batteryMeters, valueChan: ch}
	b.restoreCustom()

	var state lmAdvancedState
	for len(ch) > 0 {
		if p := <-ch; p.Key == keys.LmAdvanced {
			state = p.Val.(lmAdvancedState)
		}
	}
	assert.True(t, state.PhasesMissing)
}

// The optimizer is told that grid charging is refused without the phases on a
// circuit with a current limit and on a circuit that is gone.
func TestLmGridChargeBlockedPhasesOrphan(t *testing.T) {
	site, main, _ := lmSwitchCircuits(t, 3500)
	p := site.peak()
	p.chargePower = 2500
	p.circuit = "main"
	lms := site.lms()
	lms.batteryCircuitRef, lms.batteryCircuit = "main", main

	assert.True(t, site.lmGridChargeBlocked(), "32 A circuit without phases")

	require.NoError(t, site.SetLmAdvanced("phases", 3))
	assert.False(t, site.lmGridChargeBlocked())

	p.circuit = "gone"
	assert.True(t, site.lmGridChargeBlocked(), "circuit not resolvable")
}
