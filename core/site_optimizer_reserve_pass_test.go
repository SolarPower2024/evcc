package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertFloats(t *testing.T, want, got []float32, msg string) {
	t.Helper()
	require.Len(t, got, len(want), msg)
	for i := range want {
		assert.InDelta(t, want[i], got[i], 1, "%s: slot %d", msg, i)
	}
}

func TestLmPlanGridCharge(t *testing.T) {
	ts := optimizer.TimeSeries{Dt: []int{900, 900, 900}, Gt: []float32{375, 400, 625}, Ft: []float32{0, 0, 0}} // 1500, 1600, 2500 W
	bat := optimizer.BatteryConfig{CMax: 5000}

	p := lmPlan{power: 3000, etaC: 1, controlled: true}
	assert.InDelta(t, 750, p.gridCharge(ts, bat, 0, nil), 0.1, "no peak shaving: the charge power")

	p.limit = 2000
	assert.InDelta(t, 125, p.gridCharge(ts, bat, 0, nil), 0.1, "the room below the limit, 500 W")
	assert.Zero(t, p.gridCharge(ts, bat, 1, nil), "less than the minimum")
	assert.Zero(t, p.gridCharge(ts, bat, 2, nil), "paused above the limit")
	assert.Zero(t, p.gridCharge(ts, bat, 0, []float32{100}), "a vehicle takes the room")

	p.controlled = false
	assert.InDelta(t, 750, p.gridCharge(ts, bat, 1, nil), 0.1, "switched: the full power below the limit")
	assert.Zero(t, p.gridCharge(ts, bat, 2, nil), "switched: paused above it")

	p.limit, p.power = 0, 9000
	assert.InDelta(t, 1250, p.gridCharge(ts, bat, 0, nil), 0.1, "at most the battery's charge power")
}

// While a peak pauses grid charging the battery covers the excess, the goal is
// reached once there is room.
func TestLmPlanChargeSlot(t *testing.T) {
	ts := optimizer.TimeSeries{Dt: quarterHours(12)}
	for i := range 12 {
		gt := float32(125) // 500 W
		if i < 4 {
			gt = 750 // 3000 W
		}
		ts.Gt = append(ts.Gt, gt)
		ts.Ft = append(ts.Ft, 0)
	}
	bat := optimizer.BatteryConfig{SCapacity: 10000, SMax: 10000, CMax: 5000, DMax: 5000}
	p := lmPlan{devMin: 500, power: 5000, controlled: true, limit: 2000, etaC: 0.9, etaD: 0.9}

	// 4 peak slots of 250 Wh excess each: 3000 - 4 * 278 = 1889 Wh, then 1500 W
	// room: 337.5 Wh a slot, 4000 Wh after 7 more slots
	assert.Equal(t, 10, p.chargeSlot(ts, bat, 0, 3000, 4000, nil))
	assert.Equal(t, -1, p.chargeSlot(ts, bat, 0, 3000, 9000, nil), "not within the horizon")
	assert.InDelta(t, 1889, p.chargeBy(ts, bat, 3000, 3), 1, "only covering the peaks")

	p.limit = 0
	assert.Equal(t, 0, p.chargeSlot(ts, bat, 0, 3000, 4000, nil), "no peak shaving: 1125 Wh a slot")
}

// floorCase: a battery of 10 kWh at 40 %, reserve 35 %, limit 2 kW, 8 slots:
// 3500 W for 4 slots, 1000 W for 2, then 2000 W surplus
func floorCase() (*optimizer.OptimizationInput, *optimizer.OptimizationResult, lmPlan) {
	req := &optimizer.OptimizationInput{EtaC: 0.9, EtaD: 0.9, Batteries: []optimizer.BatteryConfig{
		{SCapacity: 10000, SMax: 10000, SInitial: 4000, SMin: 3500, CMax: 5000, DMax: 5000, ChargeFromGrid: true},
	}}
	req.TimeSeries = optimizer.TimeSeries{
		Dt: quarterHours(8),
		Gt: []float32{875, 875, 875, 875, 250, 250, 0, 0},
		Ft: []float32{0, 0, 0, 0, 0, 0, 500, 500},
		PN: make([]float32, 8), PE: make([]float32, 8),
	}
	// the first pass: down to the reserve, then over the limit
	res := &optimizer.OptimizationResult{Status: optimizer.Optimal, Batteries: []optimizer.BatteryResult{{
		StateOfCharge:    []float32{3583, 3500, 3500, 3500, 3500, 3500, 3950, 4400},
		ChargingPower:    []float32{0, 0, 0, 0, 0, 0, 500, 500},
		DischargingPower: []float32{375, 75, 0, 0, 0, 0, 0, 0},
	}},
		GridImport:          []float32{500, 500, 500, 500, 250, 250, 0, 0},
		GridExport:          make([]float32, 8),
		GridImportOvershoot: []float32{0, 300, 375, 375, 0, 0, 0, 0},
	}
	plan := lmPlan{devMin: 500, floor0: 3500, target: 3500, reserve: 3500, limit: 2000, power: 5000, controlled: true, etaC: 0.9, etaD: 0.9}
	return req, res, plan
}

