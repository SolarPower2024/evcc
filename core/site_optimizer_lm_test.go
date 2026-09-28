package core

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quarterHours(n int) []int {
	dt := make([]int, n)
	for i := range dt {
		dt[i] = 900
	}
	return dt
}

func homeBattery() optimizerBattery {
	return optimizerBattery{
		cfg: optimizer.BatteryConfig{
			SCapacity:      10000,
			SInitial:       5000,
			SMax:           10000,
			CMax:           5000,
			DMax:           5000,
			ChargeFromGrid: true,
		},
		detail: batteryDetail{Type: batteryTypeBattery, Name: "bat"},
	}
}

func setPeakShaving(site *Site, limit, reserve float64) {
	s := site.peak()
	s.enabled, s.limit, s.reserve = true, limit, reserve
	s.set = func(float64) error { return nil }
}

// Without circuits, peak shaving and soc-based grid charging the request stays
// exactly as upstream builds it.
func TestLmOptimizerInputsInert(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	site := &Site{log: util.NewLogger("test")}
	req := optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{Dt: quarterHours(16)}}
	req.Grid.PMaxImp = 22000

	batteries := []optimizerBattery{homeBattery()}
	want := batteries[0].cfg

	site.applyLmOptimizerInputs(&req, batteries)

	assert.Equal(t, float32(22000), req.Grid.PMaxImp)
	assert.Equal(t, want, batteries[0].cfg)
}

func TestLmOptimizerInputsPeakAndGridCharge(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	site := &Site{log: util.NewLogger("test")}
	setPeakShaving(site, 7000, 30)

	s := site.lms()
	s.socChargeEnabled, s.socChargeStart, s.socChargeStop = true, 20, 80

	req := optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{Dt: quarterHours(16)}}
	req.Grid.PMaxImp = 22000

	// idle: peak limit, reserve as floor (above the start soc)
	batteries := []optimizerBattery{homeBattery()}
	site.applyLmOptimizerInputs(&req, batteries)

	bat := batteries[0].cfg
	assert.Equal(t, float32(7000), req.Grid.PMaxImp, "peak limit below the circuit")
	assert.Equal(t, float32(3000), bat.SMin, "reserve 30%% of 10 kWh")
	assert.Nil(t, bat.SGoal, "no goal while grid charging does not run")

	// start soc above the reserve: it is the floor, planned ahead
	s.socChargeStart = 40
	batteries = []optimizerBattery{homeBattery()}
	site.applyLmOptimizerInputs(&req, batteries)
	assert.Equal(t, float32(4000), batteries[0].cfg.SMin)

	// running: stop soc as goal once charged, 3000 Wh at the battery's 5000 W
	// and 90% = 1125 Wh a quarter hour, in the 3rd
	s.socChargeRunning = true
	batteries = []optimizerBattery{homeBattery()}
	site.applyLmOptimizerInputs(&req, batteries)
	bat = batteries[0].cfg
	require.Len(t, bat.SGoal, 16)
	i := slices.IndexFunc(bat.SGoal, func(g float32) bool { return g > 0 })
	assert.Equal(t, float32(8000), bat.SGoal[i])
	assert.Equal(t, 2, i)

	// below the floor: the minimum is the current soc
	batteries = []optimizerBattery{homeBattery()}
	batteries[0].cfg.SInitial = 1500
	site.applyLmOptimizerInputs(&req, batteries)
	assert.Equal(t, float32(1500), batteries[0].cfg.SMin)

	// a running peak no longer refuses grid charging for the whole plan: the
	// import limit leaves it the room below the limit
	site.peak().demand = 9000
	batteries = []optimizerBattery{homeBattery()}
	site.applyLmOptimizerInputs(&req, batteries)
	bat = batteries[0].cfg
	assert.True(t, bat.ChargeFromGrid)
	assert.Contains(t, bat.SGoal, float32(8000))
	assert.Equal(t, float32(4000), bat.SMin)
}

