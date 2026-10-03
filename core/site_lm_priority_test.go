package core

import (
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/loadpoint"
	coresettings "github.com/evcc-io/evcc/core/settings"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func addTestLoadpoint(t *testing.T, name string, prio int) *Loadpoint {
	t.Helper()
	lp := NewLoadpoint(util.NewLogger(name), coresettings.NewDatabaseSettingsAdapter(name+"."))
	lp.SetPriority(prio)
	require.NoError(t, config.Loadpoints().Add(config.NewStaticDevice(config.Named{Name: name}, loadpoint.API(lp))))
	return lp
}

// The loadpoints' load management priorities are taken over into their upstream
// priority once; the battery keeps its own value.
func TestUnifyLmPriorities(t *testing.T) {
	noSettingsDB(t)
	config.Reset()
	t.Cleanup(config.Reset)
	settings.SetBool(keys.LmPrioritiesUnified, false)
	t.Cleanup(func() { settings.SetBool(keys.LmPrioritiesUnified, false) })

	wallbox := addTestLoadpoint(t, "db:1", 3) // ui value 7
	heater := addTestLoadpoint(t, "db:2", 2)  // nothing set: keeps its priority
	pump := addTestLoadpoint(t, "db:3", 5)    // nothing set: keeps its priority
	boiler := addTestLoadpoint(t, "db:4", 1)  // old lmpriority 6 in its stored config
	boiler.LmPrio_ = 6

	site := &Site{log: util.NewLogger("test")}
	site.lms().prios = map[string]int{"db:1": 7, lmBatteryName: 4}
	site.lmm().SetPriorityLookup(site.lmPriorityLookup)

	site.unifyLmPriorities()

	assert.Equal(t, 7, wallbox.GetPriority())
	assert.Equal(t, 2, heater.GetPriority())
	assert.Equal(t, 5, pump.GetPriority())
	assert.Equal(t, 6, boiler.GetPriority())
	assert.Equal(t, map[string]int{lmBatteryName: 4}, site.lms().prios)

	// shedding follows the upstream priority, the battery its own value
	assert.Equal(t, 7, site.lmm().Priority(wallbox))
	assert.Equal(t, 4, site.lmm().Priority(site.lmBattery()))

	// once only
	site.lms().prios = map[string]int{"db:1": 1}
	site.unifyLmPriorities()
	assert.Equal(t, 7, wallbox.GetPriority())

	// setting a loadpoint's priority sets its upstream priority
	require.NoError(t, site.SetLmPriority("db:2", 9))
	assert.Equal(t, 9, heater.GetPriority())
	assert.NotContains(t, site.lms().prios, "db:2")
}

// A stored loadpoint config may still carry the old lmpriority key, e.g. a yaml
// loadpoint moved to the ui. It must not stop the loadpoint from loading.
func TestLoadpointConfigOldLmPriority(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)
	require.NoError(t, config.Chargers().Add(config.NewStaticDevice(config.Named{Name: "charger"}, api.Charger(api.NewMockCharger(gomock.NewController(t))))))

	_, static, err := loadpoint.SplitConfig(map[string]any{"charger": "charger", "title": "Heizstab", "priority": 2, "lmpriority": 6})
	require.NoError(t, err)

	lp, err := NewLoadpointFromConfig(util.NewLogger("test"), nil, nil, static)
	require.NoError(t, err)
	assert.Equal(t, 6, lp.LmPrio_)
}
