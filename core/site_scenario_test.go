package core

// Scenario tests for the custom load management, grid charging and peak
// shaving: each step runs one full site cycle (peak shaving, grid charge
// decision, battery mode) and checks what reaches the battery and the two
// Home Assistant entities.

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scenarioBattery is a controllable battery recording the modes it is set to
type scenarioBattery struct {
	modes []api.BatteryMode
}

func (b *scenarioBattery) CurrentPower() (float64, error) { return 0, nil }

func (b *scenarioBattery) BatteryModes() []api.BatteryMode {
	return []api.BatteryMode{api.BatteryNormal, api.BatteryHold, api.BatteryCharge, api.BatteryDischarge}
}

func (b *scenarioBattery) SetBatteryMode(m api.BatteryMode) error {
	b.modes = append(b.modes, m)
	return nil
}

// scenarioLoad is a circuit load such as a wallbox
type scenarioLoad struct {
	title   string
	prio    int
	power   float64
	circuit api.Circuit
}

func (l *scenarioLoad) GetTitle() string            { return l.title }
func (l *scenarioLoad) LmPriority() int             { return l.prio }
func (l *scenarioLoad) GetCircuit() api.Circuit     { return l.circuit }
func (l *scenarioLoad) GetChargePower() float64     { return l.power }
func (l *scenarioLoad) GetMaxPhaseCurrent() float64 { return 0 }

type scenario struct {
	t       *testing.T
	site    *Site
	circuit api.Circuit
	loads   []api.CircuitLoad
	peak    *float64 // last discharge setpoint written
	charge  *float64 // last charge power written
	rate    api.Rate
}

// newScenario sets up a site with a controllable battery, peak shaving on with
// 5 kW limit and 30 % reserve, soc grid charging on from 20 % to 80 %, and an
// expected grid charge power of 6250 W
func newScenario(t *testing.T) *scenario {
	t.Helper()
	lm.Reset()
	Voltage = 230

	sc := &scenario{t: t}

	site := &Site{
		log:           util.NewLogger("test"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(&scenarioBattery{}))},
	}
	sc.site = site

	lms := site.lms()
	lms.socChargeEnabled = true

	s := site.peak()
	s.enabled = true
	s.chargePower = 6250
	s.set = func(v float64) error { sc.peak = &v; return nil }

	lm.SetPriorityLookup(site.lmPriorityLookup)

	return sc
}

// withCircuit puts the battery and a wallbox on a meterless circuit
func (sc *scenario) withCircuit(maxPower float64, wallbox *scenarioLoad, batteryPrio int) {
	c, err := circuit.New(util.NewLogger("test"), "test", 0, maxPower, nil, 0)
	require.NoError(sc.t, err)

	sc.circuit = c
	wallbox.circuit = c
	sc.loads = []api.CircuitLoad{wallbox}

	sc.site.peak().circuit = "test"
	lms := sc.site.lms()
	lms.batteryCircuitRef = "test"
	lms.batteryCircuit = c
	lms.prios = map[string]int{lmBatteryName: batteryPrio}
}

// withDynamicCharge sets a charge power entity
func (sc *scenario) withDynamicCharge() {
	sc.site.peak().chargeSet = func(v float64) error { sc.charge = &v; return nil }
}

// cycle runs one site cycle and returns whether grid charging is active
func (sc *scenario) cycle(soc, grid, battery float64) bool {
	site := sc.site

	site.battery.Soc = soc
	site.battery.Power = battery
	site.gridPower = grid

	if sc.circuit != nil {
		require.NoError(sc.t, sc.circuit.Update(append(sc.loads, site.lmBattery())))
	}

	site.updatePeakShaving(site.state())
	charge := site.batteryGridChargeRequested(sc.rate)
	site.updateBatteryModePeakAware(charge, false, sc.rate)

	return charge
}

// clearHoldOffs lets time pass beyond both hold-offs
func (sc *scenario) clearHoldOffs() {
	sc.site.lms().batteryShedUntil = time.Time{}
	sc.site.peak().chargePause = time.Time{}
}

func (sc *scenario) mode() api.BatteryMode { return sc.site.GetBatteryMode() }

func val(f *float64) float64 {
	if f == nil {
		return -1
	}
	return *f
}

