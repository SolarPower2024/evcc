package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/core/peak"
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

	// set in the ui
	require.NoError(t, site.SetLmAdvanced("holdOff", 10))
	require.NoError(t, site.SetLmAdvanced("timeout", 3))
	require.NoError(t, site.SetLmAdvanced("phases", 1))
	require.NoError(t, site.SetLmAdvanced("peakFreeze", 10))

	assert.Equal(t, peak.DefaultHysteresis, site.peakHysteresis(), "the band of the integration")
	assert.Equal(t, 10*time.Minute, site.lmHoldOff())
	assert.Equal(t, 3*time.Minute, site.lmTimeout())
	assert.Equal(t, 1, site.lmBatteryPhases())
	assert.Equal(t, 10*time.Minute, site.peakFreeze())

	for _, tc := range []struct {
		name  string
		value float64
	}{
		{"hysteresis", 2},
		{"freeValue", 12000},
		{"holdOff", 0},
		{"holdOff", 61},
		{"timeout", 0.5},
		{"phases", 2},
		{"phases", 4},
		{"peakFreeze", 0},
		{"peakFreeze", 15},
		{"peakFreeze", 12.5},
		{"peakCap", 2},
		{"writeTolerance", 50},
		{"circuitFromPeak", 1},
		{"homeWeekday", 1},
		{"followCycles", 3},
		{"unknown", 1},
	} {
		assert.Error(t, site.SetLmAdvanced(tc.name, tc.value), "%s = %g", tc.name, tc.value)
	}

	// rejected values leave the setting alone
	assert.Equal(t, 10*time.Minute, site.lmHoldOff())
}
