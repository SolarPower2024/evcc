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
		keys.LmProfiles:            (*Site).publishLmProfiles,
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
	site.publishLmProfiles()

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

// TestSaveLmProfileUnreadableList: saving or deleting on top of an unreadable list
// fails and leaves the raw value
func TestSaveLmProfileUnreadableList(t *testing.T) {
	site, _ := restoreTestSite(t)
	settings.SetString(keys.LmProfiles, `[{"id":`)

	_, err := site.SaveLmProfile(profile.Profile{Name: "Neu", Icon: "sun", GridCharge: bp(false)})
	assert.Error(t, err)
	assert.Error(t, site.DeleteLmProfile("x"))
	assert.Error(t, site.ApplyLmProfile("x"))

	raw, _ := settings.String(keys.LmProfiles)
	assert.Equal(t, `[{"id":`, raw)
}
