package core

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// lmStatusOf publishes the overview and returns it
func lmStatusOf(t *testing.T, site *Site) lmStatus {
	t.Helper()

	ch := make(chan util.Param, 1000)
	site.valueChan = ch
	site.publishLmStatus(false)

	var res lmStatus
	for len(ch) > 0 {
		if p := <-ch; p.Key == keys.LmStatus {
			res = p.Val.(lmStatus)
		}
	}
	return res
}

// TestLmStatusOptimizer: the overview marks the battery and the loadpoints the
// optimizer decides, only with automatic mode and (loadpoints) the level full
func TestLmStatusOptimizer(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	sc := newScenario(t)
	site := sc.site
	wallbox := &scenarioLoad{title: "wallbox", power: 0}
	sc.withCircuit(22000, wallbox, 0)

	ctrl := gomock.NewController(t)
	lp := NewLoadpoint(util.NewLogger("lp"), nil)
	lp.site = site
	lp.circuit = sc.circuit
	lp.charger = &fakeCharger{}
	lp.vehicle = modelledVehicle(ctrl)

	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "test"}, sc.circuit)))
	require.NoError(t, config.Loadpoints().Add(config.NewStaticDevice(config.Named{Name: "lp1"}, loadpoint.API(lp))))

	find := func(res lmStatus, battery bool) lmLoadStatus {
		for _, l := range res.Loads {
			if l.Battery == battery {
				return l
			}
		}
		t.Fatalf("no load (battery %v) in %+v", battery, res.Loads)
		return lmLoadStatus{}
	}

	t.Run("without automatic mode", func(t *testing.T) {
		res := lmStatusOf(t, site)
		assert.False(t, find(res, true).Optimizer)
		assert.False(t, find(res, false).Optimizer)

		b, err := json.Marshal(find(res, true))
		require.NoError(t, err)
		assert.NotContains(t, string(b), "optimizer", "only set when true")
	})

	t.Run("stage battery", func(t *testing.T) {
		enableAutomatic(t)
		sc.suggest(api.BatteryHold.String())
		settings.SetString(keys.OptimizerAutomatic, OptimizerAutomaticBattery)

		res := lmStatusOf(t, site)
		assert.True(t, find(res, true).Optimizer)
		assert.False(t, find(res, false).Optimizer, "the loadpoints are not controlled")
	})

	t.Run("stage full", func(t *testing.T) {
		enableAutomatic(t)
		sc.suggest(api.BatteryHold.String())

		res := lmStatusOf(t, site)
		assert.True(t, find(res, true).Optimizer)
		assert.True(t, find(res, false).Optimizer)

		b, err := json.Marshal(find(res, true))
		require.NoError(t, err)
		assert.Contains(t, string(b), `"optimizer":true`)
	})

	t.Run("without a result the optimizer does not control the battery", func(t *testing.T) {
		enableAutomatic(t)
		site.setSuggestions(nil)

		res := lmStatusOf(t, site)
		assert.False(t, find(res, true).Optimizer)
		assert.True(t, find(res, false).Optimizer)
	})
}

// TestLmStatusBatteryOptimizerCharge: an optimizer charge request that passed the
// gate shows as grid charging, one the gate refused does not
func TestLmStatusBatteryOptimizerCharge(t *testing.T) {
	sc := gateScenario(t, api.BatteryCharge.String())
	sc.withDynamicCharge()
	now := time.Now()

	sc.cycle(50, 3000, 0)
	require.Equal(t, api.BatteryCharge, sc.mode())

	st := sc.site.lmBatteryStatus(now, false)
	assert.True(t, st.Optimizer)
	assert.Equal(t, lmStateRunning, st.State, "charging from the grid")
	assert.Equal(t, 2000.0, st.Allowed, "the setpoint")

	// a running peak refuses the request: hold, nothing charges
	sc.cycle(50, 9000, 0)
	require.Equal(t, api.BatteryHold, sc.mode())

	st = sc.site.lmBatteryStatus(now, false)
	assert.True(t, st.Optimizer)
	assert.NotEqual(t, lmStateRunning, st.State)

	// the fork's own flag does not make the optimizer's hold a charge
	sc.suggest(api.BatteryHold.String())
	sc.cycle(50, 3000, 0)
	assert.NotEqual(t, lmStateRunning, sc.site.lmBatteryStatus(now, false).State)
}
