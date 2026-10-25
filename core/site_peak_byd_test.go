package core

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// externalControl makes peak shaving count as set up, without a Home Assistant
func externalControl(s *peakState) {
	s.protSwitch, s.protLimit, s.protSoc = haProtSwitch, haProtLimit, haProtSoc
	s.protSocSet = func(float64) error { return nil }
}

// newByd is newOmni with the battery type BYD, set up as the integration evcc Peak
// Fronius offers it: the limit shows 0 until evcc writes it, in steps of 1 W, and
// the request is a select. Neither the manual control nor the mode exist.
func newByd(t *testing.T) (*scenario, *fakeHA) {
	t.Helper()

	sc, ha := newOmni(t)

	ha.mu.Lock()
	ha.states[haProtLimit] = &haEntity{"0", map[string]any{"min": 0.0, "max": 100000.0, "step": 1.0}}
	ha.states[haProtSoc] = &haEntity{"20", map[string]any{"min": 5.0, "max": 100.0, "step": 1.0}}
	ha.mu.Unlock()

	s := sc.site.peak()
	s.batteryType = batteryTypeBYD
	s.manualEntity, s.modeEntity = "", ""
	s.requestEntity = haRequest

	return sc, ha
}

// bydOn are the calls of the first cycle of peak shaving
func bydOn(soc, limit string) []string {
	return protOn(soc, limit)
}

// bydCharging runs the first cycle of grid charging: charge power, then the
// peak shaving, then the request
func bydCharging(t *testing.T) (*scenario, *fakeHA) {
	t.Helper()

	sc, ha := newByd(t)
	sc.site.lms().socChargeEnabled = true

	require.True(t, sc.cycle(20, 1000, 0))
	require.Equal(t, append(append([]string{"set_value " + haChargeNum + " 4000"}, bydOn("30", "5000")...),
		"select_option "+haRequest+" grid_charge"), ha.take())
	require.True(t, sc.site.peakOwned())
	require.True(t, sc.site.peak().protOwned)

	return sc, ha
}

// TestBydProtection verifies that the battery type BYD hands peak shaving to the
// integration like a Marstek does: the reserve as threshold, the allowed power of
// the window as limit, the switch on, and the discharge never written
func TestBydProtection(t *testing.T) {
	sc, ha := newByd(t)
	sc.site.lms().socChargeEnabled = false

	sc.cycle(25, 8000, 0)
	assert.Equal(t, bydOn("30", "5000"), ha.take())
	assert.Equal(t, "on", ha.get(haProtSwitch))
	assert.False(t, sc.site.peakOwned(), "no request held")
	assert.True(t, sc.site.peak().protOwned)
	assert.Equal(t, 3000.0, val(sc.peak), "the setpoint is shown, not written")

	owned, err := settings.Bool(keys.PeakShavingProtOwned)
	require.NoError(t, err)
	assert.True(t, owned)

	// written once while the entities show the values
	for range 3 {
		sc.cycle(25, 8000, 0)
	}
	sc.cycle(50, 8000, 0)
	assert.Empty(t, ha.take())

	assert.Equal(t, "0", ha.get(haDischargeNum), "the discharge entity is never written")
}

// TestBydThresholdHasNoMinimum verifies that the reserve is the threshold as it is
// (the 20 % floor is Omnibattery's)
func TestBydThresholdHasNoMinimum(t *testing.T) {
	sc, ha := newByd(t)
	sc.site.lms().socChargeEnabled = false
	require.NoError(t, sc.site.SetPeakShavingReserve(10))
	assert.Equal(t, 10.0, sc.site.GetPeakShavingReserve())

	sc.cycle(8, 3000, 0)
	assert.Equal(t, bydOn("10", "5000"), ha.take())
}