func TestLmPlanFloor(t *testing.T) {
	// below the reserve only the excess of 1500 W leaves the battery, pv
	// surplus refills it
	req, res, plan := floorCase()
	assertFloats(t, []float32{3500, 3167, 2750, 2333, 2333, 2333, 2783, 3233}, plan.floor(req, 0, res), "peaks")

	// without peaks the first pass's minimum stays
	res.GridImportOvershoot = make([]float32, 8)
	assert.Nil(t, plan.floor(req, 0, res))

	// below the floor now: only real charging raises it. The plan charging from
	// the grid by itself does not, running grid charging does.
	req, res, plan = floorCase()
	req.TimeSeries.Gt = []float32{100, 100, 100, 100, 100, 100, 100, 100} // 400 W
	req.TimeSeries.Ft = make([]float32, 8)
	req.Batteries[0].SInitial = 2000
	res.GridImportOvershoot = make([]float32, 8)
	res.Batteries[0].StateOfCharge = []float32{2500, 3000, 3500, 3500, 3500, 3500, 3500, 3500}
	plan.floor0, plan.power = 2000, 3000
	assertFloats(t, []float32{2000, 2000, 2000, 2000, 2000, 2000, 2000, 2000}, plan.floor(req, 0, res), "voluntary grid charging")

	plan.grid = []lmGridGoal{{slot: 2, level: 3000}} // 1600 W room: 360 Wh a slot
	assertFloats(t, []float32{2360, 2720, 3000, 3000, 3000, 3000, 3000, 3000}, plan.floor(req, 0, res), "running grid charging")

	// the start soc above the reserve: while a peak pauses grid charging the
	// battery runs freely down to the reserve, below it only the excess
	req, res, plan = floorCase()
	req.Batteries[0].SInitial = 5000
	req.TimeSeries.Gt = []float32{875, 875, 875, 875, 875, 875, 875, 875}
	req.TimeSeries.Ft = make([]float32, 8)
	res.Batteries[0].StateOfCharge = []float32{5000, 5000, 5000, 5000, 5000, 5000, 5000, 5000}
	res.Batteries[0].DischargingPower = make([]float32, 8)
	res.GridImportOvershoot = []float32{375, 375, 375, 375, 375, 375, 375, 375}
	plan.floor0, plan.target, plan.reserve = 5000, 5000, 2000
	assertFloats(t, []float32{4583, 3611, 2639, 1667, 1250, 833, 500, 500}, plan.floor(req, 0, res), "start soc above the reserve")
}

func TestSocPassGoals(t *testing.T) {
	floor := []float32{3500, 3000, 2500, 2000, 2000, 2000, 2500, 3000, 3500, 3500}

	// stop soc at the floor target: held there
	goals := socPassGoals(make([]float32, 8), 1, 4, 3500, floor, 3500)
	assert.Equal(t, []float32{0, 0, 3500, 3500, 3500, 3500, 3500, 3500}, goals)

	// below it: raised by the grid charge, following the floor's changes up to it
	goals = socPassGoals(make([]float32, 8), 1, 4, 3000, floor, 3500)
	assert.Equal(t, []float32{0, 0, 3000, 3000, 3500, 3500, 3500, 3500}, goals)

	// peaks after the charge lower it again
	floor = []float32{3500, 3000, 2500, 2000, 2000, 1500, 1000, 1000, 1000, 1000}
	goals = socPassGoals(make([]float32, 8), 1, 4, 3000, floor, 3500)
	assert.Equal(t, []float32{0, 0, 3000, 2500, 2000, 2000, 2000, 2000}, goals)

	// without a floor only the stop soc
	goals = socPassGoals(make([]float32, 8), 1, 4, 3000, nil, 3500)
	assert.Equal(t, []float32{0, 0, 3000, 0, 0, 0, 0, 0}, goals)
}

func TestLmPlanValid(t *testing.T) {
	req, res, _ := floorCase()
	assert.True(t, lmPlanValid(*req, 0, res))

	res.Batteries[0].StateOfCharge[3] = 1000 // below its minimum
	assert.False(t, lmPlanValid(*req, 0, res))

	_, res, _ = floorCase()
	res.GridImport = res.GridImport[:4]
	assert.False(t, lmPlanValid(*req, 0, res), "incomplete")

	_, res, _ = floorCase()
	res.Status = optimizer.Infeasible
	assert.False(t, lmPlanValid(*req, 0, res))
}