// A loadpoint plans with at most its circuits' power, priorities map to 0-2.
func TestLmOptimizerInputsLoadpoint(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)
	lm.Reset()
	t.Cleanup(lm.Reset)

	root, err := circuit.New(util.NewLogger("test"), "main", 0, 22000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(root))))
	sub, err := circuit.NewConfigurableFromConfig(context.TODO(), map[string]any{"title": "garage", "maxPower": 7000, "parent": "main"})
	require.NoError(t, err)

	lp := &Loadpoint{log: util.NewLogger("lp"), circuit: sub, LmPrio: 8, priority: 8} // both: before and with one priority
	site := &Site{log: util.NewLogger("test"), loadpoints: []*Loadpoint{lp}}

	id := 0
	batteries := []optimizerBattery{{
		cfg:    optimizer.BatteryConfig{CMin: 1400, CMax: 11000},
		detail: batteryDetail{Type: batteryTypeLoadpoint, loadpoint: &id},
	}}

	req := optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{Dt: quarterHours(4)}}
	site.applyLmOptimizerInputs(&req, batteries)

	assert.Equal(t, float32(7000), batteries[0].cfg.CMax, "garage circuit")
	assert.Equal(t, 2, batteries[0].cfg.CPriority)

	// with a vehicle the entry is typed as such, same inputs
	batteries[0].cfg.CMax, batteries[0].cfg.CPriority = 11000, 0
	batteries[0].detail.Type = batteryTypeVehicle
	site.applyLmOptimizerInputs(&req, batteries)
	assert.Equal(t, float32(7000), batteries[0].cfg.CMax)
	assert.Equal(t, 2, batteries[0].cfg.CPriority)

	for prio, want := range map[int]int{0: 0, 3: 0, 4: 1, 6: 1, 7: 2, 10: 2} {
		assert.Equal(t, want, optimizerPriority(prio), "priority %d", prio)
	}
}

func TestSlotAfter(t *testing.T) {
	dt := []int{300, 900, 900, 3600}
	assert.Equal(t, 0, slotAfter(dt, 5*time.Minute))
	assert.Equal(t, 1, slotAfter(dt, 10*time.Minute))
	assert.Equal(t, 3, slotAfter(dt, time.Hour))
	assert.Equal(t, 3, slotAfter(dt, 48*time.Hour), "capped at the horizon")
}

// Edge cases: nothing to plan with, stale goal arrays, targets above the
// maximum, a time already passed, unknown loadpoints.
func TestLmOptimizerInputsEdgeCases(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	site := &Site{log: util.NewLogger("test")}
	setPeakShaving(site, 7000, 30)
	s := site.lms()
	s.socChargeEnabled, s.socChargeStart, s.socChargeStop, s.socChargeRunning = true, 20, 80, true

	req := optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{Dt: quarterHours(16)}}

	// unknown capacity: soc inputs skipped
	b := homeBattery()
	b.cfg.SCapacity = 0
	want := b.cfg
	batteries := []optimizerBattery{b}
	site.applyLmOptimizerInputs(&req, batteries)
	assert.Equal(t, want, batteries[0].cfg)

	// a goal array of another length is replaced, not indexed out of range
	b = homeBattery()
	b.cfg.SGoal = []float32{1, 2, 3}
	batteries = []optimizerBattery{b}
	site.applyLmOptimizerInputs(&req, batteries)
	require.Len(t, batteries[0].cfg.SGoal, 16)

	// a goal above the maximum soc is capped
	b = homeBattery()
	b.cfg.SMax = 7000
	batteries = []optimizerBattery{b}
	site.applyLmOptimizerInputs(&req, batteries)
	assert.Contains(t, batteries[0].cfg.SGoal, float32(7000))

	// one-time charging by a time already passed: as soon as possible, with
	// peak shaving as soon as the charge power gets there (4th step, below)
	s.socChargeRunning = false
	s.gridOnce = gridChargeOnce{Target: 90, Until: time.Now().Add(-time.Minute)}
	batteries = []optimizerBattery{homeBattery()}
	site.applyLmOptimizerInputs(&req, batteries)
	require.Len(t, batteries[0].cfg.SGoal, 16)
	assert.Equal(t, float32(9000), batteries[0].cfg.SGoal[3])

	// right away without a known charge power: at the battery's charge power,
	// 40% of 10 kWh at 5 kW with 90% efficiency takes 53 min, the 4th step
	s.gridOnce = gridChargeOnce{Target: 90}
	batteries = []optimizerBattery{homeBattery()}
	site.applyLmOptimizerInputs(&req, batteries)
	assert.Equal(t, float32(9000), batteries[0].cfg.SGoal[3])

	// loadpoint entries without a known loadpoint are left alone
	id := 5
	lp := optimizerBattery{cfg: optimizer.BatteryConfig{CMax: 11000}, detail: batteryDetail{Type: batteryTypeLoadpoint, loadpoint: &id}}
	batteries = []optimizerBattery{lp, {cfg: lp.cfg, detail: batteryDetail{Type: batteryTypeVehicle}}}
	assert.NotPanics(t, func() { site.applyLmOptimizerInputs(&req, batteries) })
	assert.Equal(t, float32(11000), batteries[0].cfg.CMax)
}

