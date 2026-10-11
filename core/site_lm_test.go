package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
)

// TestSocChargeRunningSurvivesRestart verifies that soc-based grid charging
// interrupted by a restart carries on instead of waiting for the start soc again
func TestSocChargeRunningSurvivesRestart(t *testing.T) {
	noSettingsDB(t)
	defer settings.SetBool(keys.BatterySocGridChargeRunning, false)

	before := &Site{log: util.NewLogger("test")}
	before.setSocChargeRunning(true)

	after := &Site{log: util.NewLogger("test")}
	after.restoreLmSettings()

	assert.True(t, after.lms().socChargeRunning)

	// charging that ended stays ended
	after.setSocChargeRunning(false)

	again := &Site{log: util.NewLogger("test")}
	again.restoreLmSettings()

	assert.False(t, again.lms().socChargeRunning)
}

// TestBatteryNotFollowing replays the log of a battery that keeps grid-charging
// at 6250W although it was given 3000W: the heater above it keeps its power at
// first and is cut once the battery was ignored for the set cycles
func TestBatteryNotFollowing(t *testing.T) {
	noSettingsDB(t)
	sc := newScenario(t)
	sc.withDynamicCharge()

	heater := &scenarioLoad{title: "heater", prio: 5, power: 3000}
	sc.withCircuit(9000, heater, 1) // 3000W heater + 6250W battery on 9000W
	sc.site.peak().limit = 7000     // leaves 3000W for charging at 4000W demand

	heaterAllowed := func() float64 { return sc.site.lmm().ValidatePower(heater, sc.circuit, 3000, 3000) }

	for i := range 3 {
		sc.site.lmm().Record(heater, 3000, heaterAllowed(), true, time.Now())
		assert.Equal(t, 3000.0, heaterAllowed(), "cycle %d: the battery is expected to give way", i+1)

		sc.cycle(15, 10250, -6250)
		assert.Equal(t, 3000.0, val(sc.charge))

		d, _ := sc.site.lmm().LastDecision(sc.site.lmBattery())
		assert.Equal(t, 3000.0, d.Allowed, "the battery's limit is recorded")
	}

	assert.Equal(t, 2750.0, heaterAllowed(), "no longer counting on the battery")

	var found bool
	for _, e := range sc.site.lmm().Events() {
		found = found || e.Type == lm.EventNotFollowing && e.A == 6250 && e.B == 3000
	}
	assert.True(t, found, "event for the overview")
}

// TestSocUnknownKeepsState verifies that nothing is decided from a soc that was
// never read (0 after the start) or whose reads all fail: the hysteresis, the
// one-time order and the peak reserve stay as they were
func TestSocUnknownKeepsState(t *testing.T) {
	noSettingsDB(t)
	defer settings.SetBool(keys.BatterySocGridChargeRunning, false)

	bat := config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(&capacityBattery{kWh: 10}))
	site := &Site{log: util.NewLogger("test"), batteryMeters: []config.Device[api.Meter]{bat}}
	site.lms().socChargeEnabled = true

	// start: 0 would be below the start soc, but nothing was read
	assert.False(t, site.batterySocChargeActive(), "first cycle without soc")
	assert.False(t, site.lms().socChargeRunning)

	setBatterySoc(site, 50)
	assert.False(t, site.batterySocChargeActive(), "the real soc lies above the start soc")

	// charging halfway: a failing read neither stops nor restarts it
	setBatterySoc(site, 30)
	site.lms().socChargeRunning = true
	site.battery.Devices = []types.Measurement{{}}
	site.battery.Soc = 90 // retained from before, above the stop soc
	assert.True(t, site.batterySocChargeActive(), "kept while no soc is read")

	setBatterySoc(site, 90)
	assert.False(t, site.batterySocChargeActive(), "stop soc read")

	// the switch still switches off without a soc
	site.lms().socChargeRunning = true
	site.battery.Devices = nil
	site.lms().socChargeEnabled = false
	assert.False(t, site.batterySocChargeActive(), "switched off")

	// one-time order: not planned from 0 %, carries on as stored
	site.battery.Soc = 0
	site.lms().gridOnce = gridChargeOnce{Target: 80}
	assert.False(t, site.batteryGridChargeOnceActive(), "not active yet, no soc to charge from")
	assert.Equal(t, 80.0, site.gridChargeOnce().Target, "the order stays")
	assert.False(t, site.gridChargeOnce().Active)

	site.lms().gridOnce = gridChargeOnce{Target: 80, Active: true}
	assert.True(t, site.batteryGridChargeOnceActive(), "charging carries on")
	setBatterySoc(site, 85)
	assert.False(t, site.batteryGridChargeOnceActive(), "target read")
	assert.Zero(t, site.gridChargeOnce().Target)
}

// TestPeakReserveUnknownSoc verifies that the peak reserve is not entered at a
// soc that was never read
func TestPeakReserveUnknownSoc(t *testing.T) {
	noSettingsDB(t)
	sc := newScenario(t)
	site := sc.site
	site.battery.Soc = 0
	site.battery.Devices = nil
	site.updatePeakShaving(site.state())
	assert.False(t, site.peakShavingActive(), "reserve not entered at an unread soc")

	setBatterySoc(site, 10)
	site.updatePeakShaving(site.state())
	assert.True(t, site.peakShavingActive(), "reserve entered at the read soc")

	site.battery.Devices = nil
	site.battery.Soc = 90
	site.updatePeakShaving(site.state())
	assert.True(t, site.peakShavingActive(), "state kept while the soc is not read")
}