// fakeOptimizer answers every request with res, or with an error status
func fakeOptimizer(t *testing.T, status int, res *optimizer.OptimizationResult) (*optimizer.ClientWithResponses, *atomic.Int32) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if res != nil {
			_ = json.NewEncoder(w).Encode(res)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := optimizer.NewClientWithResponses(srv.URL)
	require.NoError(t, err)
	return client, &calls
}

// The reserve pass replaces the plan with one solved against the floor, and
// keeps the first plan whenever that fails.
func TestLmOptimizerPasses(t *testing.T) {
	details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery}}}
	covered := &optimizer.OptimizationResult{Status: optimizer.Optimal, Batteries: []optimizer.BatteryResult{{
		StateOfCharge:    []float32{3583, 3167, 2750, 2333, 2333, 2333, 2783, 3233},
		ChargingPower:    []float32{0, 0, 0, 0, 0, 0, 500, 500},
		DischargingPower: []float32{375, 375, 375, 375, 0, 0, 0, 0},
	}}, GridImport: []float32{500, 500, 500, 500, 250, 250, 0, 0}, GridExport: make([]float32, 8)}

	run := func(t *testing.T, client *optimizer.ClientWithResponses, details requestDetails) (*Site, *optimizer.OptimizationInput, *optimizer.OptimizationResult) {
		req, res, plan := floorCase()
		site := &Site{log: util.NewLogger("test")}
		site.lms().plan = &plan
		site.lms().floorRaised = true
		site.lmOptimizerPasses(client, req, details, res)
		return site, req, res
	}

	t.Run("floor", func(t *testing.T) {
		client, calls := fakeOptimizer(t, http.StatusOK, covered)
		site, req, res := run(t, client, details)
		assert.EqualValues(t, 1, calls.Load(), "no recheck needed")
		assert.Equal(t, float32(500), req.Batteries[0].SMin, "the battery's own minimum")
		assertFloats(t, []float32{3500, 3167, 2750, 2333, 2333, 2333, 2783, 3233}, req.Batteries[0].SGoal, "floor as goals")
		assert.Equal(t, covered.Batteries[0].StateOfCharge, res.Batteries[0].StateOfCharge)
		assert.False(t, site.lms().floorRaised, "reaching the minimum is empty")
	})

	first := func(t *testing.T, req *optimizer.OptimizationInput, res *optimizer.OptimizationResult) {
		t.Helper()
		_, want, _ := floorCase()
		assert.Equal(t, float32(3500), req.Batteries[0].SMin)
		assert.Empty(t, req.Batteries[0].SGoal)
		assert.Equal(t, want.Batteries[0].StateOfCharge, res.Batteries[0].StateOfCharge)
	}

	t.Run("optimizer error", func(t *testing.T) {
		client, _ := fakeOptimizer(t, http.StatusInternalServerError, nil)
		_, req, res := run(t, client, details)
		first(t, req, res)
	})

	t.Run("invalid plan", func(t *testing.T) {
		bad := *covered
		bad.Batteries = []optimizer.BatteryResult{covered.Batteries[0]}
		bad.Batteries[0].StateOfCharge = []float32{3583, 100, 100, 100, 100, 100, 100, 100}
		client, _ := fakeOptimizer(t, http.StatusOK, &bad)
		_, req, res := run(t, client, details)
		first(t, req, res)
	})

	t.Run("more over the limit", func(t *testing.T) {
		worse := *covered
		worse.GridImportOvershoot = []float32{0, 900, 900, 900, 0, 0, 0, 0}
		client, _ := fakeOptimizer(t, http.StatusOK, &worse)
		_, req, res := run(t, client, details)
		first(t, req, res)
	})

	t.Run("two home batteries", func(t *testing.T) {
		client, calls := fakeOptimizer(t, http.StatusOK, covered)
		two := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery}, {Type: batteryTypeBattery}}}
		req, res, plan := floorCase()
		req.Batteries = append(req.Batteries, req.Batteries[0])
		res.Batteries = append(res.Batteries, res.Batteries[0])
		site := &Site{log: util.NewLogger("test")}
		site.lms().plan = &plan
		site.lmOptimizerPasses(client, req, two, res)
		assert.Zero(t, calls.Load())
	})
}

// A peak running now does not refuse grid charging for the whole plan.
func TestLmGridChargeBlockedPeak(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	setPeakShaving(site, 2000, 35)
	site.peak().demand = 5000
	assert.False(t, site.lmGridChargeBlocked())
}