// The EEG tariff never reaches the optimizer: its feed-in price is the
// standard feed-in tariff.
func TestLmOptimizerFeedInWithEeg(t *testing.T) {
	feedIn, err := tariff.NewFixedFromConfig(map[string]any{"price": 0.09})
	require.NoError(t, err)
	eeg, err := tariff.NewFixedFromConfig(map[string]any{"price": 0.0})
	require.NoError(t, err)

	site := &Site{log: util.NewLogger("test"), tariffs: &tariff.Tariffs{FeedIn: feedIn, FeedInEeg: eeg}}

	rates := currentRates(site.GetTariff(api.TariffUsageFeedIn))
	require.NotEmpty(t, rates)
	for _, r := range rates {
		assert.Equal(t, 0.09, r.Value)
	}
}

// co2Tariff is a planner tariff that is no price
type co2Tariff struct{}

func (co2Tariff) Rates() (api.Rates, error) { return nil, nil }
func (co2Tariff) Type() api.TariffType      { return api.TariffTypeCo2 }

// A price tariff set as planner tariff is the optimizer's grid price, anything
// else leaves the grid tariff.
func TestLmOptimizerGridTariff(t *testing.T) {
	grid, err := tariff.NewFixedFromConfig(map[string]any{"price": 0.10})
	require.NoError(t, err)
	planner, err := tariff.NewFixedFromConfig(map[string]any{"price": 0.12})
	require.NoError(t, err)

	price := func(site *Site) float64 {
		rates := currentRates(site.optimizerGridTariff())
		require.NotEmpty(t, rates)
		return rates[0].Value
	}

	site := &Site{log: util.NewLogger("test"), tariffs: &tariff.Tariffs{Grid: grid}}
	assert.Equal(t, 0.10, price(site), "without planner tariff: grid as upstream")

	site.tariffs.Planner = planner
	assert.Equal(t, 0.12, price(site), "planner price")
	assert.Equal(t, 0.10, currentRates(site.GetTariff(api.TariffUsageGrid))[0].Value, "grid tariff unchanged")

	site.tariffs.Planner = co2Tariff{}
	assert.Equal(t, 0.10, price(site), "co2 planner: grid")

	assert.Nil(t, (&Site{}).optimizerGridTariff())
}

// The forecast does not call the battery empty at a floor set by the fork.
func TestLmForecastLowest(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	low := &batteryForecastSlot{soc: 25, limit: true}

	assert.True(t, site.lmForecastLowest(low).limit, "without a raised floor it is empty")

	site.lms().floorRaised = true
	assert.False(t, site.lmForecastLowest(low).limit)
	assert.Nil(t, site.lmForecastLowest(nil))
}

// Grid charging only enters the request while it is switched on and allowed;
// the reserve stays a hard minimum above the stop soc.
func TestLmBatteryInputsSocChargeSwitch(t *testing.T) {
	newReq := func() (*optimizer.OptimizationInput, *optimizer.BatteryConfig) {
		req := &optimizer.OptimizationInput{EtaC: 0.9, EtaD: 0.9}
		for range 96 {
			req.TimeSeries.Dt = append(req.TimeSeries.Dt, 900)
			req.TimeSeries.Gt = append(req.TimeSeries.Gt, 225)
			req.TimeSeries.Ft = append(req.TimeSeries.Ft, 0)
		}
		return req, &optimizer.BatteryConfig{SCapacity: 10000, SMax: 10000, SInitial: 9000, CMax: 5000, DMax: 5000, ChargeFromGrid: true}
	}

	site := &Site{log: util.NewLogger("test")}
	s := site.lms()
	s.socChargeStart, s.socChargeStop = 25, 40

	// switched off: no goal, no floor from it
	req, bat := newReq()
	site.applyLmBatteryInputs(bat, req, false, 0, 0)
	assert.Empty(t, bat.SGoal)
	assert.Zero(t, bat.SMin)

	// switched on, not running: floor at the start soc, the charge is planned
	// by the second pass
	s.socChargeEnabled = true
	req, bat = newReq()
	site.applyLmBatteryInputs(bat, req, false, 0, 0)
	assert.Equal(t, float32(2500), bat.SMin)
	assert.Empty(t, bat.SGoal)

	// running: the stop soc after the charging time at the battery's maximum,
	// 1500 Wh at 5000 W and 0.9 = 20 min, in slot 1
	s.socChargeRunning = true
	req, bat = newReq()
	bat.SInitial = 2500
	site.applyLmBatteryInputs(bat, req, false, 0, 0)
	require.Len(t, bat.SGoal, 96)
	assert.Equal(t, float32(4000), bat.SGoal[1])
	s.socChargeRunning = false

	// peak shaving reserve 50 % above the stop soc: hard minimum, nothing planned
	req, bat = newReq()
	site.applyLmBatteryInputs(bat, req, true, 10000, 50)
	assert.Equal(t, float32(5000), bat.SMin)
	assert.Empty(t, bat.SGoal)

	// grid charging not allowed from the grid: no goal
	req, bat = newReq()
	bat.ChargeFromGrid = false
	site.applyLmBatteryInputs(bat, req, false, 0, 0)
	assert.Empty(t, bat.SGoal)
}

