package core

import (
	"bytes"
	"strings"
	"testing"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm/profile"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restoreTestSite returns a site with a log that is read back
func restoreTestSite(t *testing.T) (*Site, *bytes.Buffer) {
	t.Helper()
	noSettingsDB(t)
	site := newScenario(t).site
	// other tests leave follow the peak on, which would move the limit
	for _, key := range []string{keys.PeakFollow, keys.PeakFollowBase, keys.PeakFollowBuffer} {
		_ = settings.Delete(key)
	}
	buf := new(bytes.Buffer)
	site.log = testLogger(buf)
	return site, buf
}

// TestRestoreUnreadableJson: every json setting that cannot be read falls back to
// its default, is logged once and kept as <key>Invalid
func TestRestoreUnreadableJson(t *testing.T) {
	const raw = `{"broken`

	restores := map[string]func(*Site){
		keys.LmAdvanced:            (*Site).restoreLmAdvanced,
		keys.LmProfiles:            (*Site).restoreLmProfiles,
		keys.PeakMonths:            (*Site).restorePeakMonths,
		keys.PeakTariff:            (*Site).restorePeakTariff,
		keys.LmShedProtected:       (*Site).restoreLmGuard,
		keys.LmPriorities:          (*Site).restoreLmSettings,
		keys.BatteryGridChargeOnce: (*Site).restoreGridChargeOnce,
	}

	for key, restore := range restores {
		t.Run(key, func(t *testing.T) {
			site, buf := restoreTestSite(t)
			settings.SetString(key, raw)

			restore(site)

			assert.Equal(t, 1, strings.Count(buf.String(), "ERROR"), buf.String())
			kept, err := settings.String(key + "Invalid")
			require.NoError(t, err)
			assert.Equal(t, raw, kept)

			// the value stays as it was, only a new valid value replaces the key;
			// the priorities are rewritten at start (unifyLmPriorities), the copy keeps the raw value
			if key != keys.LmPriorities {
				stored, _ := settings.String(key)
				assert.Equal(t, raw, stored)
			}
		})
	}
}

// TestRestoreFreshDatabase: nothing stored, nothing logged, nothing kept
func TestRestoreFreshDatabase(t *testing.T) {
	site, buf := restoreTestSite(t)

	site.restoreLmSettings()
	site.restorePeakMonths()
	site.restorePeakTariff()
	site.restoreLmProfiles()

	assert.NotContains(t, buf.String(), "ERROR")
	for _, s := range settings.All() {
		assert.False(t, strings.HasSuffix(s.Key, invalidSuffix), s.Key)
	}
}

// TestRestoreLaterSaveKeepsInvalidCopy: a value saved after an unreadable one
// replaces the key and leaves the copy
func TestRestoreLaterSaveKeepsInvalidCopy(t *testing.T) {
	site, _ := restoreTestSite(t)
	settings.SetString(keys.PeakTariff, "nope")

	site.restorePeakTariff()
	require.NoError(t, settings.SetJson(keys.PeakTariff, peakTariff{Price: 5}))

	kept, _ := settings.String(keys.PeakTariff + invalidSuffix)
	assert.Equal(t, "nope", kept)

	var t2 peakTariff
	require.NoError(t, settings.Json(keys.PeakTariff, &t2))
	assert.Equal(t, 5.0, t2.Price)
}

// TestRestoreLmAdvancedKeepsGoodFields: a field of the wrong type does not take the others along
func TestRestoreLmAdvancedKeepsGoodFields(t *testing.T) {
	site, buf := restoreTestSite(t)
	settings.SetString(keys.LmAdvanced, `{"holdOff":15,"timeout":"x","peakCap":3}`)

	site.restoreLmAdvanced()

	adv := site.advanced()
	require.NotNil(t, adv.HoldOff)
	assert.Equal(t, 15.0, *adv.HoldOff)
	require.NotNil(t, adv.PeakCap)
	assert.Equal(t, 3.0, *adv.PeakCap)
	assert.Nil(t, adv.Timeout)

	assert.Equal(t, 1, strings.Count(buf.String(), "ERROR"), buf.String())
	kept, err := settings.String(keys.LmAdvanced + invalidSuffix)
	require.NoError(t, err)
	assert.Contains(t, kept, `"timeout":"x"`)
}

// TestSaveLmProfileUnreadableList: changing, deleting or applying on top of an
// unreadable list fails and leaves the raw value; publishing stays quiet after the
// start check; a new profile replaces the list and the copy keeps the raw value
func TestSaveLmProfileUnreadableList(t *testing.T) {
	const raw = `[{"id":`
	site, buf := restoreTestSite(t)
	settings.SetString(keys.LmProfiles, raw)

	site.restoreLmProfiles()
	site.publishLmProfiles()
	site.publishLmProfiles()
	assert.Equal(t, 1, strings.Count(buf.String(), "ERROR"), buf.String())

	_, err := site.SaveLmProfile(profile.Profile{ID: "x", Name: "Alt", Icon: "sun"})
	assert.Error(t, err)
	assert.Error(t, site.DeleteLmProfile("x"))
	assert.Error(t, site.ApplyLmProfile("x"))
	stored, _ := settings.String(keys.LmProfiles)
	assert.Equal(t, raw, stored)

	p, err := site.SaveLmProfile(profile.Profile{Name: "Neu", Icon: "sun", GridCharge: bp(false)})
	require.NoError(t, err)
	profiles, err := lmProfilesLoad()
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.Equal(t, p.ID, profiles[0].ID)

	kept, _ := settings.String(keys.LmProfiles + invalidSuffix)
	assert.Equal(t, raw, kept)
}

// TestRestoreOutOfRange: a stored value its setter would refuse is ignored, the
// default applies and the key is deleted
func TestRestoreOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name, key, val string
		restore        func(*Site)
		get            func(*Site) float64
	}{
		{"limit 0", keys.PeakShavingLimit, "0", (*Site).restorePeakSettings, (*Site).GetPeakShavingLimit},
		{"limit 25000", keys.PeakShavingLimit, "25000", (*Site).restorePeakSettings, (*Site).GetPeakShavingLimit},
		{"reserve 0", keys.PeakShavingReserve, "0", (*Site).restorePeakSettings, (*Site).GetPeakShavingReserve},
		{"reserve 100", keys.PeakShavingReserve, "100", (*Site).restorePeakSettings, (*Site).GetPeakShavingReserve},
		{"charge power", keys.PeakShavingChargePower, "-5", (*Site).restorePeakSettings, (*Site).GetPeakShavingChargePower},
		{"guard 600", keys.LmShedGuard, "600", (*Site).restoreLmGuard, func(s *Site) float64 { return float64(s.GetLmShedGuard()) }},
		{"freeValue 0", keys.LmAdvanced, `{"freeValue":0}`, (*Site).restoreLmAdvanced, (*Site).peakFreeValue},
		{"phases 2", keys.LmAdvanced, `{"phases":2}`, (*Site).restoreLmAdvanced, func(s *Site) float64 { return float64(s.lmBatteryPhases()) }},
		{"follow buffer -100", keys.PeakFollowBuffer, "-100", (*Site).restorePeakFollow, (*Site).GetPeakFollowBuffer},
		{"soc start >= stop", keys.BatterySocGridChargeStart, "90", (*Site).restoreLmSettings, (*Site).GetBatterySocGridChargeStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site, buf := restoreTestSite(t)
			before := tc.get(site)
			if tc.key == keys.PeakFollowBuffer {
				before = defaultPeakFollowBuffer // restore sets it, the state starts at 0
			}

			if tc.key == keys.BatterySocGridChargeStart {
				settings.SetString(keys.BatterySocGridChargeStop, "50")
			}
			settings.SetString(tc.key, tc.val)

			tc.restore(site)

			assert.Equal(t, before, tc.get(site), "default applies")
			assert.Contains(t, buf.String(), "WARN", buf.String())
			assert.NotContains(t, buf.String(), "ERROR")
			if tc.key != keys.LmAdvanced {
				_, err := settings.String(tc.key)
				assert.ErrorIs(t, err, settings.ErrNotFound, "key deleted")
			} else {
				var adv lmAdvanced
				require.NoError(t, settings.Json(tc.key, &adv))
				assert.Equal(t, lmAdvanced{}, adv, "field left out")
			}
		})
	}

	t.Run("once target 150", func(t *testing.T) {
		site, buf := restoreTestSite(t)
		settings.SetString(keys.BatteryGridChargeOnce, `{"target":150}`)

		site.restoreGridChargeOnce()

		assert.Zero(t, site.gridChargeOnce().Target)
		assert.Contains(t, buf.String(), "WARN")
		_, err := settings.String(keys.BatteryGridChargeOnce)
		assert.ErrorIs(t, err, settings.ErrNotFound)
	})
}

