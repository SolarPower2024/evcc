package core

// Scenario tests for the fork's optimizer inputs against a running optimizer:
// synthetic days (winter, summer, cheap night, negative prices) with the fork's
// settings applied as in site_optimizer.go, each plan checked for what the
// inputs promise. Runs only with OPTIMIZER_URI set, like TestLmOptimizerReplay.

import (
	"context"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scenarioDay describes a synthetic horizon in quarter hours from 18:00
type scenarioDay struct {
	steps  int
	pvPeak float64                   // W at noon, 0 = no pv
	price  func(i int) float64       // grid price in EUR/kWh at step i
	feedIn func(i int) float64       // feed-in price in EUR/kWh at step i
	demand float64                   // base load in W, 2.5 times in the evening
	extra  func(*[]optimizerBattery) // further batteries (vehicles)
}

func flat(v float64) func(int) float64 { return func(int) float64 { return v } }

// hourAt is the hour of day of step i
func hourAt(i int) float64 { return math.Mod(18+float64(i)/4, 24) }

const (
	scenarioCapacity = 16600 // Wh, as the installation
	scenarioPower    = 6250  // W charge/discharge
)

// request builds the request as upstream does before the fork's inputs
func (d scenarioDay) request(soc float64) (optimizer.OptimizationInput, []optimizerBattery) {
	if d.feedIn == nil {
		d.feedIn = flat(0.09)
	}
	req := optimizer.OptimizationInput{
		Strategy: optimizer.OptimizerStrategy{
			ChargingStrategy:    optimizer.OptimizerStrategyChargingStrategyChargeBeforeExport,
			DischargingStrategy: optimizer.OptimizerStrategyDischargingStrategyDischargeBeforeImport,
		},
		EtaC: eta,
		EtaD: eta,
	}
	req.Grid.PMaxImp = 20000

	ts := &req.TimeSeries
	for i := range d.steps {
		h := hourAt(i)
		load := d.demand
		if h >= 17 && h < 21 {
			load *= 2.5
		}
		var pv float64
		if h >= 6 && h < 20 {
			pv = d.pvPeak * math.Sin(math.Pi*(h-6)/14)
		}
		ts.Dt = append(ts.Dt, 900)
		ts.Gt = append(ts.Gt, float32(load/4))
		ts.Ft = append(ts.Ft, float32(pv/4))
		ts.PN = append(ts.PN, float32(d.price(i)/1000))
		ts.PE = append(ts.PE, float32(d.feedIn(i)/1000))
	}

	initial := float32(scenarioCapacity * soc / 100)
	batteries := []optimizerBattery{{
		cfg: optimizer.BatteryConfig{
			SCapacity:      scenarioCapacity,
			SInitial:       initial,
			SMax:           scenarioCapacity,
			SMin:           min(1162, initial), // upstream: 7% battery minimum soc
			CMax:           scenarioPower,
			DMax:           scenarioPower,
			ChargeFromGrid: true,
		},
		detail: batteryDetail{Type: batteryTypeBattery, Name: "bat"},
	}}
	if d.extra != nil {
		d.extra(&batteries)
	}
	return req, batteries
}

// finish sets the end value and the batteries as site_optimizer.go does after
// the fork's inputs
func finish(req *optimizer.OptimizationInput, batteries []optimizerBattery) {
	minOf := func(s []float32) float32 {
		m := s[0]
		for _, v := range s {
			m = min(m, v)
		}
		return m
	}
	pa := max(minOf(req.TimeSeries.PN)*eta*0.99, minOf(req.TimeSeries.PE)/eta*1.01)
	req.Batteries = nil
	for _, b := range batteries {
		b.cfg.PA = pa
		req.Batteries = append(req.Batteries, b.cfg)
	}
}

// capacityMeter is a battery meter that only knows its capacity
type capacityMeter struct{ kWh float64 }

func (m capacityMeter) CurrentPower() (float64, error) { return 0, nil }
func (m capacityMeter) Capacity() float64              { return m.kWh }

// scenarioSettings are the fork's settings of a scenario
type scenarioSettings struct {
	limit, reserve    float64 // peak shaving, limit 0 = off
	start, stop       float64 // soc-based grid charging, start 0 = off
	running           bool    // soc-based grid charging runs
	window            float64 // grid charge window in h, 0 = default
	once              gridChargeOnce
	peakDemand        float64 // current grid demand, above the limit refuses grid charging
	chargePower       float64 // W, 0 = unknown
	withBatteryMeters bool    // capacity known for the one-time duration
}

func (s scenarioSettings) site() *Site {
	site := &Site{log: util.NewLogger("scenario")}
	if s.limit > 0 {
		setPeakShaving(site, s.limit, s.reserve)
		site.peak().demand = s.peakDemand
	}
	site.peak().chargePower = s.chargePower

	lms := site.lms()
	if s.start > 0 {
		lms.socChargeEnabled, lms.socChargeStart, lms.socChargeStop, lms.socChargeRunning = true, s.start, s.stop, s.running
	}
	if s.window > 0 {
		lms.adv.GridChargeWindow = &s.window
	}
	lms.gridOnce = s.once

	if s.withBatteryMeters {
		site.batteryMeters = []config.Device[api.Meter]{
			config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(capacityMeter{scenarioCapacity / 1e3})),
		}
	}
	return site
}