func TestScenarioDischargeSetpoint(t *testing.T) {
	for _, tc := range []struct {
		name          string
		soc, grid     float64
		battery       float64
		wantPeak      float64
		wantShaving   bool
		wantModeAfter api.BatteryMode
	}{
		{"above reserve: free", 50, 3000, 0, 10000, false, api.BatteryUnknown},
		{"above reserve, big demand: still free", 50, 12000, 0, 10000, false, api.BatteryUnknown},
		{"below reserve, no peak", 25, 3000, 0, 0, true, api.BatteryNormal},
		{"exactly at reserve counts as below", 30, 3000, 0, 0, true, api.BatteryNormal},
		{"below reserve, peak", 25, 8000, 0, 3000, true, api.BatteryNormal},
		{"below reserve, battery already covering", 25, 5000, 3000, 3000, true, api.BatteryNormal},
		{"below reserve, exporting", 25, -2000, 0, 0, true, api.BatteryNormal},
		{"below reserve, battery charging from pv", 25, 0, -2000, 0, true, api.BatteryNormal},
		{"below reserve, huge peak above free value", 25, 20000, 0, 15000, true, api.BatteryNormal},
		{"fractional meter values", 25, 6234.6, 0, 1235, true, api.BatteryNormal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newScenario(t)
			sc.site.lms().socChargeEnabled = false // discharge only

			assert.False(t, sc.cycle(tc.soc, tc.grid, tc.battery))
			assert.Equal(t, tc.wantPeak, val(sc.peak), "discharge setpoint")
			assert.Equal(t, tc.wantShaving, sc.site.peakShavingActive(), "shaving")
			assert.Equal(t, tc.wantModeAfter, sc.mode(), "battery mode")
		})
	}
}

func TestScenarioReserveHysteresis(t *testing.T) {
	sc := newScenario(t)
	sc.site.lms().socChargeEnabled = false

	steps := []struct {
		soc      float64
		shaving  bool
		wantPeak float64
	}{
		{31, false, 10000}, // starts above the reserve: free
		{30, true, 3000},   // drops to the reserve: shaving
		{31, true, 3000},   // inside the 2 % band: still shaving
		{31.9, true, 3000}, // still inside
		{32, false, 10000}, // band left: free again
		{31, false, 10000}, // back inside the band from above: stays free
	}

	for _, st := range steps {
		sc.cycle(st.soc, 8000, 0)
		assert.Equal(t, st.shaving, sc.site.peakShavingActive(), "soc %.1f", st.soc)
		assert.Equal(t, st.wantPeak, val(sc.peak), "soc %.1f", st.soc)
	}
}

func TestScenarioPeakShavingOff(t *testing.T) {
	sc := newScenario(t)
	sc.site.lms().socChargeEnabled = false

	// shaving with a setpoint in the entity ...
	sc.cycle(25, 8000, 0)
	assert.Equal(t, 3000.0, val(sc.peak))

	// ... switched off: control is handed back and the battery left alone
	sc.site.peak().enabled = false
	sc.cycle(25, 8000, 0)
	assert.Equal(t, 10000.0, val(sc.peak))
	assert.False(t, sc.site.peakShavingActive())
}

func TestScenarioSocGridChargeOnOff(t *testing.T) {
	sc := newScenario(t)

	// 21 %: above the start soc, not yet charging
	assert.False(t, sc.cycle(21, 3000, 0), "21%")

	// 20 %: start soc reached, charges although below the reserve
	assert.True(t, sc.cycle(20, 3000, 0), "20%")
	assert.Equal(t, api.BatteryCharge, sc.mode())
	assert.Equal(t, 0.0, val(sc.peak), "no discharge while charging")

	// the charger itself pushes the grid over the peak limit: keeps charging,
	// the demand without the battery is what counts
	assert.True(t, sc.cycle(25, 1000+6250, -6250), "own charge power")

	// a real peak: charging pauses, the battery shaves
	assert.False(t, sc.cycle(25, 8000+6250, -6250), "peak")
	assert.Equal(t, api.BatteryNormal, sc.mode())
	assert.Equal(t, 3000.0, val(sc.peak))

	// peak over, hold-off still running
	assert.False(t, sc.cycle(25, 3000, 0), "hold-off")

	// hold-off over: charging resumes, the hysteresis kept running meanwhile
	sc.clearHoldOffs()
	assert.True(t, sc.cycle(26, 3000, 0), "resumed")
	assert.Equal(t, api.BatteryCharge, sc.mode())

	// above the reserve, still below the stop soc: charging goes on, and the
	// discharge entity says 0 instead of the free value while it does
	assert.True(t, sc.cycle(50, 3000, 0), "50%")
	assert.Equal(t, 0.0, val(sc.peak), "no discharge while charging above the reserve")

	// a peak above the reserve pauses too, the battery then runs free
	assert.False(t, sc.cycle(50, 8000, 0), "peak above reserve")
	assert.Equal(t, api.BatteryNormal, sc.mode())
	assert.Equal(t, 10000.0, val(sc.peak))

	sc.clearHoldOffs()
	assert.True(t, sc.cycle(79, 3000, 0), "79%")

	// stop soc reached
	assert.False(t, sc.cycle(80, 3000, 0), "80%")
	assert.Equal(t, api.BatteryNormal, sc.mode())

	// dropping back below the stop but above the start does not restart
	assert.False(t, sc.cycle(60, 3000, 0), "60% after stop")
}