// TestBydLimitSignOfLife verifies the limit as sign of life of evcc: never below
// 100 W, as 0 means expired, and written again when the integration shows 0
func TestBydLimitSignOfLife(t *testing.T) {
	sc, ha := newByd(t)
	sc.site.lms().socChargeEnabled = false

	sc.cycle(25, 8000, 0)
	ha.take()

	// the quarter hour began: the integration shows 0, evcc writes the same limit again
	ha.set(haProtLimit, "0")
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"set_value " + haProtLimit + " 5000"}, ha.take())

	// a small limit is lifted to the minimum, and written again at 0 although it
	// is within the rise that is not written
	s := sc.site.peak()
	s.mu.Lock()
	s.window.Allowed = 40
	s.mu.Unlock()
	sc.site.updateBatteryModePeakAware(false, false, sc.rate)
	assert.Equal(t, []string{"set_value " + haProtLimit + " 100"}, ha.take())

	ha.set(haProtLimit, "0")
	sc.site.updateBatteryModePeakAware(false, false, sc.rate)
	assert.Equal(t, []string{"set_value " + haProtLimit + " 100"}, ha.take())

	sc.site.updateBatteryModePeakAware(false, false, sc.rate)
	assert.Empty(t, ha.take())
}

// TestBydRequest verifies the request: written from the battery mode, held while
// it is not normal, back to normal once
func TestBydRequest(t *testing.T) {
	sc, ha := bydCharging(t)
	assert.Equal(t, "grid_charge", ha.get(haRequest))

	stored, err := settings.Bool(keys.PeakShavingOwned)
	require.NoError(t, err)
	assert.True(t, stored)

	// written once
	sc.cycle(20, 1000, 0)
	assert.Empty(t, ha.take())

	// the charge ends: the charge power to 0, the request normal
	require.False(t, sc.cycle(90, 1000, 0))
	assert.Equal(t, []string{"set_value " + haChargeNum + " 0", "select_option " + haRequest + " normal"}, ha.take())
	assert.False(t, sc.site.peakOwned())
	assert.Equal(t, "normal", ha.get(haRequest))

	// and not again
	sc.cycle(90, 1000, 0)
	assert.Empty(t, ha.take())
}

// TestBydRequestHold verifies hold as request, and that a request set by hand is
// left alone while evcc holds nothing
func TestBydRequestHold(t *testing.T) {
	sc, ha := newByd(t)
	sc.site.lms().socChargeEnabled = false
	sc.site.peak().enabled = false

	ha.set(haRequest, "hold")
	sc.cycle(50, 1000, 0)
	assert.Empty(t, ha.take(), "set by hand, evcc asks for normal and holds nothing")
	assert.Equal(t, "hold", ha.get(haRequest))

	sc.site.SetBatteryMode(api.BatteryHold)
	ha.set(haRequest, "normal")
	sc.site.applyOmni()
	assert.Equal(t, []string{"set_value " + haProtLimit + " 5000", "select_option " + haRequest + " hold"}, ha.take())
	assert.True(t, sc.site.peakOwned())

	// changed by hand while held: corrected
	ha.set(haRequest, "grid_charge")
	sc.site.applyOmni()
	assert.Equal(t, []string{"select_option " + haRequest + " hold"}, ha.take())

	sc.site.SetBatteryMode(api.BatteryNormal)
	sc.site.applyOmni()
	assert.Equal(t, []string{"select_option " + haRequest + " normal"}, ha.take())
	assert.False(t, sc.site.peakOwned())
}

