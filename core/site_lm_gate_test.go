package core

// The battery's grid charging on the circuit is evcc's check
// (batteryChargeExceedsCircuit); the fork feeds it the power it expects and
// shows the outcome in the overview and to the optimizer.

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

// TestLmGridChargePowerInput pins the contract of the hook in
// batteryMaxChargePower: evcc's check on the circuit uses the power entered in
// the ui, or this cycle's setpoint of a battery with a charge power entity, and
// its own value (the meters' limit) without either.
func TestLmGridChargePowerInput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ui         float64 // expected grid charge power entered in the ui
		controlled bool    // charge power entity
		setpoint   float64 // this cycle's setpoint
		meter      float64 // the battery meter's charge limit
		want       float64 // what evcc validates against the circuit
	}{
		{"ui value instead of the meter limit", 3000, false, 0, 5000, 3000},
		{"setpoint of a controlled battery", 3000, true, 2500, 5000, 2500},
		{"nothing set: evcc's value", 0, false, 0, 5000, 5000},
		{"controlled without a setpoint: evcc's value", 0, true, 0, 5000, 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			batCon := batteryControllerMock(ctrl)
			bat := &struct {
				api.Meter
				api.BatteryController
				api.BatteryPowerLimiter
			}{BatteryController: batCon, BatteryPowerLimiter: &mockBatteryPowerLimiter{charge: tc.meter}}

			circuit := api.NewMockCircuit(ctrl)
			circuit.EXPECT().GetMaxPower().Return(10000.0).AnyTimes()
			circuit.EXPECT().GetChargePower().Return(0.0).AnyTimes()
			// evcc checks exactly the power the fork passed on
			circuit.EXPECT().ValidatePower(0.0, tc.want).Return(tc.want).Times(1)
			batCon.EXPECT().SetBatteryMode(api.BatteryCharge).Times(1)

			site := &Site{
				log:           util.NewLogger("foo"),
				batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{}, api.Meter(bat))},
				circuit:       circuit,
				siteState:     siteState{battery: types.BatteryState{}},
			}

			p := site.peak()
			p.chargePower = tc.ui
			if tc.controlled {
				p.chargeSet = func(float64) error { return nil }
				p.chargeSetpoint = tc.setpoint
			}

			assert.Equal(t, tc.want, site.batteryMaxChargePower())

			site.updateBatteryMode(true, false, api.Rate{})
			assert.Equal(t, api.BatteryCharge, site.GetBatteryMode())

			ctrl.Finish()
		})
	}
}

// TestScenarioGateByPower: on/off charging with the power from the ui against
// the room on the circuit, as evcc checks it
func TestScenarioGateByPower(t *testing.T) {
	for _, tc := range []struct {
		name string
		room float64
		want api.BatteryMode
	}{
		{"3 kW room for 5 kW: held", 3000, api.BatteryHold},
		{"6 kW room for 5 kW: charges", 6000, api.BatteryCharge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newScenario(t)
			sc.site.peak().chargePower = 5000
			sc.withCircuit(10000, &scenarioLoad{title: "wallbox", power: 10000 - tc.room}, 0)

			assert.True(t, sc.cycle(20, 3000, 0), "requested")
			assert.Equal(t, tc.want, sc.mode())
		})
	}
}

// TestScenarioControlledPassesGate: the setpoint is cut to the room, so evcc's
// check on it passes. A wallbox asking for more puts the circuit over its limit:
// evcc holds, and the next cycle starts again with the smaller setpoint.
func TestScenarioControlledPassesGate(t *testing.T) {
	sc := newScenario(t)
	sc.withDynamicCharge()
	wallbox := &scenarioLoad{title: "wallbox", power: 7000}
	sc.withCircuit(10000, wallbox, 0)
	sc.site.peak().enabled = false

	assert.True(t, sc.cycle(20, 3000, 0))
	assert.Equal(t, 3000.0, val(sc.charge))
	assert.Equal(t, api.BatteryCharge, sc.mode())

	// the wallbox takes 1 kW more: over the limit, held; the battery stands below it
	wallbox.power = 8000
	assert.True(t, sc.cycle(20, 11000, -3000))
	assert.Equal(t, api.BatteryHold, sc.mode())
	assert.True(t, sc.site.lmGridChargeDenied(true))

	// the setpoint shrinks to what is left, evcc's check on it passes
	assert.True(t, sc.cycle(20, 8000, 0))
	assert.Equal(t, 2000.0, val(sc.charge))
	assert.Equal(t, api.BatteryCharge, sc.mode())
}

