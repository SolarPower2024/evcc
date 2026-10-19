package core

import (
	"testing"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBatteryGridChargeMode: soc clears evcc's price limit, price switches the
// soc-based charging off and keeps its thresholds, both changes nothing
func TestBatteryGridChargeMode(t *testing.T) {
	noSettingsDB(t)
	site := newScenario(t).site

	assert.Equal(t, gridModeBoth, site.GetBatteryGridChargeMode(), "default")

	limit := 0.2
	require.NoError(t, site.SetBatteryGridChargeLimit(&limit))
	require.NoError(t, site.SetBatterySocGridChargeStart(30))
	require.NoError(t, site.SetBatterySocGridChargeStop(80))

	require.NoError(t, site.SetBatteryGridChargeMode(gridModeBoth))
	assert.NotNil(t, site.GetBatteryGridChargeLimit(), "both: limit stays")
	assert.True(t, site.GetBatterySocGridCharge(), "both: soc charging stays")

	require.NoError(t, site.SetBatteryGridChargeMode(gridModeSoc))
	assert.Equal(t, gridModeSoc, site.GetBatteryGridChargeMode())
	assert.Nil(t, site.GetBatteryGridChargeLimit(), "soc: limit cleared")
	assert.True(t, site.GetBatterySocGridCharge(), "soc: soc charging stays")

	require.NoError(t, site.SetBatteryGridChargeMode(gridModePrice))
	assert.Equal(t, gridModePrice, site.GetBatteryGridChargeMode())
	assert.False(t, site.GetBatterySocGridCharge(), "price: soc charging off")
	assert.Equal(t, 30.0, site.GetBatterySocGridChargeStart(), "thresholds kept")
	assert.Equal(t, 80.0, site.GetBatterySocGridChargeStop(), "thresholds kept")

	assert.Error(t, site.SetBatteryGridChargeMode("cheap"))
	assert.Equal(t, gridModePrice, site.GetBatteryGridChargeMode(), "refused value changes nothing")
}

// TestBatteryGridChargeModeRestart: the choice comes back after a restart and the
// restore clears and switches nothing
func TestBatteryGridChargeModeRestart(t *testing.T) {
	noSettingsDB(t)
	sc := newScenario(t)
	site := sc.site

	require.NoError(t, site.SetBatteryGridChargeMode(gridModeSoc))

	limit := 0.2
	require.NoError(t, site.SetBatteryGridChargeLimit(&limit))
	require.NoError(t, site.SetBatterySocGridCharge(true))

	restarted := newScenario(t).site
	restarted.restoreBatteryGridChargeMode()

	assert.Equal(t, gridModeSoc, restarted.GetBatteryGridChargeMode())
	assert.True(t, restarted.GetBatterySocGridCharge(), "restore switches nothing")

	require.NoError(t, site.SetBatteryGridChargeMode(gridModePrice))
	restarted.restoreBatteryGridChargeMode()
	assert.Equal(t, gridModePrice, restarted.GetBatteryGridChargeMode())
	assert.True(t, restarted.GetBatterySocGridCharge(), "restore switches nothing")
}

// TestBatteryGridChargeModeInvalidStored: an unknown stored value warns, is
// deleted and both applies
func TestBatteryGridChargeModeInvalidStored(t *testing.T) {
	site, buf := restoreTestSite(t)
	settings.SetString(keys.BatteryGridChargeMode, "cheap")

	site.restoreBatteryGridChargeMode()

	assert.Equal(t, gridModeBoth, site.GetBatteryGridChargeMode())
	assert.Contains(t, buf.String(), "WARN", buf.String())
	assert.NotContains(t, buf.String(), "ERROR")
	_, err := settings.String(keys.BatteryGridChargeMode)
	assert.ErrorIs(t, err, settings.ErrNotFound, "key deleted")
}

// TestBatteryGridChargeModeFresh: nothing stored, nothing changes
func TestBatteryGridChargeModeFresh(t *testing.T) {
	site, buf := restoreTestSite(t)
	_ = settings.Delete(keys.BatteryGridChargeMode)

	site.restoreBatteryGridChargeMode()

	assert.Equal(t, gridModeBoth, site.GetBatteryGridChargeMode())
	assert.Empty(t, buf.String())
}
