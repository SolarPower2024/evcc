package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	jww "github.com/spf13/jwalterweatherman"
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
	p := lmPlan{devMin: 500, reserve: 3500, power: 5000, controlled: true, limit: 2000, etaC: 0.9, etaD: 0.9}

	// 4 peak slots of 250 Wh excess each: 3000 - 4 * 278 = 1889 Wh, then 1500 W
	// room: 337.5 Wh a slot, 4000 Wh after 7 more slots
	assert.Equal(t, 10, p.chargeSlot(ts, bat, 0, 3000, 4000, nil))
	assert.Equal(t, -1, p.chargeSlot(ts, bat, 0, 3000, 9000, nil), "not within the horizon")
	assert.InDelta(t, 1889, p.chargeBy(ts, bat, 3000, 3), 1, "below the reserve only covering the peaks")

	// above the reserve a paused grid charge leaves the battery free: 833 Wh a
	// slot down to the battery's minimum
	p.reserve = 1000
	assert.InDelta(t, 500, p.chargeBy(ts, bat, 3000, 3), 1, "free above the reserve")
	p.reserve = 3500

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
	goals := socPassGoals(make([]float32, 8), 1, 4, 3500, nil, floor, 3500)
	assert.Equal(t, []float32{0, 0, 3500, 3500, 3500, 3500, 3500, 3500}, goals)

	// below it: raised by the grid charge, following the floor's changes up to it
	goals = socPassGoals(make([]float32, 8), 1, 4, 3000, nil, floor, 3500)
	assert.Equal(t, []float32{0, 0, 3000, 3000, 3500, 3500, 3500, 3500}, goals)

	// peaks after the charge lower it again
	floor = []float32{3500, 3000, 2500, 2000, 2000, 1500, 1000, 1000, 1000, 1000}
	goals = socPassGoals(make([]float32, 8), 1, 4, 3000, nil, floor, 3500)
	assert.Equal(t, []float32{0, 0, 3000, 2500, 2000, 2000, 2000, 2000}, goals)

	// without a floor only the stop soc
	goals = socPassGoals(make([]float32, 8), 1, 4, 3000, nil, nil, 3500)
	assert.Equal(t, []float32{0, 0, 3000, 0, 0, 0, 0, 0}, goals)

	// the charge levels before it, as the fork charges from slot 2 on
	goals = socPassGoals(make([]float32, 8), 1, 4, 3000, []float32{2200, 2600, 3000}, nil, 3500)
	assert.Equal(t, []float32{2200, 2600, 3000, 0, 0, 0, 0, 0}, goals)
}

// The charge levels follow the fork slot by slot; capped they end with the goal.
func TestLmPlanChargeLevels(t *testing.T) {
	ts := optimizer.TimeSeries{Dt: quarterHours(6), Gt: make([]float32, 6), Ft: make([]float32, 6)}
	bat := optimizer.BatteryConfig{SCapacity: 10000, SMax: 10000, CMax: 5000, DMax: 5000}
	p := lmPlan{power: 4000, etaC: 1, etaD: 1}

	levels, i := p.chargeLevels(ts, bat, 1, 2000, 4500, nil)
	assert.Equal(t, 3, i)
	assert.Equal(t, []float32{3000, 4000, 4500}, levels)

	assert.Equal(t, []float32{3000, 4500}, capLevels(levels, 1, 2, 4500), "by slot 2 at the latest")
	assert.Equal(t, levels, capLevels(levels, 1, 5, 4500), "later: unchanged")
}

// A forced run requested while one runs is remembered and run once after it.
func TestLmOptimizeAgain(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}

	// a run is in progress
	site.optimizerMu.Lock()

	site.lmOptimizeLater(false)
	assert.False(t, site.lms().optimizeAgain.Load(), "a periodic run waits for the next slot")

	site.lmOptimizeLater(true)
	assert.True(t, site.lms().optimizeAgain.Load())

	site.optimizerMu.Unlock()
	site.lmOptimizeAgain()
	assert.False(t, site.lms().optimizeAgain.Load(), "run once")

	// the running one finished before the request was remembered: it is not lost
	site.lmOptimizeLater(true)
	assert.False(t, site.lms().optimizeAgain.Load(), "started right away")
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

	t.Run("short result", func(t *testing.T) {
		client, calls := fakeOptimizer(t, http.StatusOK, covered)
		req, res, plan := floorCase()
		res.Batteries[0].StateOfCharge = res.Batteries[0].StateOfCharge[:3]
		res.Batteries[0].ChargingPower = res.Batteries[0].ChargingPower[:3]
		site := &Site{log: util.NewLogger("test")}
		site.lms().plan = &plan
		require.NotPanics(t, func() { site.lmOptimizerPasses(client, req, details, res) })
		assert.Zero(t, calls.Load())
		want, _, _ := floorCase()
		assert.Equal(t, want.Batteries[0], req.Batteries[0], "request unchanged")
	})

	t.Run("two home batteries", func(t *testing.T) {
		client, calls := fakeOptimizer(t, http.StatusOK, covered)
		two := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery}, {Type: batteryTypeBattery}}}
		req, res, plan := floorCase()
		req.Batteries = append(req.Batteries, req.Batteries[0])
		res.Batteries = append(res.Batteries, res.Batteries[0])
		site := &Site{log: util.NewLogger("test")}
		site.lms().plan = &plan
		logger, out := captureLog()
		site.log = logger
		site.lmOptimizerPasses(client, req, two, res)
		assert.Zero(t, calls.Load())
		assert.Contains(t, out.String(), "passes skipped, 2 home batteries")
	})
}