// The second pass starts where the plan reaches the start soc; its request
// continues from the plan's state then, and both plans are joined.
func TestSocPass(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	s := site.lms()
	s.socChargeEnabled, s.socChargeStart, s.socChargeStop = true, 25, 40

	req := &optimizer.OptimizationInput{Batteries: []optimizer.BatteryConfig{
		{SCapacity: 10000, SMax: 10000, SInitial: 9000, SMin: 2500, CMax: 5000, DMax: 5000, ChargeFromGrid: true},
		{SMax: 50000, SInitial: 20000, CMax: 11000, PDemand: []float32{1, 2, 3, 4, 5, 6}},
	}}
	req.TimeSeries = optimizer.TimeSeries{Dt: []int{900, 900, 900, 900, 900, 900}, Gt: []float32{1, 2, 3, 4, 5, 6}, Ft: []float32{0, 0, 0, 0, 0, 0}, PN: []float32{1, 1, 1, 1, 1, 1}, PE: []float32{0, 0, 0, 0, 0, 0}}
	details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery}, {Type: batteryTypeVehicle}}}
	home := lmHomeBattery(details, req, &optimizer.OptimizationResult{Batteries: make([]optimizer.BatteryResult, 2)})
	assert.Equal(t, 0, home)
	res := &optimizer.OptimizationResult{Status: optimizer.Optimal, Batteries: []optimizer.BatteryResult{
		{StateOfCharge: []float32{7000, 5000, 2500, 2500, 2500, 2500}, ChargingPower: make([]float32, 6), DischargingPower: []float32{2000, 2000, 2500, 0, 0, 0}},
		{StateOfCharge: []float32{21000, 22000, 23000, 23000, 23000, 23000}, ChargingPower: []float32{1000, 1000, 1000, 0, 0, 0}, DischargingPower: make([]float32, 6)},
	}, GridImport: []float32{0, 0, 0, 9, 9, 9}}

	k, goal := site.socPassStart(req, home, res)
	assert.Equal(t, 2, k, "start soc reached at the end of slot 2")
	assert.Equal(t, float32(4000), goal)

	req2 := socPassRequest(*req, res, k)
	assert.Equal(t, []int{900, 900, 900}, req2.TimeSeries.Dt)
	assert.Equal(t, []float32{4, 5, 6}, req2.TimeSeries.Gt)
	assert.Equal(t, float32(2500), req2.Batteries[0].SInitial)
	assert.Equal(t, float32(23000), req2.Batteries[1].SInitial)
	assert.True(t, req2.Batteries[1].CActive, "the vehicle was charging")
	assert.Equal(t, []float32{4, 5, 6}, req2.Batteries[1].PDemand)
	assert.Equal(t, float32(9000), req.Batteries[0].SInitial, "the first request is unchanged")

	res2 := optimizer.OptimizationResult{Status: optimizer.Optimal, Batteries: []optimizer.BatteryResult{
		{StateOfCharge: []float32{4000, 3500, 3000}, ChargingPower: []float32{1500, 0, 0}, DischargingPower: []float32{0, 500, 500}},
		{StateOfCharge: []float32{23000, 23000, 23000}, ChargingPower: make([]float32, 3), DischargingPower: make([]float32, 3)},
	}, GridImport: []float32{2000, 1, 1}}
	socPassJoin(res, res2, k)
	assert.Equal(t, []float32{7000, 5000, 2500, 4000, 3500, 3000}, res.Batteries[0].StateOfCharge)
	assert.Equal(t, []float32{0, 0, 0, 2000, 1, 1}, res.GridImport)
	assert.Len(t, res.Batteries[1].ChargingPower, 6)

	// not reached, running, switched off, reserve above the start soc
	res.Batteries[0].StateOfCharge = []float32{9000, 9000, 9000, 9000, 9000, 9000}
	k, _ = site.socPassStart(req, home, res)
	assert.Equal(t, -1, k, "not reached")

	res.Batteries[0].StateOfCharge = []float32{7000, 2500, 2500, 2500, 2500, 2500}
	s.socChargeRunning = true
	k, _ = site.socPassStart(req, home, res)
	assert.Equal(t, -1, k, "running: goal of the first pass")
	s.socChargeRunning = false

	req.Batteries[0].SMin = 5000
	k, _ = site.socPassStart(req, home, res)
	assert.Equal(t, -1, k, "reserve above the start soc")
	req.Batteries[0].SMin = 2500

	s.socChargeEnabled = false
	k, _ = site.socPassStart(req, home, res)
	assert.Equal(t, -1, k, "switched off")
}