// TestBydLimitWithoutPeakShaving verifies that evcc goes on writing the limit while
// it steers the battery with a request, peak shaving off: the integration takes
// evcc as gone otherwise
func TestBydLimitWithoutPeakShaving(t *testing.T) {
	sc, ha := newByd(t)
	sc.site.lms().socChargeEnabled = false
	require.NoError(t, sc.site.SetPeakShaving(false))
	ha.take()

	// nothing requested: nothing written
	sc.cycle(50, 1000, 0)
	assert.Empty(t, ha.take())

	sc.site.SetBatteryMode(api.BatteryCharge)
	sc.site.applyOmni()
	assert.Equal(t, []string{"set_value " + haProtLimit + " 5000", "select_option " + haRequest + " grid_charge"}, ha.take(), "limit, no switch, no threshold")
	assert.Equal(t, "off", ha.get(haProtSwitch))

	// the quarter hour began
	ha.set(haProtLimit, "0")
	sc.site.applyOmni()
	assert.Equal(t, []string{"set_value " + haProtLimit + " 5000"}, ha.take())

	// back to normal: the request goes, the limit stops
	sc.site.SetBatteryMode(api.BatteryNormal)
	sc.site.applyOmni()
	assert.Equal(t, []string{"select_option " + haRequest + " normal"}, ha.take())
	ha.set(haProtLimit, "0")
	sc.site.applyOmni()
	assert.Empty(t, ha.take())
}

// TestBydPeakShavingOffReleasesSwitch verifies that switching peak shaving off
// turns the integration's switch off, limit and threshold stay
func TestBydPeakShavingOffReleasesSwitch(t *testing.T) {
	sc, ha := newByd(t)
	sc.site.lms().socChargeEnabled = false

	sc.cycle(25, 8000, 0)
	ha.take()

	require.NoError(t, sc.site.SetPeakShaving(false))
	assert.Equal(t, []string{"turn_off " + haProtSwitch}, ha.take())
	assert.False(t, sc.site.peak().protOwned)
	assert.Equal(t, "30", ha.get(haProtSoc))
	assert.Equal(t, "5000", ha.get(haProtLimit))
}

// TestBydWithoutRequestEntity verifies that grid charging and holding without the
// request entity only warn, once
func TestBydWithoutRequestEntity(t *testing.T) {
	sc, ha := newByd(t)
	sc.site.peak().requestEntity = ""

	var buf bytes.Buffer
	sc.site.log = testLogger(&buf)

	sc.site.SetBatteryMode(api.BatteryCharge)
	sc.site.applyOmni()
	sc.site.applyOmni()
	assert.NotContains(t, strings.Join(ha.take(), " "), "select_option")
	assert.Equal(t, 1, strings.Count(buf.String(), "request entity"))
	assert.False(t, sc.site.peakOwned())
}

// TestBydRequestEntityChecks verifies the entity: its domain and the options as
// written, and that a request evcc holds is set back before the entity changes
func TestBydRequestEntityChecks(t *testing.T) {
	sc, ha := newByd(t)
	s := sc.site.peak()
	s.requestEntity = ""

	require.ErrorContains(t, sc.site.SetPeakShavingRequestEntity("sensor.request"), "select or input_select")

	ha.mu.Lock()
	ha.states["select.bad"] = &haEntity{"normal", map[string]any{"options": []string{"normal", "hold"}}}
	ha.states["select.upper"] = &haEntity{"Normal", map[string]any{"options": []string{"Normal", "Grid_charge", "Hold"}}}
	ha.mu.Unlock()

	require.ErrorContains(t, sc.site.SetPeakShavingRequestEntity("select.bad"), "grid_charge")
	require.Error(t, sc.site.SetPeakShavingRequestEntity("select.upper"), "the exact spelling")
	require.Error(t, sc.site.SetPeakShavingRequestEntity("select.missing"))
	assert.Empty(t, sc.site.GetPeakShavingRequestEntity())

	require.NoError(t, sc.site.SetPeakShavingRequestEntity(haRequest))
	assert.Equal(t, haRequest, sc.site.GetPeakShavingRequestEntity())
	assert.Equal(t, haRequest, stored2(t, keys.PeakShavingRequestEntity))
}

// stored2 returns a stored string setting
func stored2(t *testing.T, key string) string {
	t.Helper()

	v, err := settings.String(key)
	require.NoError(t, err)

	return v
}

