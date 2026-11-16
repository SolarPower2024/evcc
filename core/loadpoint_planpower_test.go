package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/planner"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

// planPowerLoadpoint is a 3 phase loadpoint of 5520 to 11040 W on a site
func planPowerLoadpoint(t *testing.T, site *Site) *Loadpoint {
	t.Helper()

	lp := phaseCurrentsLoadpoint(t, 3, 0, 0)
	lp.site = site
	return lp
}

// chainCircuit is a circuit below the main circuit
func chainCircuit(t *testing.T, maxPower, maxCurrent float64) api.Circuit {
	t.Helper()

	ctrl := gomock.NewController(t)

	root := api.NewMockCircuit(ctrl)
	root.EXPECT().GetParent().Return(nil).AnyTimes()
	root.EXPECT().GetMaxPower().Return(0.0).AnyTimes()
	root.EXPECT().GetMaxCurrent().Return(0.0).AnyTimes()

	c := api.NewMockCircuit(ctrl)
	c.EXPECT().GetParent().Return(root).AnyTimes()
	c.EXPECT().GetMaxPower().Return(maxPower).AnyTimes()
	c.EXPECT().GetMaxCurrent().Return(maxCurrent).AnyTimes()
	return c
}

func TestPlanPower(t *testing.T) {
	const wallbox = 11040.0

	t.Run("without a site", func(t *testing.T) {
		lp := phaseCurrentsLoadpoint(t, 3, 0, 0)
		power, reason := lp.PlanPower(wallbox)
		assert.Equal(t, wallbox, power)
		assert.Empty(t, reason)
	})

	t.Run("without circuits and peak limit", func(t *testing.T) {
		site, _ := planSite(t)
		setPlanHome(site, repeat(500, 48)...)
		site.updatePlanPower()

		power, reason := planPowerLoadpoint(t, site).PlanPower(wallbox)
		assert.Equal(t, wallbox, power)
		assert.Empty(t, reason)
	})

	t.Run("peak limit less the home", func(t *testing.T) {
		site, _ := planSite(t)
		setPeakLimit(site, 7000)
		setPlanHome(site, repeat(500, 48)...)
		site.updatePlanPower()

		power, reason := planPowerLoadpoint(t, site).PlanPower(wallbox)
		assert.InDelta(t, 6500, power, 1e-6)
		assert.Equal(t, "peak", reason)
	})

	t.Run("peak limit without a forecast", func(t *testing.T) {
		site, _ := planSite(t)
		setPeakLimit(site, 7000)
		site.updatePlanPower()

		power, reason := planPowerLoadpoint(t, site).PlanPower(wallbox)
		assert.Equal(t, 7000.0, power)
		assert.Equal(t, "peak", reason)
	})

	t.Run("main circuit 20 A does not limit", func(t *testing.T) {
		rootCircuit(t, 20, 0)
		site, _ := planSite(t)
		setPlanHome(site, repeat(500, 48)...)
		site.updatePlanPower()

		power, reason := planPowerLoadpoint(t, site).PlanPower(wallbox)
		assert.Equal(t, wallbox, power, "13800 W less 500 W is more than the wall box")
		assert.Empty(t, reason)
	})

	t.Run("sub circuit without the home", func(t *testing.T) {
		site, _ := planSite(t)
		setPlanHome(site, repeat(500, 48)...)
		site.updatePlanPower()

		lp := planPowerLoadpoint(t, site)
		lp.circuit = chainCircuit(t, 7000, 0)

		power, reason := lp.PlanPower(wallbox)
		assert.Equal(t, 7000.0, power)
		assert.Equal(t, "circuit", reason)
	})

	t.Run("sub circuit current on the phases of the loadpoint", func(t *testing.T) {
		site, _ := planSite(t)
		site.updatePlanPower()

		lp := planPowerLoadpoint(t, site)
		lp.circuit = chainCircuit(t, 0, 10)

		power, reason := lp.PlanPower(wallbox)
		assert.Equal(t, 6900.0, power, "10 A on 3 phases")
		assert.Equal(t, "circuit", reason)
	})

	t.Run("below the minimum power", func(t *testing.T) {
		site, _ := planSite(t)
		setPeakLimit(site, 1000)
		site.updatePlanPower()

		lp := planPowerLoadpoint(t, site)
		power, reason := lp.PlanPower(wallbox)
		assert.Equal(t, lp.EffectiveMinPower(), power)
		assert.Equal(t, 1840.0, power, "the minimum on one phase")
		assert.Equal(t, "peak", reason)
	})

	t.Run("other loadpoints in fast mode change nothing", func(t *testing.T) {
		site, _ := planSite(t)
		setPeakLimit(site, 7000)
		site.updatePlanPower()

		other := planPowerLoadpoint(t, site)
		site.loadpoints = []*Loadpoint{planPowerLoadpoint(t, site), other}

		before, _ := other.PlanPower(wallbox)
		site.loadpoints[0].mode = api.ModeNow
		after, reason := other.PlanPower(wallbox)

		assert.Equal(t, before, after)
		assert.Equal(t, 7000.0, after)
		assert.Equal(t, "peak", reason)
	})
}

// TestGetPlanRequiredDurationPlanPower: the hook in GetPlanRequiredDuration,
// 40 kWh take 6.15 h at 6.5 kW instead of 3.6 h at the wall box's power
func TestGetPlanRequiredDurationPlanPower(t *testing.T) {
	site, _ := planSite(t)
	lp := planPowerLoadpoint(t, site)

	hours := func() float64 {
		return lp.GetPlanRequiredDuration(40, lp.EffectiveMaxPower()).Hours()
	}

	site.updatePlanPower()
	assert.InDelta(t, 40000/11040.0, hours(), 1e-3, "unconfigured as evcc")

	setPeakLimit(site, 7000)
	setPlanHome(site, repeat(500, 48)...)
	site.updatePlanPower()
	assert.InDelta(t, 40000/6500.0, hours(), 1e-3)
}

// TestPlanOverrunKeepsTheLimit: the time does not suffice with the plan power, the
// plan starts at once and reports its overrun, the limit does not move
func TestPlanOverrunKeepsTheLimit(t *testing.T) {
	site, _ := planSite(t)
	setPeakLimit(site, 7000)
	setPlanHome(site, repeat(500, 48)...)
	site.updatePlanPower()

	lp := planPowerLoadpoint(t, site)
	ui := make(chan util.Param, 100)
	lp.uiChan = ui
	lp.pushChan = nil
	lp.planner = planner.New(util.NewLogger("test"), nil)
	lp.clock.(*clock.Mock).Set(time.Now())

	now := lp.clock.Now()
	lp.planEnergy, lp.planTime = 40, now.Add(4*time.Hour)

	assert.True(t, lp.plannerActive(), "charges at once")

	var overrun time.Duration
	for len(ui) > 0 {
		if p := <-ui; p.Key == keys.PlanOverrun {
			overrun = p.Val.(time.Duration)
		}
	}
	assert.InDelta(t, 40000/6500.0-4, overrun.Hours(), 1e-3)
	assert.Equal(t, 7000.0, site.planPower().limit, "the limit stays")
}