// A peak running now does not refuse grid charging for the whole plan.
func TestLmGridChargeBlockedPeak(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	setPeakShaving(site, 2000, 35)
	site.peak().demand = 5000
	assert.False(t, site.lmGridChargeBlocked())
}

// The grid goals follow the hosted optimizer's hourly slots beyond 48 hours.
func TestLmPlanCoarse(t *testing.T) {
	plan := lmPlan{grid: []lmGridGoal{{slot: 2, level: 1000}, {slot: 6, level: 2000}}}

	// quarter hours after a partial first slot: unchanged
	fine := []int{600, 900, 900, 900, 900, 900, 900, 900}
	assert.Equal(t, plan.grid, plan.coarse(fine).grid)

	// slots 3-6 merged into one hour: the goal of slot 6 ends in it
	coarse := []int{600, 900, 900, 3600, 900}
	assert.Equal(t, []lmGridGoal{{slot: 2, level: 1000}, {slot: 3, level: 2000}}, plan.coarse(coarse).grid)
}

// The plan charges with the efficiency the request carries, not the
// identified one, so the floor does not run ahead of the optimizer's result.
func TestLmPlanEtaFollowsRequest(t *testing.T) {
	site := &Site{
		log: util.NewLogger("test"),
		batteryMeters: []config.Device[api.Meter]{
			config.NewStaticDevice[api.Meter](config.Named{Name: "batt"}, &mockMeter{}),
		},
	}
	s := site.lms()
	s.ident = []batteryIdentResult{{Name: "batt", Capacity: 10, Efficiency: 0.9025, Valid: true}}
	s.identUse = true
	require.InDelta(t, 0.95, site.identChargeEta(), 1e-6)

	req, res, _ := floorCase()
	bat := &req.Batteries[0]
	site.applyLmBatteryInputs(bat, req, true, 2000, 35, 1)

	plan := site.lmPlanInputs()
	require.NotNil(t, plan)
	assert.Equal(t, float32(0.9), plan.etaC)
	assert.InDelta(t, 5000*0.25*0.9, plan.gridCharge(req.TimeSeries, *bat, 6, nil), 0.1, "slot energy with the request's efficiency")

	// the passes with a running identification: no endless follow-up solves
	client, calls := fakeOptimizer(t, http.StatusOK, res)
	site.lmOptimizerPasses(client, req, requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery}}}, res)
	assert.LessOrEqual(t, calls.Load(), int32(2), "second pass and at most one recheck")
}

// Home batteries share the grid charge power and the room below the limit by
// their capacity.
func TestLmPlanGridChargeShare(t *testing.T) {
	ts := optimizer.TimeSeries{Dt: []int{900}, Gt: []float32{500}, Ft: []float32{0}} // 2000 W
	bat := optimizer.BatteryConfig{CMax: 10000}

	p := lmPlan{power: 8000, etaC: 1}
	assert.InDelta(t, 2000, p.gridCharge(ts, bat, 0, nil), 0.1, "one battery: all")

	p.share = 0.5
	assert.InDelta(t, 1000, p.gridCharge(ts, bat, 0, nil), 0.1, "half")
	p.share = 0.25
	assert.InDelta(t, 500, p.gridCharge(ts, bat, 0, nil), 0.1, "1:3")

	// the room below the limit: 5000 - 2000 = 3000 W, a share of it
	p = lmPlan{power: 8000, etaC: 1, controlled: true, limit: 5000, share: 0.5}
	assert.InDelta(t, 3000*0.5*0.25, p.gridCharge(ts, bat, 0, nil), 0.1, "half of the room")
	p.share = 1
	assert.InDelta(t, 3000*0.25, p.gridCharge(ts, bat, 0, nil), 0.1, "all of the room")

	// the peak rule stays
	p.share = 0.5
	p.limit = 1500
	assert.Zero(t, p.gridCharge(ts, bat, 0, nil), "no room")
}