// TestBydRequestEntityChangeReleases verifies that the request goes back to normal
// before the entity is replaced or removed, and that a change is refused while that
// fails
func TestBydRequestEntityChangeReleases(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		sc, ha := bydCharging(t)

		require.NoError(t, sc.site.SetPeakShavingRequestEntity(""))
		assert.Equal(t, []string{"select_option " + haRequest + " normal"}, ha.take())
		assert.False(t, sc.site.peakOwned())
		assert.Empty(t, sc.site.GetPeakShavingRequestEntity())
	})

	t.Run("replaced", func(t *testing.T) {
		sc, ha := bydCharging(t)
		ha.mu.Lock()
		ha.states["select.other"] = &haEntity{"normal", map[string]any{"options": []string{"normal", "grid_charge", "hold"}}}
		ha.mu.Unlock()

		require.NoError(t, sc.site.SetPeakShavingRequestEntity("select.other"))
		assert.Equal(t, []string{"select_option " + haRequest + " normal"}, ha.take())
		assert.False(t, sc.site.peakOwned())
	})

	t.Run("refused while it fails", func(t *testing.T) {
		sc, ha := bydCharging(t)
		ha.fail["select/select_option"] = http.StatusInternalServerError

		require.ErrorIs(t, sc.site.SetPeakShavingRequestEntity(""), errHandBack)
		assert.Equal(t, haRequest, sc.site.GetPeakShavingRequestEntity())
		assert.True(t, sc.site.peakOwned())

		delete(ha.fail, "select/select_option")
		require.NoError(t, sc.site.SetPeakShavingRequestEntity(""))
		assert.False(t, sc.site.peakOwned())
	})

	t.Run("entity gone", func(t *testing.T) {
		sc, ha := bydCharging(t)
		ha.forget(haRequest)

		require.NoError(t, sc.site.SetPeakShavingRequestEntity(""))
		assert.False(t, sc.site.peakOwned())
	})

	t.Run("not available keeps the hold", func(t *testing.T) {
		sc, ha := bydCharging(t)
		ha.set(haRequest, "unavailable")

		require.ErrorIs(t, sc.site.SetPeakShavingRequestEntity(""), errHandBack)
		assert.True(t, sc.site.peakOwned())
		assert.Empty(t, ha.take(), "nothing written to an entity that is not available")
	})
}

// TestBydRelease verifies that the three parts are released when they are not
// needed any more
func TestBydRelease(t *testing.T) {
	t.Run("peak shaving removed", func(t *testing.T) {
		sc, ha := bydCharging(t)
		require.NoError(t, sc.site.SetPeakShavingProtLimit(""))
		assert.Equal(t, []string{"turn_off " + haProtSwitch}, ha.take())
		assert.False(t, sc.site.GetPeakShaving(), "it cannot run without")
		assert.True(t, sc.site.peakOwned(), "the request is not part of it")
	})

	t.Run("battery type changed", func(t *testing.T) {
		sc, ha := bydCharging(t)
		require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeMarstek))
		assert.Equal(t, []string{"select_option " + haRequest + " normal", "turn_off " + haProtSwitch}, ha.take())
		assert.False(t, sc.site.peakOwned())
		assert.False(t, sc.site.peak().protOwned)
	})

	t.Run("battery type change refused", func(t *testing.T) {
		sc, ha := bydCharging(t)
		ha.fail["select/select_option"] = http.StatusInternalServerError

		require.ErrorIs(t, sc.site.SetPeakShavingBatteryType(batteryTypeMarstek), errHandBack)
		assert.Equal(t, batteryTypeBYD, sc.site.GetPeakShavingBatteryType())
	})

	t.Run("after a restart", func(t *testing.T) {
		sc, ha := bydCharging(t)
		sc.site.lms().socChargeEnabled = false

		// what a new site reads back: the marks are stored
		again := newScenario(t)
		again.site.custom.peak.conn = sc.site.peak().conn
		again.site.restorePeakSettings()
		s := again.site.peak()
		s.batteryType, s.requestEntity = batteryTypeBYD, haRequest
		s.enabled = false
		require.NoError(t, again.site.rebuildChargeSetter())

		assert.True(t, again.site.peakOwned())
		for range 3 {
			again.cycle(50, 1000, 0)
		}
		assert.Equal(t, []string{"turn_off " + haProtSwitch, "select_option " + haRequest + " normal"}, ha.take())
		assert.False(t, again.site.peakOwned())
	})
}

