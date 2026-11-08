package core

import (
	"testing"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
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

// A stored switch "load management off" of an earlier version is removed at the
// start: the circuits keep their configured limits.
func TestLmOffDropped(t *testing.T) {
	site, main, sub := lmSwitchCircuits(t, 3500)
	settings.SetBool(keys.LmOff, true)

	site = &Site{log: site.log}
	site.dropLmCircuit()
	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.Equal(t, 7000.0, sub.GetMaxPower())

	_, err := settings.Bool(keys.LmOff)
	assert.Error(t, err, "setting removed")

	// off or not stored: nothing to do
	settings.SetBool(keys.LmOff, false)
	site.dropLmCircuit()
	_, err = settings.Bool(keys.LmOff)
	assert.Error(t, err)
	site.dropLmCircuit()

	// a value that cannot be read is removed as well
	settings.SetString(keys.LmOff, "maybe")
	site.dropLmCircuit()
	_, err = settings.String(keys.LmOff)
	assert.ErrorIs(t, err, settings.ErrNotFound, "unreadable value removed")
	assert.Equal(t, 10000.0, main.GetMaxPower())
}

// The stored load management (peak) circuit, also under the key of lm3/lm4, is
// removed at the start; a circuit that was changed at runtime starts with its
// configured limit again, the new code does not change a limit.
func TestLmCircuitDropped(t *testing.T) {
	site, main, sub := lmSwitchCircuits(t, 3500)
	settings.SetString(keys.LmCircuit, "main")
	settings.SetString(keys.PeakFollowCircuit, "garage")

	// the restart: the circuits come from their configuration
	site = &Site{log: site.log, batteryMeters: site.batteryMeters}
	site.dropLmCircuit()

	for _, key := range []string{keys.LmCircuit, keys.PeakFollowCircuit} {
		_, err := settings.String(key)
		assert.ErrorIs(t, err, settings.ErrNotFound, key)
	}

	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.Equal(t, 7000.0, sub.GetMaxPower())

	// nothing stored: nothing to do
	site.dropLmCircuit()
}

// restoreCustom with the old settings stored: the circuits have their limit, the
// peak limit does not reach them, the circuit name is gone from the store.
func TestLmCircuitDroppedAtRestore(t *testing.T) {
	noSettingsDB(t)
	freshSettings(t)
	a, main, _ := lmSwitchCircuits(t, 3500)

	settings.SetString(keys.LmCircuit, "main")
	settings.SetString(keys.LmAdvanced, `{"circuitFromPeak":true,"holdOff":15}`)
	settings.SetBool(keys.PeakShaving, true)
	settings.SetFloat(keys.PeakShavingLimit, 4000)
	useLogFileDir(t)

	b := &Site{log: a.log, batteryMeters: a.batteryMeters}
	b.restoreCustom()
	b.updateCustom(siteState{})

	assert.Equal(t, 10000.0, main.GetMaxPower(), "the peak limit does not reach the circuit")
	_, err := settings.String(keys.LmCircuit)
	assert.ErrorIs(t, err, settings.ErrNotFound)

	raw, err := settings.String(keys.LmAdvanced)
	require.NoError(t, err)
	assert.NotContains(t, raw, "circuitFromPeak")
	assert.Contains(t, raw, "holdOff", "the other values stay")
	assert.False(t, b.advanced().CircuitFromPeak)
}

// The setting is no longer used, but an old client that sets it gets no error.
func TestLmAdvancedCircuitFromPeakIgnored(t *testing.T) {
	site, main, _ := lmSwitchCircuits(t, 3500)

	require.NoError(t, site.SetLmAdvanced("circuitFromPeak", 1))
	assert.False(t, site.advanced().CircuitFromPeak)
	assert.Equal(t, 10000.0, main.GetMaxPower())
	require.Error(t, site.SetLmAdvanced("circuitFromPeak", 2))
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
