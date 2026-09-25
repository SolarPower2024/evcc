package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
)

// TestForkInertWhenUnused pins that the fork's battery hooks in site.update
// behave exactly like upstream while none of its features is set up: the grid
// charge decision is upstream's price limit, the battery mode upstream's, and
// nothing is written anywhere.
func TestForkInertWhenUnused(t *testing.T) {
	lm.Reset()
	t.Cleanup(lm.Reset)

	bat := &scenarioBattery{}
	site := &Site{
		log:           util.NewLogger("test"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(bat))},
	}

	limit := 0.20
	site.batteryGridChargeLimit = &limit

	for _, tc := range []struct {
		price    float64
		wantMode api.BatteryMode
	}{
		{0.15, api.BatteryCharge},
		{0.30, api.BatteryNormal},
	} {
		rate := api.Rate{Value: tc.price}
		rate.Start, rate.End = site.peak().clock.Now(), site.peak().clock.Now().Add(15*time.Minute)

		site.updatePeakShaving(site.state())
		got := site.batteryGridChargeRequested(rate)
		assert.Equal(t, site.batteryGridChargeActive(rate), got, "price %.2f: upstream's decision", tc.price)

		site.updateBatteryModePeakAware(got, false, rate)
		assert.Equal(t, tc.wantMode, site.GetBatteryMode(), "price %.2f", tc.price)
	}

	assert.False(t, site.peakShavingActive())
	assert.Equal(t, []api.BatteryMode{api.BatteryCharge, api.BatteryNormal}, bat.modes, "only upstream's modes applied")
}
