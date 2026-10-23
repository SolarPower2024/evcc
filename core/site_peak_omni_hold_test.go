package core

import (
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// omniHold is newOmni with the optimizer in automatic mode holding the battery
func omniHold(t *testing.T) (*scenario, *fakeHA) {
	t.Helper()

	sc, ha := newOmni(t)
	enableAutomatic(t)
	sc.suggest(api.BatteryHold.String())

	return sc, ha
}

// heldSoc returns the persisted threshold before the hold, 0 = not raised
func heldSoc(t *testing.T) float64 {
	t.Helper()

	v, err := settings.Float(keys.PeakShavingHoldSoc)
	if err != nil {
		return 0
	}
	return v
}

// While the optimizer holds, the threshold goes to 100 % and the value before is
// kept; the end of the hold writes the reserve again.
func TestOmniHoldRaisesThreshold(t *testing.T) {
	sc, ha := omniHold(t)
	ha.set(haProtSoc, "30")

	sc.cycle(50, 3000, 0)
	assert.Equal(t, "100", ha.get(haProtSoc))
	assert.Equal(t, "on", ha.get(haProtSwitch), "peak shaving stays on")
	assert.Equal(t, 30.0, sc.site.peak().holdSoc)
	assert.Equal(t, 30.0, heldSoc(t), "persisted")

	// a second cycle keeps the value read before the first raise
	sc.cycle(50, 3000, 0)
	assert.Equal(t, 30.0, sc.site.peak().holdSoc)

	sc.suggest(api.BatteryNormal.String())
	sc.cycle(50, 3000, 0)
	assert.Equal(t, "30", ha.get(haProtSoc))
	assert.Zero(t, sc.site.peak().holdSoc)
	assert.Zero(t, heldSoc(t))
}

// A changed reserve during the hold is what the end of the hold writes, as every
// cycle with peak shaving on does.
func TestOmniHoldEndsWithCurrentReserve(t *testing.T) {
	sc, ha := omniHold(t)

	sc.cycle(50, 3000, 0)
	require.Equal(t, "100", ha.get(haProtSoc))

	require.NoError(t, sc.site.SetPeakShavingReserve(40))
	sc.suggest(api.BatteryNormal.String())
	sc.cycle(50, 3000, 0)
	assert.Equal(t, "40", ha.get(haProtSoc))
}

// Turning peak shaving off during the hold writes the value before back, then
// turns the switch off.
func TestOmniHoldRestoreOnPeakOff(t *testing.T) {
	sc, ha := omniHold(t)
	ha.set(haProtSoc, "35")

	sc.cycle(50, 3000, 0)
	require.Equal(t, "100", ha.get(haProtSoc))
	ha.take()

	require.NoError(t, sc.site.SetPeakShaving(false))
	sc.cycle(50, 3000, 0)
	assert.Equal(t, []string{"set_value " + haProtSoc + " 35", "turn_off " + haProtSwitch}, ha.take())
	assert.Zero(t, heldSoc(t))
}

// A failing write back keeps the switch on and the value kept, the next cycle
// tries again.
func TestOmniHoldRestoreFails(t *testing.T) {
	sc, ha := omniHold(t)
	ha.set(haProtSoc, "30")
	sc.cycle(50, 3000, 0)

	// turning peak shaving off releases right away, see SetPeakShaving
	ha.fail["number/set_value"] = 500
	require.NoError(t, sc.site.SetPeakShaving(false))
	sc.cycle(50, 3000, 0)
	assert.Equal(t, "on", ha.get(haProtSwitch))
	assert.Equal(t, 30.0, heldSoc(t))

	delete(ha.fail, "number/set_value")
	sc.cycle(50, 3000, 0)
	assert.Equal(t, "30", ha.get(haProtSoc))
	assert.Equal(t, "off", ha.get(haProtSwitch))
}

// A shutdown during the hold writes the value before back.
func TestOmniHoldRestoreOnShutdown(t *testing.T) {
	sc, ha := omniHold(t)
	ha.set(haProtSoc, "30")
	sc.cycle(50, 3000, 0)
	require.Equal(t, "100", ha.get(haProtSoc))

	sc.site.releasePeakOnShutdown()
	assert.Equal(t, "30", ha.get(haProtSoc))
	assert.Zero(t, heldSoc(t))
}

// After a crash during the hold, the next start writes the value before back,
// also with peak shaving off by then.
func TestOmniHoldRestoreAfterRestart(t *testing.T) {
	sc, ha := newOmni(t)
	ha.set(haProtSoc, "100")

	settings.SetString(keys.PeakShavingBatteryType, batteryTypeMarstek)
	settings.SetString(keys.PeakShavingProtSwitch, haProtSwitch)
	settings.SetString(keys.PeakShavingProtLimit, haProtLimit)
	settings.SetString(keys.PeakShavingProtSoc, haProtSoc)
	settings.SetBool(keys.PeakShaving, false)
	settings.SetFloat(keys.PeakShavingHoldSoc, 25)

	again := newScenario(t)
	again.site.lms().socChargeEnabled = false
	again.site.custom.peak.conn = sc.site.peak().conn
	again.site.restorePeakSettings()
	require.Equal(t, 25.0, again.site.peak().holdSoc)

	again.cycle(50, 3000, 0)
	assert.Equal(t, "25", ha.get(haProtSoc))
	assert.Zero(t, heldSoc(t))
}

// Without peak shaving the hold is evcc's battery mode, the threshold is not
// touched.
func TestOmniHoldWithoutPeakShaving(t *testing.T) {
	sc, ha := omniHold(t)
	sc.site.peak().enabled = false
	ha.set(haProtSoc, "30")

	sc.cycle(50, 3000, 0)
	assert.Equal(t, "30", ha.get(haProtSoc))
	assert.Zero(t, heldSoc(t))
}

// Without automatic mode a hold suggestion changes nothing.
func TestOmniHoldInertWithoutAutomatic(t *testing.T) {
	sc, ha := newOmni(t)
	sc.suggest(api.BatteryHold.String())

	sc.cycle(50, 3000, 0)
	assert.Equal(t, "30", ha.get(haProtSoc))
	assert.Zero(t, heldSoc(t))
}

// A peak covered during the hold is recorded like one below the reserve.
func TestOmniHoldRecordsPeak(t *testing.T) {
	sc, _ := omniHold(t)

	sc.cycle(50, 8000, 0)
	assert.True(t, sc.site.peak().covering, "peak covered")
}
