package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planSite returns a site whose clocks stand still, with no limit and no forecast
func planSite(t *testing.T) (*Site, *clock.Mock) {
	t.Helper()

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 11, 16, 22, 0, 0, 0, time.UTC))

	site := &Site{log: util.NewLogger("test")}
	site.custom.peak.clock = clk
	site.custom.planPower.clock = clk

	return site, clk
}

// setPlanHome gives the site a home forecast of quarter hour slots, in W
func setPlanHome(site *Site, watts ...float64) {
	req := optimizer.OptimizationInput{}
	for _, w := range watts {
		req.TimeSeries.Dt = append(req.TimeSeries.Dt, 900)
		req.TimeSeries.Gt = append(req.TimeSeries.Gt, float32(w/4)) // Wh per quarter hour
	}
	site.setPlanHomeForecast(req, site.planPower().now())
}

// repeat returns n times w
func repeat(w float64, n int) []float64 {
	res := make([]float64, n)
	for i := range res {
		res[i] = w
	}
	return res
}

func TestPlanRoom(t *testing.T) {
	t.Run("no limit", func(t *testing.T) {
		site, _ := planSite(t)
		setPlanHome(site, 500)
		site.updatePlanPower()

		room, reason := site.planRoom(time.Time{}, 3)
		assert.Zero(t, room)
		assert.Empty(t, reason)
	})

	t.Run("peak limit less the home", func(t *testing.T) {
		site, clk := planSite(t)
		setPeakLimit(site, 7000)
		setPlanHome(site, repeat(500, 48)...)
		site.updatePlanPower()

		room, reason := site.planRoom(clk.Now().Add(6*time.Hour), 3)
		assert.InDelta(t, 6500, room, 1e-6)
		assert.Equal(t, "peak", reason)
	})

	t.Run("fallback without forecast", func(t *testing.T) {
		site, _ := planSite(t)
		setPeakLimit(site, 7000)
		site.updatePlanPower()

		room, reason := site.planRoom(time.Time{}, 3)
		assert.Equal(t, 7000.0, room)
		assert.Equal(t, "peak", reason)
	})

	t.Run("fallback with an old forecast", func(t *testing.T) {
		site, clk := planSite(t)
		setPeakLimit(site, 7000)
		setPlanHome(site, repeat(500, 48)...)
		site.updatePlanPower()

		clk.Add(2*time.Hour + time.Minute)
		room, _ := site.planRoom(time.Time{}, 3)
		assert.Equal(t, 7000.0, room)

		clk.Add(-2 * time.Minute)
		room, _ = site.planRoom(time.Time{}, 3)
		assert.InDelta(t, 6500, room, 1e-6, "forecast of 2 hours still counts")
	})

	t.Run("the window decides, from now to the plan time", func(t *testing.T) {
		site, clk := planSite(t)
		setPeakLimit(site, 7000)
		// 4 hours at 2000 W, then 500 W: the day is high, the night is low
		setPlanHome(site, append(repeat(2000, 16), repeat(500, 32)...)...)
		site.updatePlanPower()

		room, _ := site.planRoom(clk.Now().Add(4*time.Hour), 3)
		assert.InDelta(t, 5000, room, 1e-6, "only the high hours")

		room, _ = site.planRoom(clk.Now().Add(8*time.Hour), 3)
		assert.InDelta(t, 5750, room, 1e-6, "mean of 4 h at 2000 and 4 h at 500")

		// no plan time: the next 12 hours, 4 h at 2000 and 8 h at 500
		room, _ = site.planRoom(time.Time{}, 3)
		assert.InDelta(t, 7000-(4*2000+8*500)/12.0, room, 1e-6)

		// plan time passed: the same
		room, _ = site.planRoom(clk.Now().Add(-time.Hour), 3)
		assert.InDelta(t, 7000-(4*2000+8*500)/12.0, room, 1e-6)

		// the window starts now: the passed hour at 2000 W does not count
		setPlanHome(site, append(repeat(2000, 4), repeat(500, 44)...)...)
		clk.Add(time.Hour)
		room, _ = site.planRoom(clk.Now().Add(2*time.Hour), 3)
		assert.InDelta(t, 6500, room, 1e-6)
	})

	t.Run("home above the limit", func(t *testing.T) {
		site, _ := planSite(t)
		setPeakLimit(site, 7000)
		setPlanHome(site, repeat(9000, 48)...)
		site.updatePlanPower()

		room, reason := site.planRoom(time.Time{}, 3)
		assert.Equal(t, 1.0, room, "positive: 0 would mean no limit")
		assert.Equal(t, "peak", reason)
	})

	t.Run("battery is not counted", func(t *testing.T) {
		site, _ := planSite(t)
		setPeakLimit(site, 7000)
		setPlanHome(site, repeat(500, 48)...)
		site.updatePlanPower()
		before, _ := site.planRoom(time.Time{}, 3)

		site.peak().reserve = 80
		site.peak().chargePower = 3000
		site.peak().shaving = true
		site.updatePlanPower()
		after, _ := site.planRoom(time.Time{}, 3)

		assert.Equal(t, before, after)
	})
}

// rootCircuit registers a main circuit with the given limits
func rootCircuit(t *testing.T, maxCurrent, maxPower float64) {
	t.Helper()

	config.Reset()
	t.Cleanup(config.Reset)

	c, err := circuit.New(util.NewLogger("test"), "main", maxCurrent, maxPower, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(c))))
}

func TestPlanRoomMainCircuit(t *testing.T) {
	t.Run("current limit on 3 phases", func(t *testing.T) {
		rootCircuit(t, 20, 0)
		site, _ := planSite(t)
		site.updatePlanPower()

		room, reason := site.planRoom(time.Time{}, 3)
		assert.InDelta(t, 13800, room, 1e-6)
		assert.Equal(t, "circuit", reason)
	})

	t.Run("current limit on 1 phase", func(t *testing.T) {
		rootCircuit(t, 25, 0)
		site, _ := planSite(t)
		site.updatePlanPower()

		room, reason := site.planRoom(time.Time{}, 1)
		assert.InDelta(t, 5750, room, 1e-6)
		assert.Equal(t, "circuit", reason)
	})

	t.Run("peak limit below the circuit", func(t *testing.T) {
		rootCircuit(t, 20, 0)
		site, _ := planSite(t)
		setPeakLimit(site, 7000)
		site.updatePlanPower()

		room, reason := site.planRoom(time.Time{}, 3)
		assert.Equal(t, 7000.0, room)
		assert.Equal(t, "peak", reason)
	})

	t.Run("circuit below the peak limit", func(t *testing.T) {
		rootCircuit(t, 0, 6000)
		site, _ := planSite(t)
		setPeakLimit(site, 7000)
		setPlanHome(site, repeat(500, 48)...)
		site.updatePlanPower()

		room, reason := site.planRoom(time.Time{}, 3)
		assert.InDelta(t, 5500, room, 1e-6)
		assert.Equal(t, "circuit", reason)
	})
}

// TestPlanPowerParallel: the loadpoint reads the cache while the site writes it
func TestPlanPowerParallel(t *testing.T) {
	rootCircuit(t, 20, 0)
	site, _ := planSite(t)
	setPeakLimit(site, 7000)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			site.updatePlanPower()
			setPlanHome(site, repeat(500, 48)...)
		}
	}()

	for range 500 {
		site.planRoom(time.Time{}, 3)
	}
	<-done
}
