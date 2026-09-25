package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLmAdvancedSettings(t *testing.T) {
	lm.Reset()
	t.Cleanup(lm.Reset)

	site := &Site{log: util.NewLogger("test")}

	// defaults
	assert.Equal(t, lm.DefaultHysteresis, site.peakHysteresis())
	assert.Equal(t, lm.DefaultFreeValue, site.peakFreeValue())
	assert.Equal(t, lm.DefaultHoldOff, site.lmHoldOff())
	assert.Equal(t, lm.DefaultTimeout, site.lmTimeout())
	assert.Equal(t, lm.DefaultPhases, site.lmBatteryPhases())
	assert.Equal(t, 12*time.Minute, site.peakFreeze())
	assert.Equal(t, 2.0, site.peakCap())

	// yaml overrides the default
	site.LoadManagement.PeakShaving.FreeValue = 8000
	site.LoadManagement.Battery.HoldOff = 2 * time.Minute
	assert.Equal(t, 8000.0, site.peakFreeValue())
	assert.Equal(t, 2*time.Minute, site.lmHoldOff())

	// the ui overrides both
	require.NoError(t, site.SetLmAdvanced("hysteresis", 0))
	require.NoError(t, site.SetLmAdvanced("freeValue", 12000))
	require.NoError(t, site.SetLmAdvanced("holdOff", 10))
	require.NoError(t, site.SetLmAdvanced("timeout", 3))
	require.NoError(t, site.SetLmAdvanced("phases", 1))
	require.NoError(t, site.SetLmAdvanced("peakFreeze", 10))
	require.NoError(t, site.SetLmAdvanced("peakCap", 1.5))

	assert.Equal(t, 0.0, site.peakHysteresis(), "0 is a valid value, not unset")
	assert.Equal(t, 12000.0, site.peakFreeValue())
	assert.Equal(t, 10*time.Minute, site.lmHoldOff())
	assert.Equal(t, 3*time.Minute, site.lmTimeout())
	assert.Equal(t, 1, site.lmBatteryPhases())
	assert.Equal(t, 10*time.Minute, site.peakFreeze())
	assert.Equal(t, 1.5, site.peakCap())

	// the free value is what peak shaving writes while off
	var written []float64
	s := site.peak()
	s.set = func(v float64) error { written = append(written, v); return nil }
	site.handBackPeak()
	assert.Equal(t, []float64{12000}, written)

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
		{"unknown", 1},
	} {
		assert.Error(t, site.SetLmAdvanced(tc.name, tc.value), "%s = %g", tc.name, tc.value)
	}

	// rejected values leave the setting alone
	assert.Equal(t, 12000.0, site.peakFreeValue())
}