// TestBydNeedsEntities verifies that peak shaving cannot be turned on without the
// three entities, with the request entity or not
func TestBydNeedsEntities(t *testing.T) {
	sc, _ := newByd(t)
	s := sc.site.peak()
	require.NoError(t, sc.site.SetPeakShaving(false))

	for _, missing := range []func(){
		func() { s.protSwitch = "" },
		func() { s.protLimit = "" },
		func() { s.protSoc, s.protSocSet = "", nil },
	} {
		missing()
		require.ErrorIs(t, sc.site.SetPeakShaving(true), errProtMissing)
		s.protSwitch, s.protLimit, s.protSoc = haProtSwitch, haProtLimit, haProtSoc
		require.NoError(t, sc.site.rebuildProtSetter())
	}

	s.requestEntity = ""
	require.NoError(t, sc.site.SetPeakShaving(true), "the request is not needed")
}

// TestBydOptimizerHold verifies that the optimizer's hold raises the threshold for
// the battery type BYD as well, and gives it back
func TestBydOptimizerHold(t *testing.T) {
	sc, ha := newByd(t)
	keepSettings(t)
	holdOptimizer(t, sc.site)
	enableAutomatic(t)
	sc.suggest(api.BatteryHold.String())
	ha.set(haProtSoc, "30")

	sc.cycle(50, 3000, 0)
	assert.Equal(t, "100", ha.get(haProtSoc))
	assert.Equal(t, 30.0, sc.site.peak().holdSoc)

	sc.suggest(api.BatteryNormal.String())
	sc.cycle(50, 3000, 0)
	assert.Equal(t, "30", ha.get(haProtSoc))
	assert.Zero(t, sc.site.peak().holdSoc)
}

// TestBydMarstekHasNoRequest verifies that the request is never written for the
// battery type Marstek
func TestBydMarstekHasNoRequest(t *testing.T) {
	sc, ha := newOmni(t)
	sc.site.peak().requestEntity = haRequest
	sc.site.lms().socChargeEnabled = true

	sc.cycle(20, 1000, 0)
	sc.cycle(90, 1000, 0)
	for _, c := range ha.take() {
		assert.NotContains(t, c, haRequest)
	}
	assert.Equal(t, "normal", ha.get(haRequest))
}

// TestBydHeldMarkStored verifies that the request mark is in the database right
// away: a crash within the minute would lose it
func TestBydHeldMarkStored(t *testing.T) {
	sc, _ := newByd(t)
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	sc.site.lms().socChargeEnabled = true

	require.True(t, sc.cycle(20, 1000, 0))
	assert.Equal(t, "true", stored(t, keys.PeakShavingOwned), "request")
	assert.Equal(t, "true", stored(t, keys.PeakShavingProtOwned), "peak shaving")

	require.False(t, sc.cycle(90, 1000, 0))
	assert.Equal(t, "false", stored(t, keys.PeakShavingOwned))

	require.NoError(t, sc.site.SetPeakShaving(false))
	assert.Equal(t, "false", stored(t, keys.PeakShavingProtOwned))
}

// TestBydRestoresRequestEntity verifies that the request entity survives a restart
func TestBydRestoresRequestEntity(t *testing.T) {
	sc, _ := newByd(t)
	sc.site.peak().requestEntity = ""
	require.NoError(t, sc.site.SetPeakShavingRequestEntity(haRequest))

	again := newScenario(t)
	again.site.custom.peak.conn = sc.site.peak().conn
	again.site.restorePeakSettings()

	assert.Equal(t, haRequest, again.site.GetPeakShavingRequestEntity())
}