type scenarioResult struct {
	req optimizer.OptimizationInput
	res *optimizer.OptimizationResult
	dur time.Duration
}

func (r scenarioResult) soc(home int) []float32 { return r.res.Batteries[home].StateOfCharge }

// overshoot is the import above the limit at step i in Wh, reported apart from the grid import
func (r scenarioResult) overshoot(i int) float32 {
	if i < len(r.res.GridImportOvershoot) {
		return r.res.GridImportOvershoot[i]
	}
	return 0
}

// importW is the grid import at step i in W
func (r scenarioResult) importW(i int) float64 {
	return float64(r.res.GridImport[i]) / float64(r.req.TimeSeries.Dt[i]) * 3600
}

func TestLmOptimizerScenarios(t *testing.T) {
	uri := os.Getenv("OPTIMIZER_URI")
	if uri == "" {
		t.Skip("OPTIMIZER_URI not set")
	}
	config.Reset()
	t.Cleanup(config.Reset)

	client, err := optimizer.NewClientWithResponses(uri, optimizer.WithHTTPClient(&http.Client{Timeout: 90 * time.Second}))
	require.NoError(t, err)

	solveReq := func(t *testing.T, req optimizer.OptimizationInput) scenarioResult {
		t.Helper()
		start := time.Now()
		resp, err := client.PostOptimizeChargeScheduleWithResponse(context.Background(), req)
		dur := time.Since(start)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))
		require.NotNil(t, resp.JSON200)
		res := resp.JSON200
		require.Contains(t, []optimizer.OptimizationResultStatus{optimizer.Optimal, optimizer.Feasible}, res.Status)
		require.Len(t, res.Batteries, len(req.Batteries))
		require.Len(t, res.GridImport, len(req.TimeSeries.Dt))
		for _, b := range res.Batteries {
			require.Len(t, b.StateOfCharge, len(req.TimeSeries.Dt))
		}
		return scenarioResult{req, res, dur}
	}

	// run applies the settings, solves and checks the general promises: soc
	// never below the minimum, grid import within the hard limit or reported,
	// no energy from nowhere, goals reached unless goalMayMiss
	type opts struct{ goalMayMiss bool }
	run := func(t *testing.T, day scenarioDay, soc float64, set scenarioSettings, o opts) scenarioResult {
		t.Helper()
		req, batteries := day.request(soc)
		set.site().applyLmOptimizerInputs(&req, batteries)
		finish(&req, batteries)

		r := solveReq(t, req)
		bat := req.Batteries[0]
		s := r.soc(0)

		minSoc := s[0]
		for i, v := range s {
			minSoc = min(minSoc, v)
			assert.GreaterOrEqual(t, v, bat.SMin-1, "soc below the minimum at step %d", i)
		}

		for i := range r.res.GridImport {
			if w := r.importW(i); w > float64(req.Grid.PMaxImp)+1 {
				assert.Failf(t, "grid import above the limit", "step %d: %.0f W > %.0f W", i, w, req.Grid.PMaxImp)
			}
		}

		// energy balance: nothing is consumed that was not produced or imported
		ts := req.TimeSeries
		for i := range ts.Dt {
			in := ts.Ft[i] + r.res.GridImport[i] + r.overshoot(i)
			out := ts.Gt[i] + r.res.GridExport[i]
			for j, b := range r.res.Batteries {
				in += b.DischargingPower[i]
				out += b.ChargingPower[i]
				if len(req.Batteries[j].PDemand) > i {
					out += req.Batteries[j].PDemand[i]
				}
			}
			assert.LessOrEqual(t, out, in+1, "energy balance at step %d", i)
		}

		for i, g := range bat.SGoal {
			if g > 0 && !o.goalMayMiss {
				assert.GreaterOrEqual(t, s[i], g-1, "goal %.0f Wh not reached at step %d", g, i)
			}
		}

		// without grid charging the home battery charges from surplus pv only
		if !bat.ChargeFromGrid {
			for i := range ts.Dt {
				surplus := max(0, ts.Ft[i]-ts.Gt[i])
				assert.LessOrEqual(t, r.res.Batteries[0].ChargingPower[i], surplus+1, "grid charged at step %d", i)
			}
		}

		t.Logf("%s in %v: soc %.0f%% -> min %.0f%% / end %.0f%%, s_min %.0f%%, p_max_imp %.0f W, peak import %.0f W",
			r.res.Status, r.dur.Round(time.Millisecond), soc, pct(minSoc), pct(s[len(s)-1]), pct(bat.SMin), req.Grid.PMaxImp, peakImport(r))
		return r
	}

	// installation: peak limit 10 kW, reserve 20%, grid charging 25% -> 40%
	installation := scenarioSettings{limit: 10000, reserve: 20, start: 25, stop: 40, chargePower: scenarioPower, withBatteryMeters: true}
	winter := scenarioDay{steps: 192, pvPeak: 2500, price: flat(0.10), demand: 600}
	summer := scenarioDay{steps: 192, pvPeak: 9000, price: flat(0.10), demand: 400}

	t.Run("inert without settings", func(t *testing.T) {
		req, batteries := winter.request(50)
		want := batteries[0].cfg
		(scenarioSettings{}).site().applyLmOptimizerInputs(&req, batteries)
		assert.Equal(t, want, batteries[0].cfg)
		assert.Equal(t, float32(20000), req.Grid.PMaxImp)
		run(t, winter, 50, scenarioSettings{}, opts{})
	})

	// the floor is the higher of reserve and start soc, never above the soc
	t.Run("soc sweep", func(t *testing.T) {
		for _, soc := range []float64{5, 15, 20, 25, 30, 40, 60, 80, 100} {
			for name, day := range map[string]scenarioDay{"winter": winter, "summer": summer} {
				t.Run(name+"/"+ftoa(soc), func(t *testing.T) {
					r := run(t, day, soc, installation, opts{})
					want := min(scenarioCapacity*25/100, scenarioCapacity*soc/100)
					assert.InDelta(t, max(want, min(1162, scenarioCapacity*soc/100)), r.req.Batteries[0].SMin, 1)
					assert.Equal(t, float32(10000), r.req.Grid.PMaxImp)
				})
			}
		}
	})

	t.Run("peak limit above the circuit is ignored", func(t *testing.T) {
		set := installation
		set.limit = 30000
		r := run(t, winter, 50, set, opts{})
		assert.Equal(t, float32(20000), r.req.Grid.PMaxImp)
	})

	// running grid charging reaches the stop soc within the window
	t.Run("grid charging window", func(t *testing.T) {
		for _, window := range []float64{1, 3, 6, 12} {
			t.Run(ftoa(window)+"h", func(t *testing.T) {
				set := installation
				set.running, set.window = true, window
				r := run(t, winter, 20, set, opts{})
				bat := r.req.Batteries[0]
				i := slotAfter(r.req.TimeSeries.Dt, set.site().socChargeDuration(bat, bat.SInitial, scenarioCapacity*0.4))
				assert.LessOrEqual(t, i, slotAfter(r.req.TimeSeries.Dt, time.Duration(window*float64(time.Hour))), "within the window")
				require.Len(t, r.req.Batteries[0].SGoal, len(r.req.TimeSeries.Dt))
				assert.InDelta(t, scenarioCapacity*0.4, r.req.Batteries[0].SGoal[i], 1)
			})
		}
	})

	// not yet running: the battery falls to the start soc and is charged to the
	// stop soc again, the forecast shows it (the plan must not stay at the start soc)
	t.Run("grid charging planned ahead", func(t *testing.T) {
		day := winter
		day.demand = 1100
		day.price = flat(0.25) // planner price that lets the battery discharge
		for _, soc := range []float64{40, 97} {
			t.Run(ftoa(soc), func(t *testing.T) {
				req, batteries := day.request(soc)
				site := installation.site()
				site.applyLmOptimizerInputs(&req, batteries)
				finish(&req, batteries)
				r := solveReq(t, req)
				details := requestDetails{BatteryDetails: lo.Map(batteries, func(b optimizerBattery, _ int) batteryDetail { return b.detail })}

				site.lmSocChargePass(client, &req, details, r.res)
				s := r.soc(0)
				require.Len(t, s, len(req.TimeSeries.Dt), "joined over the whole horizon")

				// down to the start soc, then charged to the stop soc right away
				first := slices.IndexFunc(s, func(v float32) bool { return pct(v) <= 26 })
				require.GreaterOrEqual(t, first, 0, "discharges to the start soc")
				require.Less(t, first+3, len(s))
				assert.GreaterOrEqual(t, pct(max(s[first+1], s[first+2], s[first+3])), 39.0, "charged to the stop soc")
				for i, v := range s {
					assert.GreaterOrEqual(t, pct(v), 24.9, "below the start soc at step %d", i)
				}
				t.Logf("start soc at step %d, then %.0f%% %.0f%% %.0f%%", first, pct(s[first+1]), pct(s[first+2]), pct(s[first+3]))
			})
		}
	})

	// the peak shaving reserve above the stop soc is a hard minimum, even when
	// discharging further would pay
	t.Run("reserve above the stop soc", func(t *testing.T) {
		day := winter
		day.demand = 1100
		day.price = flat(0.25)
		set := installation
		set.reserve = 50
		r := run(t, day, 97, set, opts{})
		details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery}}}
		set.site().lmSocChargePass(client, &r.req, details, r.res)
		assert.Empty(t, lo.Filter(r.req.Batteries[0].SGoal, func(g float32, _ int) bool { return g > 0 }), "no grid charging planned")
		for i, v := range r.soc(0) {
			assert.GreaterOrEqual(t, pct(v), 49.9, "below the reserve at step %d", i)
		}
	})

	// the limit leaves no room for grid charging: the limit wins
	t.Run("grid charging against a tight limit", func(t *testing.T) {
		set := installation
		set.running, set.limit = true, 1000
		r := run(t, winter, 20, set, opts{goalMayMiss: true})
		var over float32
		for i := range r.res.GridImport {
			over += r.overshoot(i)
		}
		// the evening load of 1500 W exceeds the limit, the reserve is kept for the
		// peak shaving control, the plan reports the rest as overshoot
		t.Logf("overshoot %.0f Wh, violations %+v", over, r.res.LimitViolations)
		assert.Equal(t, float32(1000), r.req.Grid.PMaxImp)
	})

	// a peak refuses grid charging: not offered, the battery charges from pv only
	t.Run("peak refuses grid charging", func(t *testing.T) {
		for name, day := range map[string]scenarioDay{"winter": winter, "summer": summer} {
			t.Run(name, func(t *testing.T) {
				set := installation
				set.running, set.peakDemand = true, 12000
				set.once = gridChargeOnce{Target: 90}
				r := run(t, day, 20, set, opts{})
				bat := r.req.Batteries[0]
				assert.False(t, bat.ChargeFromGrid)
				assert.Nil(t, bat.SGoal, "no goal while refused")
				assert.InDelta(t, scenarioCapacity*0.2, bat.SMin, 1, "reserve only")
			})
		}
	})

	// one-time grid charging right away: target when the charge power can reach it
	t.Run("once right away", func(t *testing.T) {
		set := installation
		set.once = gridChargeOnce{Target: 90}
		r := run(t, winter, 30, set, opts{})
		bat := r.req.Batteries[0]
		// 60% of 16.6 kWh at 6.25 kW with 90% efficiency: 1.77 h, the 8th quarter hour
		require.Len(t, bat.SGoal, len(r.req.TimeSeries.Dt))
		assert.InDelta(t, scenarioCapacity*0.9, bat.SGoal[7], 1)
	})

	// without a known grid charge power: at the battery's maximum charge power
	t.Run("once right away, charge power unknown", func(t *testing.T) {
		set := installation
		set.chargePower, set.withBatteryMeters = 0, false
		set.once = gridChargeOnce{Target: 90}
		r := run(t, winter, 30, set, opts{})
		require.Len(t, r.req.Batteries[0].SGoal, len(r.req.TimeSeries.Dt))
		assert.InDelta(t, scenarioCapacity*0.9, r.req.Batteries[0].SGoal[7], 1)
	})

	// by a time: at the cheapest slots before it
	t.Run("once by a time, cheap night", func(t *testing.T) {
		cheap := func(i int) bool { return i >= 16 && i < 28 } // 22:00-01:00
		day := winter
		day.price = func(i int) float64 {
			if cheap(i) {
				return 0.05
			}
			return 0.12
		}
		set := installation
		set.once = gridChargeOnce{Target: 90, Until: time.Now().Add(12 * time.Hour)}
		r := run(t, day, 30, set, opts{})

		var inCheap, total float64
		ts := r.req.TimeSeries
		for i, c := range r.res.Batteries[0].ChargingPower {
			if grid := float64(c - max(0, ts.Ft[i]-ts.Gt[i])); grid > 1 {
				total += grid
				if cheap(i) {
					inCheap += grid
				}
			}
		}
		t.Logf("grid charged %.0f Wh, %.0f%% in the cheap slots", total, inCheap/total*100)
		i := slotAfter(ts.Dt, 12*time.Hour)
		assert.GreaterOrEqual(t, r.soc(0)[i], float32(scenarioCapacity*0.9)-1, "target not reached by the time")
		assert.Greater(t, inCheap/total, 0.8, "charging not in the cheap slots")
	})

	// one-time and running grid charging together: both goals
	t.Run("once and grid charging", func(t *testing.T) {
		set := installation
		set.running = true
		set.once = gridChargeOnce{Target: 90, Until: time.Now().Add(12 * time.Hour)}
		r := run(t, winter, 20, set, opts{})
		goals := lo.Filter(r.req.Batteries[0].SGoal, func(g float32, _ int) bool { return g > 0 })
		assert.Contains(t, goals, float32(scenarioCapacity*0.9), "one-time target")
		assert.Contains(t, goals, float32(scenarioCapacity*0.4), "stop soc")
	})

	t.Run("once below the soc", func(t *testing.T) {
		set := installation
		set.once = gridChargeOnce{Target: 50}
		r := run(t, winter, 60, set, opts{})
		assert.NotContains(t, r.req.Batteries[0].SGoal, float32(scenarioCapacity*0.5), "no one-time target")
	})

	// negative prices: the battery charges from the grid in them, within the limit
	t.Run("negative prices", func(t *testing.T) {
		negative := func(i int) bool { return i >= 40 && i < 52 } // 04:00-07:00
		day := winter
		day.price = func(i int) float64 {
			if negative(i) {
				return -0.05
			}
			return 0.25
		}
		day.feedIn = func(i int) float64 {
			if negative(i) {
				return 0
			}
			return 0.09
		}
		r := run(t, day, 30, installation, opts{})
		var charged float64
		for i, c := range r.res.Batteries[0].ChargingPower {
			if negative(i) {
				charged += float64(c)
			}
		}
		t.Logf("charged %.0f Wh at negative prices", charged)
		assert.Greater(t, charged, 1000.0)
	})

	// low grid prices (T35): discharging only pays from about 11.2 ct against
	// 9 ct feed-in, documents the current behaviour
	t.Run("low price threshold", func(t *testing.T) {
		for _, ct := range []float64{10, 11, 11.2, 12, 15, 25} {
			t.Run(ftoa(ct)+"ct", func(t *testing.T) {
				day := winter
				day.pvPeak, day.price = 0, flat(ct/100)
				r := run(t, day, 100, installation, opts{})
				var dis float64
				for _, v := range r.res.Batteries[0].DischargingPower {
					dis += float64(v)
				}
				t.Logf("%.1f ct: discharged %.0f Wh", ct, dis)
				switch {
				case ct <= 11:
					assert.Zero(t, math.Round(dis), "discharges below the threshold")
				case ct >= 12:
					assert.Greater(t, dis, 1000.0, "does not discharge above the threshold")
				}
			})
		}
	})

	// vehicles: the circuit budget caps the power, the priority ranks surplus
	vehicle := func(prio int, cmax float32) optimizerBattery {
		return optimizerBattery{
			cfg: optimizer.BatteryConfig{
				SCapacity: 60000, SInitial: 20000, SMax: 60000,
				CMin: 1400, CMax: cmax, CPriority: prio, ChargeFromGrid: false,
			},
			detail: batteryDetail{Type: batteryTypeVehicle},
		}
	}

	t.Run("vehicle within the circuit budget", func(t *testing.T) {
		day := summer
		day.extra = func(b *[]optimizerBattery) { *b = append(*b, vehicle(0, 7000)) }
		r := run(t, day, 50, installation, opts{})
		for i, wh := range r.res.Batteries[1].ChargingPower {
			assert.LessOrEqual(t, float64(wh)/900*3600, 7000.0+1, "above the circuit at step %d", i)
		}
	})

	t.Run("vehicle priority", func(t *testing.T) {
		day := summer
		day.pvPeak = 6000
		day.extra = func(b *[]optimizerBattery) { *b = append(*b, vehicle(2, 11000), vehicle(0, 11000)) }
		r := run(t, day, 100, installation, opts{})
		// by 16:00 of the first day, before both are full
		sum := func(b int) (res float64) {
			for _, v := range r.res.Batteries[b].ChargingPower[:88] {
				res += float64(v)
			}
			return res
		}
		t.Logf("by 16:00: high priority %.0f Wh, low priority %.0f Wh", sum(1), sum(2))
		assert.Greater(t, sum(1), sum(2), "higher priority gets less surplus")
	})

	t.Run("charging vehicle (c_active)", func(t *testing.T) {
		day := summer
		day.extra = func(b *[]optimizerBattery) {
			v := vehicle(0, 11000)
			v.cfg.CActive = true
			*b = append(*b, v)
		}
		run(t, day, 50, installation, opts{})
	})

	// horizon lengths up to what the installation sends, within evcc's timeout
	t.Run("horizons", func(t *testing.T) {
		for _, steps := range []int{8, 96, 192, 408} {
			t.Run(ftoa(float64(steps)), func(t *testing.T) {
				day := winter
				day.steps = steps
				set := installation
				set.running = true
				r := run(t, day, 20, set, opts{goalMayMiss: steps < 12})
				assert.Less(t, r.dur, 30*time.Second)
			})
		}
	})
}

func pct(wh float32) float64 { return float64(wh) / scenarioCapacity * 100 }

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func peakImport(r scenarioResult) float64 {
	var res float64
	for i := range r.res.GridImport {
		res = max(res, r.importW(i))
	}
	return res
}