func TestScenarioPriceGridCharge(t *testing.T) {
	sc := newScenario(t)
	sc.site.lms().socChargeEnabled = false

	limit := 0.20
	sc.site.batteryGridChargeLimit = &limit

	now := time.Now()
	sc.rate = api.Rate{Start: now.Add(-time.Hour), End: now.Add(time.Hour), Value: 0.15}
	assert.True(t, sc.cycle(50, 3000, 0), "cheap")
	assert.True(t, sc.cycle(50, 3000, -6250), "cheap, charging")
	assert.Equal(t, 0.0, val(sc.peak), "no discharge while charging")

	// the peak pause applies to price-based charging as well
	assert.False(t, sc.cycle(50, 9000, 0), "cheap but peak")

	sc.clearHoldOffs()
	sc.rate.Value = 0.25
	assert.False(t, sc.cycle(50, 3000, 0), "expensive")
}

func TestScenarioCircuitOnOff(t *testing.T) {
	t.Run("fits", func(t *testing.T) {
		sc := newScenario(t)
		sc.withCircuit(22000, &scenarioLoad{title: "wallbox", power: 7000}, 0)
		assert.True(t, sc.cycle(20, 3000, 0))
	})

	t.Run("does not fit, equal priority", func(t *testing.T) {
		sc := newScenario(t)
		wallbox := &scenarioLoad{title: "wallbox", power: 7000}
		sc.withCircuit(12000, wallbox, 0)

		// 7000 + 6250 > 12000: no charging, and the hold-off keeps it off
		assert.False(t, sc.cycle(20, 3000, 0))
		assert.False(t, sc.cycle(20, 3000, 0), "hold-off")

		// same priority: the wallbox keeps its power
		assert.Equal(t, 7000.0, lm.ValidatePower(wallbox, sc.circuit, 7000, 7000))
	})

	t.Run("does not fit, battery outranks wallbox", func(t *testing.T) {
		sc := newScenario(t)
		wallbox := &scenarioLoad{title: "wallbox", prio: 1, power: 7000}
		sc.withCircuit(12000, wallbox, 5)

		assert.False(t, sc.cycle(20, 3000, 0))

		// the wallbox gives way by what the battery is missing: 1250 W
		allowed := lm.ValidatePower(wallbox, sc.circuit, 7000, 7000)
		assert.Equal(t, 5750.0, allowed)
		wallbox.power = allowed

		// after the hold-off the battery fits
		sc.clearHoldOffs()
		assert.True(t, sc.cycle(20, 3000, 0))

		// charging stops at the stop soc and the wallbox gets everything back
		sc.site.battery.Power = 0
		assert.False(t, sc.cycle(80, 3000, 0))
		assert.Equal(t, 7000.0, lm.ValidatePower(wallbox, sc.circuit, 5750, 7000))
	})

	t.Run("already charging is not re-checked against itself", func(t *testing.T) {
		sc := newScenario(t)
		sc.withCircuit(13250, &scenarioLoad{title: "wallbox", power: 7000}, 0)

		// circuit exactly full with the battery drawing 6250
		assert.True(t, sc.cycle(20, 3000, -6250))
		assert.True(t, sc.cycle(21, 3000, -6250))
	})

	t.Run("unknown charge power blocks", func(t *testing.T) {
		sc := newScenario(t)
		sc.site.peak().chargePower = 0 // nothing entered, meter reports no limits
		sc.withCircuit(22000, &scenarioLoad{title: "wallbox", power: 0}, 0)
		assert.False(t, sc.cycle(20, 3000, 0))
	})

	t.Run("no circuit, unknown charge power does not block", func(t *testing.T) {
		sc := newScenario(t)
		sc.site.peak().chargePower = 0
		assert.True(t, sc.cycle(20, 3000, 0))
	})
}

