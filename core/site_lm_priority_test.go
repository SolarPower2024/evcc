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

func addTestLoadpoint(t *testing.T, name string, prio, lmPrio int) *Loadpoint {
	t.Helper()
	lp := NewLoadpoint(util.NewLogger(name), coresettings.NewDatabaseSettingsAdapter(name+"."))
	lp.SetPriority(prio)
	lp.LmPrio = lmPrio
	require.NoError(t, config.Loadpoints().Add(config.NewStaticDevice(config.Named{Name: name}, loadpoint.API(lp))))
	return lp
}

// The loadpoints' load management priorities are taken over into their upstream
// priority once; the battery keeps its own value.
func TestUnifyLmPriorities(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)
	lm.Reset()
	t.Cleanup(lm.Reset)
	settings.SetBool(keys.LmPrioritiesUnified, false)
	t.Cleanup(func() { settings.SetBool(keys.LmPrioritiesUnified, false) })

	wallbox := addTestLoadpoint(t, "db:1", 3, 0) // ui value 7
	heater := addTestLoadpoint(t, "db:2", 0, 2)  // yaml lmpriority 2
	pump := addTestLoadpoint(t, "db:3", 5, 0)    // nothing set: keeps its priority

	site := &Site{log: util.NewLogger("test")}
	site.lms().prios = map[string]int{"db:1": 7, lmBatteryName: 4}
	lm.SetPriorityLookup(site.lmPriorityLookup)

	site.unifyLmPriorities()

	assert.Equal(t, 7, wallbox.GetPriority())
	assert.Equal(t, 2, heater.GetPriority())
	assert.Equal(t, 5, pump.GetPriority())
	assert.Equal(t, map[string]int{lmBatteryName: 4}, site.lms().prios)

	// shedding follows the upstream priority, the battery its own value
	assert.Equal(t, 7, lm.Priority(wallbox))
	assert.Equal(t, 4, lm.Priority(site.lmBattery()))

	// once only
	site.lms().prios = map[string]int{"db:1": 1}
	site.unifyLmPriorities()
	assert.Equal(t, 7, wallbox.GetPriority())

	// setting a loadpoint's priority sets its upstream priority
	require.NoError(t, site.SetLmPriority("db:2", 9))
	assert.Equal(t, 9, heater.GetPriority())
	assert.NotContains(t, site.lms().prios, "db:2")
}