// TestScenarioMeterlessCircuitCountsBattery: a circuit without meter counts the
// battery's grid charging, so evcc's check stops it on overload
func TestScenarioMeterlessCircuitCountsBattery(t *testing.T) {
	sc := newScenario(t)
	wallbox := &scenarioLoad{title: "wallbox", power: 3000}
	sc.withCircuit(10000, wallbox, 0)

	assert.True(t, sc.cycle(20, 3000, 0))
	assert.Equal(t, api.BatteryCharge, sc.mode())

	// the wallbox starts: 7000 + 4000 > 10000
	wallbox.power = 7000
	assert.True(t, sc.cycle(21, 3000, -4000))
	assert.Equal(t, api.BatteryHold, sc.mode())
}

// TestLmGridChargeDeniedState: held by the circuit check shows as shed without a
// time, is entered in the event log once, and blocks grid charging for the
// optimizer; not while the hems dimmed, the api set the mode or there is no circuit
func TestLmGridChargeDeniedState(t *testing.T) {
	ctrl := gomock.NewController(t)
	hems := api.NewMockHEMS(ctrl)
	var dimmed *float64
	hems.EXPECT().MaxConsumptionPower().DoAndReturn(func() *float64 { return dimmed }).AnyTimes()

	site := &Site{
		log:           util.NewLogger("test"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(&scenarioBattery{}))},
		circuit:       api.NewMockCircuit(ctrl),
		batteryMode:   api.BatteryHold,
	}
	site.peak().chargePower = 4000

	now := time.Now()

	st := site.lmBatteryStatus(now, true)
	assert.Equal(t, lmStateShed, st.State)
	assert.Nil(t, st.Until)

	site.lmBatteryStatus(now, true) // still held: no second event
	events := site.lmm().Events()
	if assert.Len(t, events, 1) {
		assert.Equal(t, lm.EventGridChargeDenied, events[0].Type)
		assert.Equal(t, 0.0, events[0].A)
		assert.Equal(t, 4000.0, events[0].B)
	}

	// the optimizer reads the requested state of the last cycle
	site.lms().gridCharge = true
	assert.True(t, site.lmGridChargeBlocked())
	site.lms().gridCharge = false
	assert.False(t, site.lmGridChargeBlocked(), "not requested")
	site.lms().gridCharge = true

	// released and held again: a new event
	site.batteryMode = api.BatteryCharge
	assert.Equal(t, lmStateRunning, site.lmBatteryStatus(now, true).State)
	site.batteryMode = api.BatteryHold
	site.lmBatteryStatus(now, true)
	assert.Len(t, site.lmm().Events(), 2)

	// the hems holds it, not the circuit
	site.hems = hems
	dimmed = new(1000.0)
	assert.NotEqual(t, lmStateShed, site.lmBatteryStatus(now, true).State)
	assert.False(t, site.lmGridChargeBlocked())
	site.hems = nil

	// set from outside
	site.batteryModeExternal = api.BatteryHold
	assert.False(t, site.lmGridChargeBlocked())
	site.batteryModeExternal = api.BatteryUnknown

	// without a circuit nothing is held by it
	site.circuit = nil
	assert.False(t, site.lmGridChargeBlocked())
	assert.NotEqual(t, lmStateShed, site.lmBatteryStatus(now, true).State)
}

// TestBatteryOnRootCircuit: with circuits the battery always counts on the
// site's root circuit, without nothing is managed
func TestBatteryOnRootCircuit(t *testing.T) {
	ctrl := gomock.NewController(t)
	site := &Site{log: util.NewLogger("test")}

	assert.Nil(t, site.lmBatteryCircuit())
	assert.Empty(t, site.circuitLoads(), "no circuit, no battery load")

	site.circuit = api.NewMockCircuit(ctrl)
	assert.Equal(t, site.circuit, site.lmBatteryCircuit())
	assert.Equal(t, site.circuit, site.lmBattery().GetCircuit())

	loads := site.circuitLoads()
	if assert.Len(t, loads, 1) {
		assert.Equal(t, site.lmBattery(), loads[0])
	}
}
