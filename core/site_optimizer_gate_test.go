package core

import (
	"github.com/benbjohnson/clock"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// gateScenario is newScenario with the optimizer in automatic mode and a battery
// suggestion; an empty action is a missing result. The peak limit is 5 kW with a
// 30 % reserve, soc grid charging goes from 20 to 80 %, the grid charge power is
// 6250 W.
func gateScenario(t *testing.T, action string) *scenario {
	t.Helper()

	sc := newScenario(t)
	holdOptimizer(t, sc.site)
	enableAutomatic(t)
	if action != "" {
		sc.suggest(action)
	}
	return sc
}

// suggest replaces the optimizer's battery suggestion
func (sc *scenario) suggest(action string) {
	sc.site.setSuggestions(map[string]types.Suggestion{batteryKey("bat"): {Action: action}})
}

// applied returns the modes the battery was set to
func (sc *scenario) applied() []api.BatteryMode {
	return sc.site.batteryMeters[0].Instance().(*scenarioBattery).modes
}

// Outside automatic mode the gate passes upstream's decision unchanged, whatever
// the mode and the fork's grid charge flag are.
func TestLmGateInertWithoutAutomatic(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	for _, m := range []api.BatteryMode{api.BatteryUnknown, api.BatteryNormal, api.BatteryHold, api.BatteryCharge} {
		for _, gridCharge := range []bool{false, true} {
			assert.Equal(t, m, site.lmGateBatteryMode(m, gridCharge), "%s, grid charge %v", m, gridCharge)
		}
	}

	assert.False(t, site.optimizerInControl())
	assert.False(t, site.optimizerCharges())
	assert.False(t, site.optimizerHolds())
}

// A mode set from outside goes before the optimizer, the gate leaves it alone.
func TestLmGateExternalModeFirst(t *testing.T) {
	sc := gateScenario(t, api.BatteryCharge.String())
	site := sc.site

	require.True(t, site.optimizerInControl())

	site.batteryModeExternal = api.BatteryHold
	assert.False(t, site.optimizerInControl())
	for _, m := range []api.BatteryMode{api.BatteryUnknown, api.BatteryNormal, api.BatteryHold, api.BatteryCharge} {
		assert.Equal(t, m, site.lmGateBatteryMode(m, true), m.String())
	}
}

// The optimizer's charge request passes the gate: the charge power setpoint is
// written, and for the battery type Marstek it is what Omnibattery is told.
func TestLmGateOptimizerCharge(t *testing.T) {
	sc := gateScenario(t, api.BatteryCharge.String())
	sc.withDynamicCharge()
	sc.site.peak().batteryType = batteryTypeMarstek

	// soc 50 %: the fork's own soc grid charging would not start
	assert.False(t, sc.cycle(50, 3000, 0), "the fork's own grid charge flag does not decide")

	assert.Equal(t, api.BatteryCharge, sc.mode())
	assert.Equal(t, []api.BatteryMode{api.BatteryCharge}, sc.applied())
	// the full grid charge power: peak shaving of this type is not set up here, so
	// nothing trims it to the room below the peak limit
	assert.Equal(t, 6250.0, sc.site.peak().chargeSetpoint, "setpoint")

	charge, power := sc.site.omniWant()
	assert.True(t, charge, "Omnibattery is told to charge")
	assert.Equal(t, 6250.0, power)
}

// A charge request is refused during a running peak and without room on the
// circuit, the setpoint is 0. During the peak the battery covers it (normal),
// without room on the circuit it holds.
func TestLmGateOptimizerChargeRefused(t *testing.T) {
	t.Run("peak", func(t *testing.T) {
		sc := gateScenario(t, api.BatteryCharge.String())
		sc.withDynamicCharge()

		sc.cycle(50, 9000, 0)

		assert.Equal(t, api.BatteryNormal, sc.mode(), "the battery covers the peak")
		assert.NotContains(t, sc.applied(), api.BatteryHold)
		assert.Equal(t, 0.0, val(sc.charge), "no setpoint")
		charge, _ := sc.site.omniWant()
		assert.False(t, charge)
	})

	t.Run("circuit full", func(t *testing.T) {
		sc := gateScenario(t, api.BatteryCharge.String())
		sc.withCircuit(12000, &scenarioLoad{title: "wallbox", power: 7000}, 0)

		sc.cycle(50, 3000, 0) // 7000 + 6250 > 12000

		assert.Equal(t, api.BatteryHold, sc.mode())
		assert.Equal(t, []api.BatteryMode{api.BatteryHold}, sc.applied())
	})

	t.Run("circuit full, setpoint", func(t *testing.T) {
		sc := gateScenario(t, api.BatteryCharge.String())
		sc.withCircuit(7400, &scenarioLoad{title: "wallbox", power: 7000}, 0)
		sc.withDynamicCharge()

		sc.cycle(50, 3000, 0)

		assert.Equal(t, api.BatteryHold, sc.mode())
		assert.Less(t, val(sc.charge), 500.0, "less than the minimum worth charging with")
	})

	t.Run("allowed again", func(t *testing.T) {
		sc := gateScenario(t, api.BatteryCharge.String())

		sc.cycle(50, 9000, 0)
		require.Equal(t, api.BatteryNormal, sc.mode())

		sc.clearHoldOffs()
		sc.cycle(50, 3000, 0)
		assert.Equal(t, api.BatteryCharge, sc.mode())
	})
}

// Hold gives way to normal only while a peak has to be covered. The peak setpoint
// then covers the peak and not more, it is not the free value.
func TestLmGateOptimizerHold(t *testing.T) {
	sc := gateScenario(t, api.BatteryHold.String())
	sc.withDynamicCharge()

	assert.False(t, sc.cycle(50, 3000, 0), "no peak")
	assert.Equal(t, api.BatteryHold, sc.mode())
	assert.Equal(t, 0.0, val(sc.peak), "only a peak is covered")
	assert.True(t, sc.site.optimizerHolds())

	assert.False(t, sc.cycle(50, 8000, 0), "peak")
	assert.Equal(t, api.BatteryNormal, sc.mode(), "the battery covers the peak")
	assert.Equal(t, 3000.0, val(sc.peak), "the peak setpoint")

	// a demand around the limit does not switch the mode every cycle
	assert.False(t, sc.cycle(50, 3000, 0), "peak over, within the hold-off")
	assert.Equal(t, api.BatteryNormal, sc.mode())
	assert.Equal(t, 0.0, val(sc.peak))

	sc.clearHoldOffs()
	assert.False(t, sc.cycle(50, 3000, 0), "peak over")
	assert.Equal(t, api.BatteryHold, sc.mode())

	assert.Equal(t, []api.BatteryMode{api.BatteryHold, api.BatteryNormal, api.BatteryHold}, sc.applied())
}

// Hold without peak shaving: the gate has no peak to give way to.
func TestLmGateOptimizerHoldWithoutPeakShaving(t *testing.T) {
	site := automaticSite(t, api.BatteryHold.String())

	assert.Equal(t, api.BatteryHold, site.lmGateBatteryMode(api.BatteryHold, false))

	setPeakShaving(site, 7000, 30)
	s := site.peak()
	s.demand, s.window.Allowed = 6000, 7000
	assert.Equal(t, api.BatteryHold, site.lmGateBatteryMode(api.BatteryHold, false), "no peak")

	s.demand = 9000
	assert.Equal(t, api.BatteryNormal, site.lmGateBatteryMode(api.BatteryHold, false), "covering a peak")
}

// holdOptimizer keeps the optimizer from running for the duration of the test:
// with automatic mode on, a setter calling Optimize would otherwise start a real
// run in the background that reads and changes the site while the test does
func holdOptimizer(t *testing.T, site *Site) {
	t.Helper()
	site.optimizerMu.Lock()
	t.Cleanup(site.optimizerMu.Unlock)
}

func automaticSite(t *testing.T, action string) *Site {
	t.Helper()

	site := &Site{
		log:           util.NewLogger("test"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(&scenarioBattery{}))},
	}
	holdOptimizer(t, site)
	enableAutomatic(t)

	if action != "" {
		site.setSuggestions(map[string]types.Suggestion{batteryKey("bat"): {Action: action}})
	}
	return site
}

