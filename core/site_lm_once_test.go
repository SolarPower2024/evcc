package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capacityBattery struct {
	scenarioBattery
	kWh float64
}

func (b *capacityBattery) Capacity() float64 { return b.kWh }

func onceSite(t *testing.T, soc float64) *Site {
	t.Helper()
	settings.SetJson(keys.BatteryGridChargeOnce, gridChargeOnce{})
	t.Cleanup(func() { settings.SetJson(keys.BatteryGridChargeOnce, gridChargeOnce{}) })

	site := &Site{
		log:           util.NewLogger("test"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(&capacityBattery{kWh: 10}))},
	}
	site.battery.Soc = soc
	site.LoadManagement.Battery.Power = 5000
	return site
}

// Right away: charges until the target, then switches itself off.
func TestGridChargeOnceRightAway(t *testing.T) {
	site := onceSite(t, 40)

	require.Error(t, site.SetBatteryGridChargeOnce(30, time.Time{}), "already above")
	require.Error(t, site.SetBatteryGridChargeOnce(80, time.Now().Add(-time.Hour)), "time passed")
	require.NoError(t, site.SetBatteryGridChargeOnce(80, time.Time{}))

	assert.True(t, site.batteryGridChargeRequested(api.Rate{}))
	assert.True(t, site.gridChargeOnce().Active)

	// survives a restart
	restarted := onceSite(t, 40)
	settings.SetJson(keys.BatteryGridChargeOnce, site.gridChargeOnce())
	restarted.restoreGridChargeOnce()
	assert.Equal(t, 80.0, restarted.gridChargeOnce().Target)

	site.battery.Soc = 80
	assert.False(t, site.batteryGridChargeRequested(api.Rate{}))
	assert.Zero(t, site.gridChargeOnce().Target, "ended at the target")
}

// By a time: waits for the cheapest slots, charges right away once the time
// has passed, and can be cancelled.
func TestGridChargeOnceByTime(t *testing.T) {
	site := onceSite(t, 40)

	// 40% -> 80% of 10 kWh at 5 kW takes 48 min: far ahead without a tariff the
	// simple plan starts just before the time
	require.NoError(t, site.SetBatteryGridChargeOnce(80, time.Now().Add(6*time.Hour)))
	assert.Equal(t, 48*time.Minute, site.onceRequiredDuration(80, 40).Round(time.Minute))
	assert.False(t, site.batteryGridChargeOnceActive(), "not yet")

	// the time passed without reaching the target
	o := site.gridChargeOnce()
	o.Until = time.Now().Add(-time.Minute)
	site.setGridChargeOnce(o)
	assert.True(t, site.batteryGridChargeOnceActive(), "late: right away")

	require.NoError(t, site.CancelBatteryGridChargeOnce())
	assert.False(t, site.batteryGridChargeOnceActive())
}

// As optimizer input: the target as goal, right away at the earliest step.
func TestGridChargeOnceOptimizerGoal(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	site := onceSite(t, 40)
	require.NoError(t, site.SetBatteryGridChargeOnce(80, time.Time{}))

	req := optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{Dt: quarterHours(16)}}
	batteries := []optimizerBattery{homeBattery()}
	batteries[0].cfg.SInitial = 4000

	site.applyLmOptimizerInputs(&req, batteries)

	goal := batteries[0].cfg.SGoal
	require.Len(t, goal, 16)
	assert.Equal(t, float32(8000), goal[3], "48 min at 5 kW: the 4th quarter hour")
}
