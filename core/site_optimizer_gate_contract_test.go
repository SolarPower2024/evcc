package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// suggestionContractSite has a missing device before the battery, as evcc keeps
// a device that failed to load as nil
func suggestionContractSite(name string) *Site {
	return &Site{
		log: util.NewLogger("test"),
		batteryMeters: []config.Device[api.Meter]{
			nil,
			config.NewStaticDevice(config.Named{Name: name}, api.Meter(&scenarioBattery{})),
		},
	}
}

// The automatic mode gate (site_optimizer_gate.go) reads evcc's
// batterySuggestionMode without a hook. It relies on: no result is ok false, so
// the fork's own grid charging applies; every action evcc's result writes comes
// back as its mode, charge, hold and holdcharge are what the gate acts on; an
// unknown action is normal and ok; the result is found under the key evcc
// writes it with; an expired result is ok false again.
func TestBatterySuggestionModeContract(t *testing.T) {
	t.Run("no result", func(t *testing.T) {
		site := suggestionContractSite("bat")

		mode, ok := site.batterySuggestionMode()
		assert.False(t, ok)
		assert.Equal(t, api.BatteryUnknown, mode)
	})

	t.Run("actions", func(t *testing.T) {
		site := suggestionContractSite("bat")

		for _, m := range []api.BatteryMode{api.BatteryNormal, api.BatteryHold, api.BatteryCharge, api.BatteryHoldCharge, api.BatteryDischarge} {
			site.setSuggestions(map[string]types.Suggestion{batteryKey("bat"): {Action: m.String()}})

			mode, ok := site.batterySuggestionMode()
			assert.True(t, ok, m.String())
			assert.Equal(t, m, mode, m.String())
		}
	})

	t.Run("unknown action", func(t *testing.T) {
		site := suggestionContractSite("bat")
		site.setSuggestions(map[string]types.Suggestion{batteryKey("bat"): {Action: "boost"}})

		mode, ok := site.batterySuggestionMode()
		assert.True(t, ok)
		assert.Equal(t, api.BatteryNormal, mode)
	})

	t.Run("result of a solve until it expires", func(t *testing.T) {
		site := suggestionContractSite("home")
		enableAutomatic(t)

		completed := reapplyFixture(site,
			[]optimizer.BatteryConfig{{}},
			[]optimizer.BatteryResult{reapplyIdleBattery},
			[]batteryDetail{reapplyHomeBattery},
		)

		mode, ok := site.batterySuggestionMode()
		require.True(t, ok, "the result is not found under the battery's name")
		assert.Equal(t, api.BatteryNormal, mode)
		assert.True(t, site.optimizerInControl())

		site.reapplySuggestions(completed.Add(2*tariff.SlotDuration + time.Minute))

		mode, ok = site.batterySuggestionMode()
		assert.False(t, ok, "an expired result still counts")
		assert.Equal(t, api.BatteryUnknown, mode)
		assert.False(t, site.optimizerInControl())
	})
}