// Releasing the grid charge sets the charge power setpoint to 0.
func TestLmGateReleaseGridCharge(t *testing.T) {
	sc := gateScenario(t, api.BatteryCharge.String())
	sc.withDynamicCharge()

	sc.cycle(50, 3000, 0)
	require.Equal(t, 2000.0, val(sc.charge))

	sc.suggest(api.BatteryNormal.String())
	sc.cycle(50, 3000, 0)
	assert.Equal(t, 0.0, val(sc.charge))
	assert.Equal(t, 0.0, sc.site.peak().chargeSetpoint)

	sc.suggest(api.BatteryCharge.String())
	sc.cycle(50, 3000, 0)
	require.Equal(t, 2000.0, val(sc.charge))

	sc.suggest(api.BatteryHold.String())
	sc.cycle(50, 3000, 0)
	assert.Equal(t, 0.0, val(sc.charge))
}

// While the optimizer is in control the fork's own grid charging neither charges
// nor reserves circuit power: the soc switch would start at 20 %, but the
// optimizer holds.
func TestLmGateNoOwnGridCharge(t *testing.T) {
	sc := gateScenario(t, api.BatteryHold.String())
	wallbox := &scenarioLoad{title: "wallbox", prio: 1, power: 7000}
	sc.withCircuit(12000, wallbox, 5) // the battery outranks the wallbox
	sc.withDynamicCharge()
	sc.site.peak().reserve = 10 // the start soc is above the reserve

	assert.False(t, sc.cycle(20, 3000, 0), "start soc reached")
	assert.True(t, sc.site.batterySocChargeActive(), "the hysteresis keeps tracking")
	assert.False(t, sc.site.batteryGridChargeRequested(api.Rate{}))
	assert.Equal(t, api.BatteryHold, sc.mode())

	// the wallbox keeps its power: nothing is reserved for the battery
	assert.Equal(t, 7000.0, sc.site.lmm().ValidatePower(wallbox, sc.circuit, 7000, 7000))
	assert.Equal(t, 0.0, val(sc.charge))
}

