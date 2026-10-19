package core

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db"
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

// newOnceSite is a site with a battery at soc and the charge power set, the
// stored order is left as it is
func newOnceSite(t *testing.T, soc float64) *Site {
	t.Helper()

	site := &Site{
		log:           util.NewLogger("test"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(&capacityBattery{kWh: 10}))},
	}
	setBatterySoc(site, soc)
	site.peak().chargePower = 5000
	return site
}

// onceSite is a new site with no stored order, which is cleared again after the test
func onceSite(t *testing.T, soc float64) *Site {
	t.Helper()
	settings.SetJson(keys.BatteryGridChargeOnce, gridChargeOnce{})
	t.Cleanup(func() { settings.SetJson(keys.BatteryGridChargeOnce, gridChargeOnce{}) })

	return newOnceSite(t, soc)
}

// Right away: charges until the target, then switches itself off.
func TestGridChargeOnceRightAway(t *testing.T) {
	noSettingsDB(t)
	site := onceSite(t, 40)

	require.Error(t, site.SetBatteryGridChargeOnce(30, time.Time{}), "already above")
	require.Error(t, site.SetBatteryGridChargeOnce(80, time.Now().Add(-time.Hour)), "time passed")
	require.NoError(t, site.SetBatteryGridChargeOnce(80, time.Time{}))

	assert.True(t, site.batteryGridChargeRequested(api.Rate{}))
	assert.True(t, site.gridChargeOnce().Active)

	// survives a restart: what the setter stored, not what the test sets
	restarted := newOnceSite(t, 40)
	restarted.restoreGridChargeOnce()
	assert.Equal(t, 80.0, restarted.gridChargeOnce().Target)

	setBatterySoc(site, 80)
	assert.False(t, site.batteryGridChargeRequested(api.Rate{}))
	assert.Zero(t, site.gridChargeOnce().Target, "ended at the target")
}

// The last chosen target stays after the end, a cancel and a restart, as the
// preset for the next start on the battery page.
func TestGridChargeOnceKeepsLastTarget(t *testing.T) {
	noSettingsDB(t)
	site := onceSite(t, 40)
	assert.Zero(t, site.gridChargeOnce().Last, "never started")

	require.NoError(t, site.SetBatteryGridChargeOnce(65, time.Time{}))
	setBatterySoc(site, 65)
	assert.False(t, site.batteryGridChargeOnceActive())
	assert.Zero(t, site.gridChargeOnce().Target, "ended at the target")
	assert.Equal(t, 65.0, site.gridChargeOnce().Last, "kept after the end")

	setBatterySoc(site, 40)
	require.NoError(t, site.SetBatteryGridChargeOnce(90, time.Now().Add(time.Hour)))
	require.NoError(t, site.CancelBatteryGridChargeOnce())
	assert.Equal(t, 90.0, site.gridChargeOnce().Last, "kept after a cancel")

	var stored gridChargeOnce
	require.NoError(t, settings.Json(keys.BatteryGridChargeOnce, &stored))
	assert.Equal(t, 90.0, stored.Last, "stored")
	restarted := &Site{log: util.NewLogger("test")}
	restarted.restoreGridChargeOnce()
	assert.Equal(t, 90.0, restarted.gridChargeOnce().Last, "kept across a restart")
	assert.Zero(t, restarted.gridChargeOnce().Target)
}

// By a time: waits for the cheapest slots, charges right away once the time
// has passed, and can be cancelled.
func TestGridChargeOnceByTime(t *testing.T) {
	noSettingsDB(t)
	site := onceSite(t, 40)

	// 40% -> 80% of 10 kWh at 5 kW with 90% efficiency takes 53 min: far ahead without a tariff the
	// simple plan starts just before the time
	require.NoError(t, site.SetBatteryGridChargeOnce(80, time.Now().Add(6*time.Hour)))
	assert.Equal(t, 53*time.Minute, site.onceRequiredDuration(80, 40).Round(time.Minute))
	assert.False(t, site.batteryGridChargeOnceActive(), "not yet")

	// the time passed without reaching the target
	o := site.gridChargeOnce()
	o.Until = time.Now().Add(-time.Minute)
	site.setGridChargeOnce(o)
	assert.True(t, site.batteryGridChargeOnceActive(), "late: right away")

	require.NoError(t, site.CancelBatteryGridChargeOnce())
	assert.False(t, site.batteryGridChargeOnceActive())
}

