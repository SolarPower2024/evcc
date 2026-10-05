package core

import (
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/evcc-io/evcc/util/logstash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRestoreCustomAfterRestart: every fork setting set through the api is
// back after a restart, read by restoreCustom as evcc's restoreSettings calls
// it. The Home Assistant entities are stored as such; without a Home Assistant
// connection in the test their outputs stay unresolved, the names are kept.
func TestRestoreCustomAfterRestart(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)
	config.Reset()
	t.Cleanup(config.Reset)

	useLogFileDir(t)

	main, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(main))))

	newSite := func() *Site {
		return &Site{
			log:           util.NewLogger("test"),
			batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(&scenarioBattery{}))},
		}
	}

	a := newSite()
	require.NoError(t, a.SetBatterySocGridCharge(true))
	require.NoError(t, a.SetBatterySocGridChargeStart(25))
	require.NoError(t, a.SetBatterySocGridChargeStop(85))
	require.NoError(t, a.SetLmShedGuard(10))
	require.NoError(t, a.SetLmCircuit("main"))
	require.NoError(t, a.SetLmEnabled(false))
	require.NoError(t, a.SetBatteryIdentUse(true))
	require.NoError(t, a.SetLmAdvanced("hysteresis", 3))
	require.NoError(t, a.SetPeakShavingLimit(7000))
	require.NoError(t, a.SetPeakShavingReserve(40))
	require.NoError(t, a.SetPeakShavingChargePower(4000))
	require.NoError(t, a.SetPeakShavingCircuit("main"))
	require.NoError(t, a.SetPeakFollowBuffer(1000))
	require.NoError(t, a.SetPeakFollow(true))
	require.NoError(t, a.SetPeakTariff("price", 40))
	require.NoError(t, a.SetLogFile(logstash.FileConfig{Enabled: true, Level: "info", Days: 30}))
	require.NoError(t, a.SetSnowCover(true))
	require.NoError(t, a.SetSnowAuto(true))

	// set through Home Assistant in the ui, stored as names
	settings.SetString(keys.PeakShavingEntity, "input_number.peak")
	settings.SetString(keys.PeakShavingChargeEntity, "input_number.charge")
	settings.SetString(keys.PeakShavingEnergyEntity, "sensor.grid_import")
	settings.SetString(keys.FeedInEegEntity, "sensor.eeg_export")
	settings.SetBool(keys.PeakShaving, true)

	b := newSite()
	b.restoreCustom()

	// a meter outage is counted from the start, see peakCheckMeters
	assert.False(t, b.peak().updated.IsZero())

	assert.True(t, b.GetBatterySocGridCharge())
	assert.Equal(t, 25.0, b.GetBatterySocGridChargeStart())
	assert.Equal(t, 85.0, b.GetBatterySocGridChargeStop())
	assert.Equal(t, 10, b.GetLmShedGuard())
	assert.Equal(t, "main", b.GetLmCircuit())
	assert.False(t, b.GetLmEnabled())
	assert.True(t, b.GetBatteryIdentUse())
	assert.Equal(t, 3.0, b.peakHysteresis())
	assert.Equal(t, 7000.0, b.GetPeakShavingLimit(), "the base, not a followed limit")
	assert.Equal(t, 40.0, b.GetPeakShavingReserve())
	assert.Equal(t, 4000.0, b.GetPeakShavingChargePower())
	assert.Equal(t, "main", b.GetPeakShavingCircuit())
	assert.True(t, b.GetPeakFollow())
	assert.Equal(t, 1000.0, b.GetPeakFollowBuffer())
	assert.Equal(t, 40.0, b.peak().tariff.Price)
	assert.True(t, b.GetPeakShaving())
	assert.Equal(t, "input_number.peak", b.GetPeakShavingEntity())
	assert.Equal(t, "input_number.charge", b.GetPeakShavingChargeEntity())
	assert.Equal(t, "sensor.grid_import", b.GetPeakShavingEnergyEntity())
	assert.Equal(t, "sensor.eeg_export", b.GetFeedInEegEntity())
	assert.True(t, b.LogFile().Enabled)
	assert.Equal(t, "info", b.LogFile().Level)
	assert.Equal(t, 30, b.LogFile().Days)
	assert.True(t, b.GetSnowCover())
	assert.True(t, b.GetSnowAuto())
}