// TestBydMigratePeakEntity verifies the end of the old way: the discharge setpoint
// entity is set free once if evcc held it back, and its key and the mark are gone
func TestBydMigratePeakEntity(t *testing.T) {
	const entity = haDischargeNum

	t.Run("held", func(t *testing.T) {
		sc, ha := newByd(t)
		keepSettings(t)
		settings.SetString(keys.PeakShavingEntity, entity)
		settings.SetBool(keys.PeakShavingOwned, true)
		ha.set(entity, "3000")

		s := sc.site.peak()
		s.owned = true

		sc.site.migratePeakEntity()
		assert.Equal(t, []string{"set_value " + entity + " 10000"}, ha.take())
		assert.False(t, sc.site.peakOwned())

		_, err := settings.String(keys.PeakShavingEntity)
		assert.ErrorIs(t, err, settings.ErrNotFound)
		_, err = settings.Bool(keys.PeakShavingOwned)
		assert.ErrorIs(t, err, settings.ErrNotFound)

		// once
		sc.site.migratePeakEntity()
		assert.Empty(t, ha.take())
	})

	t.Run("stored free value", func(t *testing.T) {
		sc, ha := newByd(t)
		keepSettings(t)
		settings.SetString(keys.PeakShavingEntity, entity)
		ha.set(entity, "3000")

		v := 8000.0
		lms := sc.site.lms()
		lms.advMu.Lock()
		lms.adv.FreeValue = &v
		lms.advMu.Unlock()
		sc.site.peak().owned = true

		sc.site.migratePeakEntity()
		assert.Equal(t, []string{"set_value " + entity + " 8000"}, ha.take())
		assert.Nil(t, sc.site.advanced().FreeValue, "dropped")
	})

	t.Run("not held", func(t *testing.T) {
		sc, ha := newByd(t)
		keepSettings(t)
		settings.SetString(keys.PeakShavingEntity, entity)
		ha.set(entity, "3000")

		sc.site.migratePeakEntity()
		assert.Empty(t, ha.take(), "an entity evcc never held back is left alone")

		_, err := settings.String(keys.PeakShavingEntity)
		assert.ErrorIs(t, err, settings.ErrNotFound)
	})

	t.Run("failing write is logged, not repeated", func(t *testing.T) {
		sc, ha := newByd(t)
		keepSettings(t)
		settings.SetString(keys.PeakShavingEntity, entity)
		settings.SetBool(keys.PeakShavingOwned, true)
		sc.site.peak().owned = true
		ha.fail["number/set_value"] = http.StatusInternalServerError

		var buf bytes.Buffer
		sc.site.log = testLogger(&buf)

		sc.site.migratePeakEntity()
		assert.Contains(t, buf.String(), "was not set free")
		assert.False(t, sc.site.peakOwned())

		_, err := settings.String(keys.PeakShavingEntity)
		assert.ErrorIs(t, err, settings.ErrNotFound)

		delete(ha.fail, "number/set_value")
		ha.take()
		sc.site.migratePeakEntity()
		assert.Empty(t, ha.take())
	})

	t.Run("marstek keeps its mark", func(t *testing.T) {
		sc, ha := newOmni(t)
		keepSettings(t)
		settings.SetString(keys.PeakShavingEntity, entity)
		settings.SetBool(keys.PeakShavingOwned, true)
		sc.site.peak().owned = true

		sc.site.migratePeakEntity()
		assert.Empty(t, ha.take(), "the mark is the manual control")
		assert.True(t, sc.site.peakOwned())

		owned, err := settings.Bool(keys.PeakShavingOwned)
		require.NoError(t, err)
		assert.True(t, owned)

		_, err = settings.String(keys.PeakShavingEntity)
		assert.ErrorIs(t, err, settings.ErrNotFound)
	})

	t.Run("no key, nothing to do", func(t *testing.T) {
		sc, ha := newByd(t)
		keepSettings(t)
		sc.site.peak().owned = true

		sc.site.migratePeakEntity()
		assert.Empty(t, ha.take())
		assert.True(t, sc.site.peakOwned(), "the request mark of this version")
	})
}