// Without a battery (removed after the order) the order ends instead of staying
// active at soc 0 forever.
func TestGridChargeOnceWithoutBattery(t *testing.T) {
	noSettingsDB(t)
	site := onceSite(t, 40)
	require.NoError(t, site.SetBatteryGridChargeOnce(80, time.Time{}))

	site.batteryMeters = nil
	site.battery.Soc = 0

	assert.False(t, site.batteryGridChargeOnceActive())
	assert.Zero(t, site.gridChargeOnce().Target, "cancelled")
}

// As optimizer input: the target as goal, right away at the earliest step.
func TestGridChargeOnceOptimizerGoal(t *testing.T) {
	noSettingsDB(t)
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

// An order whose time is long past is ended after a restart and in the cycle; one
// that is only late still charges right away (the peak gate may have held it back).
func TestGridChargeOnceExpired(t *testing.T) {
	noSettingsDB(t)

	now := time.Now()
	assert.True(t, onceExpired(gridChargeOnce{Target: 80, Until: now.Add(-50 * time.Hour)}, now))
	assert.True(t, onceExpired(gridChargeOnce{Target: 80, Until: now.Add(-maxGridChargeOnceLate - time.Minute)}, now))
	assert.False(t, onceExpired(gridChargeOnce{Target: 80, Until: now.Add(-10 * time.Minute)}, now))
	assert.False(t, onceExpired(gridChargeOnce{Target: 80, Until: now.Add(time.Hour)}, now))
	assert.False(t, onceExpired(gridChargeOnce{Target: 80}, now), "right away has no time")
	assert.False(t, onceExpired(gridChargeOnce{Until: now.Add(-50 * time.Hour)}, now), "no order")

	restore := func(until time.Duration) *Site {
		site := onceSite(t, 40)
		settings.SetJson(keys.BatteryGridChargeOnce, gridChargeOnce{Target: 80, Until: time.Now().Add(until), Active: true})
		site.restoreGridChargeOnce()
		return site
	}

	site := restore(-50 * time.Hour)
	assert.Zero(t, site.gridChargeOnce().Target, "ended after the restart")

	var stored gridChargeOnce
	require.NoError(t, settings.Json(keys.BatteryGridChargeOnce, &stored))
	assert.Zero(t, stored.Target, "and not restored again")

	site = restore(-10 * time.Minute)
	assert.Equal(t, 80.0, site.gridChargeOnce().Target, "late: carries on")
	assert.True(t, site.batteryGridChargeOnceActive(), "late: right away")

	// in the cycle
	site = onceSite(t, 40)
	site.lms().gridOnce = gridChargeOnce{Target: 80, Until: time.Now().Add(-50 * time.Hour), Active: true}
	assert.False(t, site.batteryGridChargeOnceActive())
	assert.Zero(t, site.gridChargeOnce().Target, "ended in the cycle")

	// no soc read yet: the time decides all the same
	site = onceSite(t, 40)
	site.battery.Devices = nil
	site.lms().gridOnce = gridChargeOnce{Target: 80, Until: time.Now().Add(-50 * time.Hour)}
	assert.False(t, site.batteryGridChargeOnceActive())
	assert.Zero(t, site.gridChargeOnce().Target)
}

// The order survives a restart through a database file: stored by the setter,
// written out, the database opened anew, the settings read back from the file.
func TestGridChargeOnceSurvivesRestartOnDisk(t *testing.T) {
	noSettingsDB(t)

	// the settings in memory come back after the test, the file database replaces them
	before := settings.All()
	t.Cleanup(func() {
		if db.Instance != nil {
			if sqlDB, err := db.Instance.DB(); err == nil {
				sqlDB.Close()
			}
		}
		db.Instance = nil
		for _, s := range settings.All() {
			_ = settings.Delete(s.Key)
		}
		for _, s := range before {
			settings.SetString(s.Key, s.Value)
		}
	})

	path := filepath.Join(t.TempDir(), "evcc.db")
	require.NoError(t, db.NewInstance("sqlite", path))

	site := newOnceSite(t, 40)
	require.NoError(t, site.SetBatteryGridChargeOnce(80, time.Now().Add(6*time.Hour)))
	require.NoError(t, settings.Persist())

	// the restart: close, open the same file, the settings load from it
	sqlDB, err := db.Instance.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	require.NoError(t, db.NewInstance("sqlite", path))

	restarted := newOnceSite(t, 40)
	restarted.restoreGridChargeOnce()
	assert.Equal(t, 80.0, restarted.gridChargeOnce().Target)
	assert.False(t, restarted.gridChargeOnce().Until.IsZero())
}