func TestScenarioDynamicCharge(t *testing.T) {
	t.Run("sized below the peak limit", func(t *testing.T) {
		sc := newScenario(t)
		sc.withDynamicCharge()

		for _, st := range []struct {
			name         string
			demand       float64
			charging     float64 // current battery charge power
			want         float64
			wantCharging bool
		}{
			{"1 kW demand", 1000, 0, 4000, true},
			// the grid meter includes the charging, the setpoint must not chase it
			{"stable while charging", 1000, 4000, 4000, true},
			{"demand rises", 2500, 4000, 2500, true},
			{"low demand, capped at the charge power", -3000, 2500, 6250, true},
			{"less than the minimum left", 4600, 2500, 0, false},
		} {
			got := sc.cycle(20, st.demand+st.charging, -st.charging)
			assert.Equal(t, st.wantCharging, got, st.name)
			assert.Equal(t, st.want, val(sc.charge), st.name)
		}
	})

	t.Run("peak pauses and writes zero", func(t *testing.T) {
		sc := newScenario(t)
		sc.withDynamicCharge()

		assert.True(t, sc.cycle(20, 1000, 0))
		assert.Equal(t, 4000.0, val(sc.charge))

		assert.False(t, sc.cycle(20, 8000+4000, -4000))
		assert.Equal(t, 0.0, val(sc.charge))
		assert.Equal(t, 3000.0, val(sc.peak))
		assert.Equal(t, api.BatteryNormal, sc.mode())
	})

	t.Run("peak shaving off: full power", func(t *testing.T) {
		sc := newScenario(t)
		sc.withDynamicCharge()
		sc.site.peak().enabled = false

		assert.True(t, sc.cycle(20, 9000, 0))
		assert.Equal(t, 6250.0, val(sc.charge))
	})

	t.Run("circuit is tighter than the peak limit", func(t *testing.T) {
		sc := newScenario(t)
		sc.withDynamicCharge()
		sc.withCircuit(9000, &scenarioLoad{title: "wallbox", power: 7000}, 0)

		// peak room 4000, circuit room 2000
		assert.True(t, sc.cycle(20, 1000, 0))
		assert.Equal(t, 2000.0, val(sc.charge))

		// charging at 2000, circuit full: holds, does not drop
		assert.True(t, sc.cycle(20, 3000, -2000))
		assert.Equal(t, 2000.0, val(sc.charge))
	})

	t.Run("stop writes zero", func(t *testing.T) {
		sc := newScenario(t)
		sc.withDynamicCharge()

		assert.True(t, sc.cycle(20, 1000, 0))
		assert.False(t, sc.cycle(80, 1000, 0))
		assert.Equal(t, 0.0, val(sc.charge))
	})
}

func TestScenarioSettingsValidation(t *testing.T) {
	sc := newScenario(t)
	site := sc.site

	for _, tc := range []struct {
		limit float64
		ok    bool
	}{
		{1999, false}, {2000, true}, {2250, false}, {2500, true}, {20000, true}, {20500, false},
	} {
		assert.Equal(t, tc.ok, site.SetPeakShavingLimit(tc.limit) == nil, "limit %.0f", tc.limit)
	}

	for _, tc := range []struct {
		soc float64
		ok  bool
	}{
		{0, false}, {5, true}, {95, true}, {100, false},
	} {
		assert.Equal(t, tc.ok, site.SetPeakShavingReserve(tc.soc) == nil, "reserve %.0f", tc.soc)
	}

	for _, tc := range []struct {
		power float64
		ok    bool
	}{
		{-1, false}, {0, true}, {6250, true}, {20000, true}, {20001, false},
	} {
		assert.Equal(t, tc.ok, site.SetPeakShavingChargePower(tc.power) == nil, "charge power %.0f", tc.power)
	}

	// start 20 / stop 80
	assert.Error(t, site.SetBatterySocGridChargeStart(80), "start at stop")
	assert.Error(t, site.SetBatterySocGridChargeStop(20), "stop at start")
	assert.NoError(t, site.SetBatterySocGridChargeStart(79))
	assert.NoError(t, site.SetBatterySocGridChargeStop(100))
	assert.Error(t, site.SetBatterySocGridChargeStart(-1))
	assert.Error(t, site.SetBatterySocGridChargeStop(101))

	assert.Error(t, site.SetPeakShavingEntity("sensor.battery"), "wrong domain")
	assert.Error(t, site.SetPeakShavingChargeEntity("switch.battery"), "wrong domain")

	assert.NoError(t, site.SetLmPriority(lmBatteryName, 0))
	assert.NoError(t, site.SetLmPriority(lmBatteryName, 10))
	assert.Error(t, site.SetLmPriority(lmBatteryName, 11))
	assert.Error(t, site.SetLmPriority(lmBatteryName, -1))
	assert.Error(t, site.SetLmPriority("no-such-loadpoint", 1))

	assert.Error(t, site.SetPeakShavingCircuit("no-such-circuit"))

	// switching on without a target entity is refused
	site.peak().set = nil
	site.peak().enabled = false
	assert.Error(t, site.SetPeakShaving(true))
}