// Without a current result the fork's soc grid charging applies as without the
// optimizer.
func TestLmGateOptimizerStale(t *testing.T) {
	sc := gateScenario(t, "")
	require.False(t, sc.site.optimizerInControl())

	assert.False(t, sc.cycle(50, 3000, 0), "above the start soc")
	assert.Empty(t, sc.applied(), "evcc's decision: nothing to change")

	assert.True(t, sc.cycle(20, 3000, 0), "start soc reached")
	assert.Equal(t, api.BatteryCharge, sc.mode())
	assert.Equal(t, []api.BatteryMode{api.BatteryCharge}, sc.applied())

	// the optimizer is back: it decides
	sc.suggest(api.BatteryHold.String())
	assert.False(t, sc.cycle(50, 3000, 0))
	assert.Equal(t, api.BatteryHold, sc.mode())
}

// After a restart there is no optimizer result until the first solve: the soc
// grid charging, restored from the settings, charges.
func TestLmGateRestartWithoutResult(t *testing.T) {
	noSettingsDB(t)
	freshSettings(t)
	enableAutomatic(t)
	useLogFileDir(t)

	settings.SetBool(keys.BatterySocGridCharge, true)
	settings.SetFloat(keys.BatterySocGridChargeStart, 20)
	settings.SetFloat(keys.BatterySocGridChargeStop, 80)

	ref := newScenario(t).site
	site := &Site{log: ref.log, batteryMeters: ref.batteryMeters}
	site.restoreCustom()

	require.True(t, site.Automatic())
	require.False(t, site.optimizerInControl())

	setBatterySoc(site, 20)
	assert.True(t, site.batteryGridChargeRequested(api.Rate{}))
	assert.Equal(t, api.BatteryCharge, site.lmGateBatteryMode(api.BatteryCharge, true))
}

// A §14a dimming makes hold of the charge request: evcc checks after the gate.
func TestLmGateHemsDimmed(t *testing.T) {
	sc := gateScenario(t, api.BatteryCharge.String())

	ctrl := gomock.NewController(t)
	dimmed := 4200.0
	h := api.NewMockHEMS(ctrl)
	h.EXPECT().MaxConsumptionPower().Return(&dimmed).AnyTimes()
	h.EXPECT().CurtailedPercent().Return(nil).AnyTimes()
	sc.site.hems = h

	sc.cycle(50, 3000, 0)

	assert.Equal(t, api.BatteryHold, sc.mode())
	assert.Equal(t, []api.BatteryMode{api.BatteryHold}, sc.applied())
}

