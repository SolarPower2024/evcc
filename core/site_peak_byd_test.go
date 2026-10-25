package core

import (
	"bytes"
	"net/http"
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

	return sc, ha
}

// bydOn are the calls of the first cycle of peak shaving
func bydOn(soc, limit string) []string {
	return protOn(soc, limit)
}

// bydCharging runs the first cycle of grid charging: charge power, then the
// peak shaving; the request is the scripts' of the battery mode
func bydCharging(t *testing.T) (*scenario, *fakeHA) {
	t.Helper()

	sc, ha := newByd(t)
	sc.site.lms().socChargeEnabled = true

	require.True(t, sc.cycle(20, 1000, 0))
	require.Equal(t, append([]string{"set_value " + haChargeNum + " 4000"}, bydOn("30", "5000")...), ha.take())
	require.False(t, sc.site.peakOwned(), "no manual control, no request: evcc holds nothing else")
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

// TestBydLimitWithoutPeakShaving verifies that evcc goes on writing the limit
// without peak shaving, whatever the battery mode: the integration takes evcc as
// gone otherwise. Rules as before: at least 100 W, again at 0, no small rise.
func TestBydLimitWithoutPeakShaving(t *testing.T) {
	sc, ha := newByd(t)
	sc.site.lms().socChargeEnabled = false
	ha.take()

	// peak shaving off, battery mode normal: the limit is there all the same
	require.NoError(t, sc.site.SetPeakShaving(false))
	assert.Equal(t, []string{"set_value " + haProtLimit + " 5000"}, ha.take())
	assert.Equal(t, "off", ha.get(haProtSwitch), "no switch, no threshold")

	sc.cycle(50, 1000, 0)
	assert.Empty(t, ha.take(), "written once while the entity shows it")

	// the quarter hour began
	ha.set(haProtLimit, "0")
	sc.cycle(50, 1000, 0)
	assert.Equal(t, []string{"set_value " + haProtLimit + " 5000"}, ha.take())

	// in any battery mode
	for _, m := range []api.BatteryMode{api.BatteryHold, api.BatteryCharge} {
		sc.site.SetBatteryMode(m)
		ha.set(haProtLimit, "0")
		sc.site.applyOmni()
		assert.Equal(t, []string{"set_value " + haProtLimit + " 5000"}, ha.take(), m.String())
	}

	// without a limit entity or a battery there is nothing to write
	s := sc.site.peak()
	ha.set(haProtLimit, "0")
	s.protLimit = ""
	sc.site.applyOmni()
	assert.Empty(t, ha.take())
	s.protLimit = haProtLimit
	sc.site.batteryMeters = nil
	sc.site.applyOmni()
	assert.Empty(t, ha.take())
}

// TestBydMarstekWithoutPeakShavingWritesNothing verifies the Marstek has no sign of life
func TestBydMarstekWithoutPeakShavingWritesNothing(t *testing.T) {
	sc, ha := newOmni(t)
	require.NoError(t, sc.site.SetPeakShaving(false))
	ha.take()

	sc.cycle(50, 1000, 0)
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

// TestBydRelease verifies that the three parts are released when they are not
// needed any more
func TestBydRelease(t *testing.T) {
	t.Run("peak shaving removed", func(t *testing.T) {
		sc, ha := bydCharging(t)
		require.NoError(t, sc.site.SetPeakShavingProtLimit(""))
		assert.Equal(t, []string{"turn_off " + haProtSwitch}, ha.take())
		assert.False(t, sc.site.GetPeakShaving(), "it cannot run without")
	})

	t.Run("battery type changed", func(t *testing.T) {
		sc, ha := bydCharging(t)
		require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeMarstek))
		assert.Equal(t, []string{"turn_off " + haProtSwitch}, ha.take())
		assert.False(t, sc.site.peak().protOwned)
	})

	t.Run("battery type change refused", func(t *testing.T) {
		sc, ha := bydCharging(t)
		ha.fail["switch/turn_off"] = http.StatusInternalServerError

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
		s.batteryType = batteryTypeBYD
		s.enabled = false
		require.NoError(t, again.site.rebuildChargeSetter())

		assert.True(t, s.protOwned)
		for range 3 {
			again.cycle(50, 1000, 0)
		}
		assert.Equal(t, []string{"turn_off " + haProtSwitch}, ha.take())
		assert.False(t, s.protOwned)
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

	require.NoError(t, sc.site.SetPeakShaving(true))
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

// TestBydHeldMarkStored verifies that the mark of the switch is in the database right
// away: a crash within the minute would lose it. The mark of the manual control
// (peakShavingOwned) is never set for this type.
func TestBydHeldMarkStored(t *testing.T) {
	sc, _ := newByd(t)
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	sc.site.lms().socChargeEnabled = true

	require.True(t, sc.cycle(20, 1000, 0))
	assert.Empty(t, stored(t, keys.PeakShavingOwned), "no manual control")
	assert.Equal(t, "true", stored(t, keys.PeakShavingProtOwned), "peak shaving")

	require.False(t, sc.cycle(90, 1000, 0))
	assert.Empty(t, stored(t, keys.PeakShavingOwned))

	require.NoError(t, sc.site.SetPeakShaving(false))
	assert.Equal(t, "false", stored(t, keys.PeakShavingProtOwned))
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

	t.Run("peak shaving off without the entities", func(t *testing.T) {
		sc, _ := newByd(t)
		keepSettings(t)
		settings.SetString(keys.PeakShavingEntity, entity)
		settings.SetBool(keys.PeakShaving, true)
		s := sc.site.peak()
		s.enabled = true
		s.protSwitch, s.protLimit, s.protSoc, s.protSocSet = "", "", "", nil

		var buf bytes.Buffer
		sc.site.log = testLogger(&buf)

		sc.site.migratePeakEntity()
		assert.False(t, sc.site.GetPeakShaving())
		on, err := settings.Bool(keys.PeakShaving)
		require.NoError(t, err)
		assert.False(t, on, "stored")
		assert.Contains(t, buf.String(), "off until the entities of evcc Peak Fronius are set")
	})

	t.Run("peak shaving stays on with the entities", func(t *testing.T) {
		sc, _ := newByd(t)
		keepSettings(t)
		settings.SetString(keys.PeakShavingEntity, entity)
		sc.site.peak().enabled = true

		sc.site.migratePeakEntity()
		assert.True(t, sc.site.GetPeakShaving())
	})

	t.Run("peak shaving without entities, no old key: untouched", func(t *testing.T) {
		sc, _ := newByd(t)
		keepSettings(t)
		s := sc.site.peak()
		s.enabled = true
		s.protSwitch = ""

		sc.site.migratePeakEntity()
		assert.True(t, sc.site.GetPeakShaving())
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