// Two home batteries take their part of the power, so together they reach the
// stop soc no earlier than one battery with the sum of both.
func TestLmOptimizerInputsTwoBatteries(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	newReq := func() *optimizer.OptimizationInput {
		req := &optimizer.OptimizationInput{EtaC: 0.9, EtaD: 0.9}
		for range 96 {
			req.TimeSeries.Dt = append(req.TimeSeries.Dt, 900)
			req.TimeSeries.Gt = append(req.TimeSeries.Gt, 0)
			req.TimeSeries.Ft = append(req.TimeSeries.Ft, 0)
		}
		return req
	}
	bat := func(capacity, power float32) optimizerBattery {
		return optimizerBattery{
			cfg:    optimizer.BatteryConfig{SCapacity: capacity, SMax: capacity, SInitial: capacity / 10, CMax: power, DMax: power, ChargeFromGrid: true},
			detail: batteryDetail{Type: batteryTypeBattery},
		}
	}
	goalSlot := func(b optimizerBattery) int {
		for i, g := range b.cfg.SGoal {
			if g >= b.cfg.SCapacity*0.8-1 {
				return i
			}
		}
		return -1
	}

	run := func(batteries []optimizerBattery) *Site {
		site := &Site{log: util.NewLogger("test")}
		s := site.lms()
		s.socChargeEnabled, s.socChargeStart, s.socChargeStop, s.socChargeRunning = true, 20, 80, true
		site.applyLmOptimizerInputs(newReq(), batteries)
		return site
	}

	single := []optimizerBattery{bat(10000, 4000)}
	run(single)

	two := []optimizerBattery{bat(5000, 2000), bat(5000, 2000)}
	site := run(two)

	require.NotEqual(t, -1, goalSlot(single[0]))
	for _, b := range two {
		assert.GreaterOrEqual(t, goalSlot(b), goalSlot(single[0]))
	}
	assert.Equal(t, float32(0.5), site.lmPlanInputs().share)
}

// captureLog returns a logger that writes into a buffer
func captureLog() (*util.Logger, *bytes.Buffer) {
	var out bytes.Buffer
	return &util.Logger{
		Notepad:  jww.NewNotepad(jww.LevelTrace, jww.LevelTrace, io.Discard, &out, "", 0),
		Redactor: new(util.Redactor),
	}, &out
}

// A failed further solve reports what the optimizer says about it.
func TestLmSolveError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(optimizer.Error{Message: "invalid input", Details: map[string]string{"batteries.0.s_max": "too small"}})
	}))
	t.Cleanup(srv.Close)

	client, err := optimizer.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	req, _, _ := floorCase()
	res, err := lmSolve(context.Background(), client, *req)
	assert.Nil(t, res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid input")
	assert.Contains(t, err.Error(), "batteries.0.s_max: too small")
}

// The passes share one time budget: a solve that takes longer ends them, the
// plan before stays and no further solve starts.
func TestLmOptimizerPassesTimeBudget(t *testing.T) {
	prev := lmPassesTimeout
	lmPassesTimeout = 100 * time.Millisecond
	t.Cleanup(func() { lmPassesTimeout = prev })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	client, err := optimizer.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	req, res, plan := floorCase()
	site := &Site{log: util.NewLogger("test")}
	site.lms().plan = &plan
	site.lms().socChargeEnabled, site.lms().socChargeStart, site.lms().socChargeStop = true, 20, 80

	start := time.Now()
	site.lmOptimizerPasses(client, req, requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery}}}, res)

	assert.Less(t, time.Since(start), 2*time.Second, "returns within the budget")
	assert.EqualValues(t, 1, calls.Load(), "the soc pass does not solve any more")
	want, wantRes, _ := floorCase()
	assert.Equal(t, want.Batteries[0], req.Batteries[0], "request unchanged")
	assert.Equal(t, wantRes.Batteries[0], res.Batteries[0], "plan unchanged")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.True(t, site.lmBudgetUsed(ctx))
	assert.False(t, site.lmBudgetUsed(context.Background()))
}