// With peak shaving running (below the reserve) the optimizer's charge request
// goes the way through updateBatteryMode instead of being forced to normal, and
// its hold leaves the battery in normal, the peak setpoint covers the peaks.
func TestLmGatePeakShavingActive(t *testing.T) {
	t.Run("charge", func(t *testing.T) {
		sc := gateScenario(t, api.BatteryCharge.String())
		sc.withDynamicCharge()

		sc.cycle(25, 3000, 0) // below the reserve of 30 %
		require.True(t, sc.site.peakShavingActive())

		assert.Equal(t, api.BatteryCharge, sc.mode())
		assert.Equal(t, 2000.0, val(sc.charge))
	})

	t.Run("charge, peak", func(t *testing.T) {
		sc := gateScenario(t, api.BatteryCharge.String())
		sc.withDynamicCharge()

		sc.cycle(25, 8000, 0)
		assert.Equal(t, api.BatteryNormal, sc.mode(), "refused, the battery covers the peak")
		assert.Equal(t, 0.0, val(sc.charge))
	})

	t.Run("hold", func(t *testing.T) {
		sc := gateScenario(t, api.BatteryHold.String())
		sc.withDynamicCharge()

		sc.cycle(25, 8000, 0)
		require.True(t, sc.site.peakShavingActive())

		assert.Equal(t, api.BatteryNormal, sc.mode(), "the reserve is held for peaks")
		assert.Equal(t, 3000.0, val(sc.peak))
		assert.Equal(t, 0.0, val(sc.charge))
	})
}

// Stage "battery": the loadpoints stay as they are, stage "full" controls them.
func TestLmGateLoadpointsByStage(t *testing.T) {
	enableAutomatic(t)

	ctrl := gomock.NewController(t)
	site := &Site{log: util.NewLogger("test")}
	lp := &Loadpoint{site: site, vehicle: modelledVehicle(ctrl), charger: &fakeCharger{}}

	settings.SetString(keys.OptimizerAutomatic, OptimizerAutomaticBattery)
	assert.True(t, site.Automatic())
	assert.False(t, lp.optimizerControlled(), "stage battery")

	settings.SetString(keys.OptimizerAutomatic, OptimizerAutomaticFull)
	assert.True(t, lp.optimizerControlled(), "stage full")

	lp.charger = &fakeCharger{features: []api.Feature{api.Heating}}
	assert.False(t, lp.optimizerControlled(), "a heater is never controlled")
}

// The optimizer's charging goes through setLimit, so the circuit limits it as
// any other charging.
func TestLmGateOptimizerChargingCircuit(t *testing.T) {
	enableAutomatic(t)

	m := &lmMeter{power: 5000} // 5 kW of 10 kW taken by the rest
	c, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, m, 0)
	require.NoError(t, err)
	require.NoError(t, c.Update(nil))

	charger := &fakeCharger{}
	lp := NewLoadpoint(util.NewLogger("lp"), nil)
	lp.clock = clock.NewMock()
	lp.wakeUpTimer = NewTimer()
	lp.circuit = c
	lp.charger = charger
	lp.phases = 3
	lp.minCurrent, lp.maxCurrent = 6, 16

	// 8 kW are 11.6 A on three phases, the circuit leaves 5 kW, 7.2 A
	ok, err := lp.optimizerCharging(&types.Suggestion{Action: actionCharge, Charge: 8000, Grid: 8000})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.LessOrEqual(t, charger.current, int64(7), "limited to what the circuit leaves")
	assert.GreaterOrEqual(t, charger.current, int64(6))
}

// Without meter values the demand is unknown: the optimizer's hold gives way, the
// battery covers.
func TestLmGatePeakNeedsBatteryMetersStale(t *testing.T) {
	sc := gateScenario(t, api.BatteryHold.String())
	sc.cycle(50, 3000, 0)
	require.False(t, sc.site.peakNeedsBattery())

	s := sc.site.peak()
	s.mu.Lock()
	s.updated = s.clock.Now().Add(-time.Hour)
	s.mu.Unlock()
	assert.True(t, sc.site.peakNeedsBattery())
}
