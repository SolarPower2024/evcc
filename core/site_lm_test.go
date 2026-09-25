package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
)

// TestSocChargeRunningSurvivesRestart verifies that soc-based grid charging
// interrupted by a restart carries on instead of waiting for the start soc again
func TestSocChargeRunningSurvivesRestart(t *testing.T) {
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
	sc := newScenario(t)
	sc.withDynamicCharge()

	heater := &scenarioLoad{title: "heater", prio: 5, power: 3000}
	sc.withCircuit(9000, heater, 1) // 3000W heater + 6250W battery on 9000W
	sc.site.peak().limit = 7000     // leaves 3000W for charging at 4000W demand

	heaterAllowed := func() float64 { return lm.ValidatePower(heater, sc.circuit, 3000, 3000) }

	for i := range 3 {
		lm.Record(heater, 3000, heaterAllowed(), true, time.Now())
		assert.Equal(t, 3000.0, heaterAllowed(), "cycle %d: the battery is expected to give way", i+1)

		sc.cycle(15, 10250, -6250)
		assert.Equal(t, 3000.0, val(sc.charge))

		d, _ := lm.LastDecision(sc.site.lmBattery())
		assert.Equal(t, 3000.0, d.Allowed, "the battery's limit is recorded")
	}

	assert.Equal(t, 2750.0, heaterAllowed(), "no longer counting on the battery")

	var found bool
	for _, e := range lm.Events() {
		found = found || e.Type == lm.EventNotFollowing && e.A == 6250 && e.B == 3000
	}
	assert.True(t, found, "event for the overview")
}