// The soc pass replaces the rest of the plan with one that charges to the stop
// soc, and keeps the first plan when the second is worse.
func TestLmSocChargePass(t *testing.T) {
	// the second pass covers slots 1-7, the battery has reached the start soc in slot 0
	second := func() *optimizer.OptimizationResult {
		return &optimizer.OptimizationResult{Status: optimizer.Optimal, Batteries: []optimizer.BatteryResult{{
			StateOfCharge:    []float32{4625, 5750, 6000, 6000, 6000, 6000, 6000},
			ChargingPower:    []float32{5000, 5000, 1000, 0, 0, 0, 0},
			DischargingPower: make([]float32, 7),
		}}, GridImport: []float32{4000, 4000, 1000, 0, 0, 0, 0}, GridExport: make([]float32, 7)}
	}

	run := func(t *testing.T, client *optimizer.ClientWithResponses, mod func(*optimizer.OptimizationInput, *optimizer.OptimizationResult)) (*optimizer.OptimizationInput, *optimizer.OptimizationResult) {
		req, res, plan := floorCase()
		plan.limit, plan.controlled = 0, false
		req.Batteries[0].SMin = 0
		res.GridImportOvershoot = nil
		if mod != nil {
			mod(req, res)
		}
		site := &Site{log: util.NewLogger("test")}
		s := site.lms()
		s.socChargeEnabled, s.socChargeStart, s.socChargeStop = true, 36, 60
		site.lmSocChargePass(context.Background(), client, req, 0, plan, nil, res)
		return req, res
	}

	_, before := run(t, func() *optimizer.ClientWithResponses {
		c, _ := fakeOptimizer(t, http.StatusInternalServerError, nil)
		return c
	}(), nil)

	t.Run("success", func(t *testing.T) {
		client, calls := fakeOptimizer(t, http.StatusOK, second())
		req, res := run(t, client, nil)
		assert.EqualValues(t, 1, calls.Load())
		assert.Equal(t, []float32{3583, 4625, 5750, 6000, 6000, 6000, 6000, 6000}, res.Batteries[0].StateOfCharge, "joined")
		assert.Len(t, req.Batteries[0].SGoal, 8, "goals in the request")
		assert.Contains(t, req.Batteries[0].SGoal, float32(6000), "the stop soc")
	})

	t.Run("error", func(t *testing.T) {
		client, _ := fakeOptimizer(t, http.StatusInternalServerError, nil)
		_, res := run(t, client, nil)
		assert.Equal(t, before.Batteries[0].StateOfCharge, res.Batteries[0].StateOfCharge)
	})

	t.Run("invalid plan", func(t *testing.T) {
		bad := second()
		bad.Batteries[0].StateOfCharge = []float32{50000, 50000, 50000, 50000, 50000, 50000, 50000}
		client, _ := fakeOptimizer(t, http.StatusOK, bad)
		_, res := run(t, client, nil)
		assert.Equal(t, before.Batteries[0].StateOfCharge, res.Batteries[0].StateOfCharge)
	})

	t.Run("more over the limit in the rest", func(t *testing.T) {
		worse := second()
		worse.GridImportOvershoot = []float32{0, 900, 0, 0, 0, 0, 0}
		client, _ := fakeOptimizer(t, http.StatusOK, worse)
		_, res := run(t, client, nil)
		assert.Equal(t, before.Batteries[0].StateOfCharge, res.Batteries[0].StateOfCharge)
	})

	t.Run("overshoot before the start does not count", func(t *testing.T) {
		withHead := second()
		withHead.GridImportOvershoot = make([]float32, 7)
		client, _ := fakeOptimizer(t, http.StatusOK, withHead)
		_, res := run(t, client, func(_ *optimizer.OptimizationInput, res *optimizer.OptimizationResult) {
			res.GridImportOvershoot = []float32{500, 0, 0, 0, 0, 0, 0, 0}
		})
		assert.Equal(t, float32(4625), res.Batteries[0].StateOfCharge[1], "joined")
	})

	t.Run("no overshoot field", func(t *testing.T) {
		worse := second()
		worse.GridImportOvershoot = nil
		client, _ := fakeOptimizer(t, http.StatusOK, worse)
		require.NotPanics(t, func() {
			_, res := run(t, client, nil)
			assert.Equal(t, float32(4625), res.Batteries[0].StateOfCharge[1])
		})
	})
}

func TestTailSum(t *testing.T) {
	assert.Equal(t, float32(5), tailSum([]float32{1, 2, 3}, 1))
	assert.Zero(t, tailSum([]float32{1, 2, 3}, 3))
	assert.Zero(t, tailSum(nil, 2))
	assert.Equal(t, float32(6), tailSum([]float32{1, 2, 3}, -1))
}

// Grid charging is refused without a known charge power on a circuit and while
// the shed hold-off runs.
func TestLmGridChargeBlocked(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	site := &Site{log: util.NewLogger("test")}
	assert.False(t, site.lmGridChargeBlocked(), "no circuit")

	root, err := circuit.New(util.NewLogger("test"), "main", 0, 22000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(root))))
	require.NoError(t, site.SetPeakShavingCircuit("main"))
	assert.True(t, site.lmGridChargeBlocked(), "unknown charge power")

	site.peak().chargePower = 5000
	assert.False(t, site.lmGridChargeBlocked(), "known charge power")

	site.lms().batteryShedUntil = time.Now().Add(time.Hour)
	assert.True(t, site.lmGridChargeBlocked(), "shed hold-off")

	site.lms().batteryShedUntil = time.Now().Add(-time.Minute)
	assert.False(t, site.lmGridChargeBlocked(), "hold-off over")
}
