package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLmAdvancedSettings(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}

	// defaults
	assert.Equal(t, lm.DefaultHoldOff, site.lmHoldOff())
	assert.Equal(t, lm.DefaultTimeout, site.lmTimeout())
	assert.Equal(t, lm.DefaultPhases, site.lmBatteryPhases())
	assert.Equal(t, 12*time.Minute, site.peakFreeze())
	assert.Equal(t, 2.0, site.peakCap())
	assert.Equal(t, 0.0, site.peakWriteTolerance())

	// set in the ui
	require.NoError(t, site.SetLmAdvanced("hysteresis", 0)) // accepted, without effect
	require.NoError(t, site.SetLmAdvanced("holdOff", 10))
	require.NoError(t, site.SetLmAdvanced("timeout", 3))
	require.NoError(t, site.SetLmAdvanced("phases", 1))
	require.NoError(t, site.SetLmAdvanced("peakFreeze", 10))
	require.NoError(t, site.SetLmAdvanced("peakCap", 1.5))
	require.NoError(t, site.SetLmAdvanced("writeTolerance", 50))

	assert.Nil(t, site.advanced().Hysteresis, "no setting any more")
	assert.Equal(t, peak.DefaultHysteresis, site.peakHysteresis(), "the band of the integration")
	assert.Equal(t, 10*time.Minute, site.lmHoldOff())
	assert.Equal(t, 3*time.Minute, site.lmTimeout())
	assert.Equal(t, 1, site.lmBatteryPhases())
	assert.Equal(t, 10*time.Minute, site.peakFreeze())
	assert.Equal(t, 1.5, site.peakCap())
	assert.Equal(t, 50.0, site.peakWriteTolerance())

	// the free value is no setting any more: accepted, without effect
	require.NoError(t, site.SetLmAdvanced("freeValue", 12000))
	assert.Nil(t, site.advanced().FreeValue)

	for _, tc := range []struct {
		name  string
		value float64
	}{
		{"hysteresis", 21},
		{"freeValue", 0},
		{"freeValue", 1.5},
		{"holdOff", 0},
		{"holdOff", 61},
		{"timeout", 0.5},
		{"phases", 2},
		{"phases", 4},
		{"peakFreeze", 0},
		{"peakFreeze", 15},
		{"peakFreeze", 12.5},
		{"peakCap", 0.9},
		{"peakCap", 11},
		{"writeTolerance", 1001},
		{"writeTolerance", 2.5},
		{"circuitFromPeak", 2},
		{"circuitFromPeak", 0.5},
		{"unknown", 1},
	} {
		assert.Error(t, site.SetLmAdvanced(tc.name, tc.value), "%s = %g", tc.name, tc.value)
	}

	// rejected values leave the setting alone
	assert.Equal(t, 10*time.Minute, site.lmHoldOff())
}

// TestLmAdvancedHysteresisDropped verifies that a stored hysteresis is accepted and
// dropped at start: the band is fixed, see bydHysteresis
func TestLmAdvancedHysteresisDropped(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)

	require.NoError(t, settings.SetJson(keys.LmAdvanced, map[string]float64{"hysteresis": 5, "holdOff": 7}))

	site := &Site{log: util.NewLogger("test")}
	site.restoreLmAdvanced()

	assert.Equal(t, peak.DefaultHysteresis, site.peakHysteresis())
	assert.Equal(t, 7*time.Minute, site.lmHoldOff(), "the other settings stay")

	var stored map[string]float64
	require.NoError(t, settings.Json(keys.LmAdvanced, &stored))
	assert.NotContains(t, stored, "hysteresis")
	assert.Contains(t, stored, "holdOff")
}
