package core

import (
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func automaticSite(t *testing.T, action string) *Site {
	t.Helper()
	enableAutomatic(t)

	bat := &scenarioBattery{}
	site := &Site{
		log:           util.NewLogger("test"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(bat))},
	}
	if action != "" {
		site.setSuggestions(map[string]types.Suggestion{batteryKey("bat"): {Action: action}})
	}
	return site
}

// The optimizer's charge request passes the fork's gate.
func TestLmGateOptimizerCharge(t *testing.T) {
	site := automaticSite(t, api.BatteryCharge.String())
	require.True(t, site.optimizerInControl())

	assert.Equal(t, api.BatteryCharge, site.lmGateBatteryMode(api.BatteryCharge, false), "allowed")

	// a running peak refuses it
	setPeakShaving(site, 7000, 30)
	site.peak().demand = 9000
	assert.Equal(t, api.BatteryHold, site.lmGateBatteryMode(api.BatteryCharge, false), "held during a peak")

	// the fork's own grid charge flag does not decide while the optimizer is in control
	assert.False(t, site.batteryGridChargeRequested(api.Rate{}))
}

// Hold gives way to normal while a peak has to be covered.
func TestLmGateOptimizerHoldDuringPeak(t *testing.T) {
	site := automaticSite(t, api.BatteryHold.String())

	assert.Equal(t, api.BatteryHold, site.lmGateBatteryMode(api.BatteryHold, false), "no peak shaving")

	setPeakShaving(site, 7000, 30)
	s := site.peak()
	s.demand, s.allowed = 6000, 7000
	assert.Equal(t, api.BatteryHold, site.lmGateBatteryMode(api.BatteryHold, false), "no peak")
	assert.True(t, site.optimizerHolds())

	s.demand = 9000
	assert.Equal(t, api.BatteryNormal, site.lmGateBatteryMode(api.BatteryHold, false), "covering a peak")
}

// Without a current result the fork's grid charging applies as without the optimizer.
func TestLmGateOptimizerStale(t *testing.T) {
	site := automaticSite(t, "")
	require.False(t, site.optimizerInControl())

	assert.Equal(t, api.BatteryCharge, site.lmGateBatteryMode(api.BatteryNormal, true))
	assert.Equal(t, api.BatteryNormal, site.lmGateBatteryMode(api.BatteryNormal, false))
}

// Outside automatic mode the gate passes upstream's decision unchanged.
func TestLmGateInertWithoutAutomatic(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	for _, m := range []api.BatteryMode{api.BatteryUnknown, api.BatteryNormal, api.BatteryHold, api.BatteryCharge} {
		assert.Equal(t, m, site.lmGateBatteryMode(m, true))
	}
}
