package core

import (
	"testing"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/core/loadpoint"
	coresettings "github.com/evcc-io/evcc/core/settings"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func addTestLoadpoint(t *testing.T, name string, prio int) *Loadpoint {
	t.Helper()
	lp := NewLoadpoint(util.NewLogger(name), coresettings.NewDatabaseSettingsAdapter(name+"."))
	lp.SetPriority(prio)
	require.NoError(t, config.Loadpoints().Add(config.NewStaticDevice(config.Named{Name: name}, loadpoint.API(lp))))
	return lp
}

// The loadpoints' load management priorities are taken over into their upstream
// priority once; the battery's entry is dropped.
func TestUnifyLmPriorities(t *testing.T) {
	noSettingsDB(t)
	config.Reset()
	t.Cleanup(config.Reset)
	settings.SetBool(keys.LmPrioritiesUnified, false)
	t.Cleanup(func() { settings.SetBool(keys.LmPrioritiesUnified, false) })

	wallbox := addTestLoadpoint(t, "db:1", 3) // ui value 7
	heater := addTestLoadpoint(t, "db:2", 2)  // nothing set: keeps its priority
	pump := addTestLoadpoint(t, "db:3", 5)    // nothing set: keeps its priority

	site := &Site{log: util.NewLogger("test")}
	site.lms().prios = map[string]int{"db:1": 7, lmBatteryName: 4}

	site.unifyLmPriorities()

	assert.Equal(t, 7, wallbox.GetPriority())
	assert.Equal(t, 2, heater.GetPriority())
	assert.Equal(t, 5, pump.GetPriority())
	assert.Equal(t, map[string]int{lmBatteryName: 4}, site.lms().prios)

	// shedding follows the upstream priority, the battery stands below
	assert.Equal(t, 7, site.lmm().Priority(wallbox))
	assert.Equal(t, lm.BatteryPriority, site.lmm().Priority(site.lmBattery()))

	// once only
	site.lms().prios = map[string]int{"db:1": 1}
	site.unifyLmPriorities()
	assert.Equal(t, 7, wallbox.GetPriority())

	// setting a loadpoint's priority sets its upstream priority
	require.NoError(t, site.SetLmPriority("db:2", 9))
	assert.Equal(t, 9, heater.GetPriority())
	assert.NotContains(t, site.lms().prios, "db:2")
}

// A stored priority of the battery is accepted at start and removed from the
// database, the loadpoints' values stay.
func TestDropBatteryLmPriority(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)
	config.Reset()
	t.Cleanup(config.Reset)
	settings.SetBool(keys.LmPrioritiesUnified, true)

	require.NoError(t, settings.SetJson(keys.LmPriorities, map[string]int{"db:1": 7, lmBatteryName: 4}))

	site := &Site{log: util.NewLogger("test")}
	require.NotPanics(t, site.restoreLmSettings)

	var stored map[string]int
	require.NoError(t, settings.Json(keys.LmPriorities, &stored))
	assert.Equal(t, map[string]int{"db:1": 7}, stored)
	assert.NotContains(t, site.lms().prios, lmBatteryName)

	// the battery stands below the loadpoints whatever was stored
	assert.Equal(t, lm.BatteryPriority, site.lmm().Priority(site.lmBattery()))

	// nothing stored: nothing to do, and the battery's priority cannot be set
	require.NotPanics(t, site.dropBatteryLmPriority)
	assert.Error(t, site.SetLmPriority(lmBatteryName, 4))
}