// TestRestoreFollowLimitKept: follow the peak stores its limit in steps of 100 W,
// which is no multiple of the 500 W step of the setter
func TestRestoreFollowLimitKept(t *testing.T) {
	site, buf := restoreTestSite(t)
	settings.SetString(keys.PeakShavingLimit, "11300")

	site.restorePeakSettings()

	assert.Equal(t, 11300.0, site.GetPeakShavingLimit())
	assert.NotContains(t, buf.String(), "WARN")
}

// TestRestoreLiveValuesUnchanged: the values of the live system run through as they are
func TestRestoreLiveValuesUnchanged(t *testing.T) {
	site, buf := restoreTestSite(t)

	settings.SetString(keys.PeakShavingLimit, "3500")
	settings.SetString(keys.PeakShavingReserve, "20")
	settings.SetString(keys.PeakShavingChargePower, "1800")
	settings.SetString(keys.BatterySocGridChargeStart, "15")
	settings.SetString(keys.BatterySocGridChargeStop, "20")
	settings.SetString(keys.LmAdvanced, `{"freeValue":1800,"phases":1,"writeTolerance":50}`)
	settings.SetString(keys.BatteryGridChargeOnce, `{"target":0}`)

	site.restoreLmSettings()
	site.restorePeakSettings()
	site.restoreGridChargeOnce()

	assert.Equal(t, 3500.0, site.GetPeakShavingLimit())
	assert.Equal(t, 20.0, site.GetPeakShavingReserve())
	assert.Equal(t, 1800.0, site.GetPeakShavingChargePower())
	assert.Equal(t, 15.0, site.GetBatterySocGridChargeStart())
	assert.Equal(t, 20.0, site.GetBatterySocGridChargeStop())
	assert.Equal(t, 1800.0, site.peakFreeValue())
	assert.Equal(t, 1, site.lmBatteryPhases())
	assert.Equal(t, 50.0, site.peakWriteTolerance())

	assert.Empty(t, buf.String(), "nothing warned or logged")
	for _, key := range []string{keys.PeakShavingLimit, keys.PeakShavingReserve, keys.LmAdvanced, keys.BatteryGridChargeOnce} {
		_, err := settings.String(key)
		assert.NoError(t, err, key)
	}
}

// TestRestoreHomeWeekdayMoved: homeWeekday from an old backup becomes homeForecast
// once and is gone from the stored value
func TestRestoreHomeWeekdayMoved(t *testing.T) {
	site, _ := restoreTestSite(t)
	settings.SetString(keys.LmAdvanced, `{"homeWeekday":1,"holdOff":15}`)

	site.restoreLmAdvanced()

	assert.Equal(t, homeForecastWeekday, site.homeForecast())

	var adv lmAdvanced
	require.NoError(t, settings.Json(keys.LmAdvanced, &adv))
	assert.Nil(t, adv.HomeWeekday)
	require.NotNil(t, adv.HomeForecast)
	assert.Equal(t, 1.0, *adv.HomeForecast)
	require.NotNil(t, adv.HoldOff)

	raw, _ := settings.String(keys.LmAdvanced)
	assert.NotContains(t, raw, "homeWeekday")

	// an explicit homeForecast wins
	settings.SetString(keys.LmAdvanced, `{"homeWeekday":1,"homeForecast":2}`)
	site.lms().adv = lmAdvanced{}
	site.restoreLmAdvanced()
	assert.Equal(t, homeForecastManual, site.homeForecast())
}

