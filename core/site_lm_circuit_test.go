package core

import (
	"testing"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lmSwitchCircuits adds the circuits "main" (10 kW) and "garage" (7 kW) and
// returns a site with a battery and peak shaving on at limit
func lmSwitchCircuits(t *testing.T, limit float64) (*Site, *circuit.Circuit, *circuit.Circuit) {
	t.Helper()

	noSettingsDB(t)
	keepSettings(t)
	config.Reset()
	t.Cleanup(config.Reset)

	log := util.NewLogger("test")
	main, err := circuit.New(log, "peak", 32, 10000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(main))))
	sub, err := circuit.New(log, "garage", 0, 7000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "garage"}, api.Circuit(sub))))

	site := &Site{
		log:           log,
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(&scenarioBattery{}))},
	}
	site.peak().clock = clock.NewMock()
	setPeakShaving(site, limit, 20)

	return site, main, sub
}

// Peak shaving never touches a circuit's power limit: not with a changed peak
// limit and not with peak shaving on or off.
func TestCircuitLimitsStayConfigured(t *testing.T) {
	site, main, sub := lmSwitchCircuits(t, 3500)

	require.NoError(t, site.SetPeakShavingLimit(5000))
	assert.Equal(t, 10000.0, main.GetMaxPower())
	require.NoError(t, site.SetPeakShaving(false))
	require.NoError(t, site.SetPeakShaving(true))
	site.updateCustom(siteState{})
	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.Equal(t, 7000.0, sub.GetMaxPower())
	assert.Equal(t, 32.0, main.GetMaxCurrent())
}

// The battery's circuit that is gone is removed with the next cycle: the circuits
// that exist are not touched.
func TestBatteryCircuitDeleted(t *testing.T) {
	site, main, _ := lmSwitchCircuits(t, 3500)
	require.NoError(t, site.SetPeakShavingCircuit("garage"))
	require.NoError(t, config.Circuits().Delete("garage"))
	assert.Equal(t, "garage", site.GetPeakShavingCircuit(), "the name stays until the cycle")

	site.updateCustom(siteState{})
	assert.Empty(t, site.GetPeakShavingCircuit())
	assert.Equal(t, 10000.0, main.GetMaxPower())
}
