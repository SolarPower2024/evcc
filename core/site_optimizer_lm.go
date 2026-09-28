package core

// Custom extension: the fork's settings as optimizer inputs, see
// site_optimizer_gate.go for the gate the automatic mode passes.
//
// Inputs, not overrides: what the optimizer has to respect goes into its
// request, so its plan already contains it, in the advisory as well as in the
// automatic mode:
//
//   - peak shaving: the peak limit as hard grid import limit, the reserve as
//     the home battery's minimum soc; below it the battery only covers peaks,
//     see site_optimizer_reserve_pass.go
//   - soc-based grid charging: the start soc as minimum soc; while charging
//     runs the stop soc as goal once charged, with peak shaving only with the
//     room below the limit; where it starts later see site_optimizer_soc_pass.go
//   - grid charging the load management currently refuses is not offered
//     (charge_from_grid off)
//   - load management: a loadpoint plans with at most its circuits' power, and
//     the priorities rank the batteries (c_priority)
//   - a price tariff set as planner tariff is the grid price the optimizer
//     plans with, statistics keep the grid tariff
//
// Without circuits, peak shaving and soc-based grid charging the request is
// unchanged.

import (
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
)

// defaultGridChargeWindow is how long the optimizer may take to reach the stop
// soc of running soc-based grid charging
const defaultGridChargeWindow = 3 * time.Hour

// gridChargeWindow returns the grid charge window
func (site *Site) gridChargeWindow() time.Duration {
	if v := site.advanced().GridChargeWindow; v != nil {
		return time.Duration(*v * float64(time.Hour))
	}
	return defaultGridChargeWindow
}

// optimizerPriority maps a priority 0-10 onto the optimizer's 0-2
func optimizerPriority(prio int) int {
	switch {
	case prio >= 7:
		return 2
	case prio >= 4:
		return 1
	default:
		return 0
	}
}

// optimizerGridTariff returns the tariff the optimizer plans the grid price
// with: a price tariff set as planner tariff, else the grid tariff as upstream.
// Lets a fixed planning price above the real one make discharging pay where the
// real prices are too close to the feed-in price.
func (site *Site) optimizerGridTariff() api.Tariff {
	site.RLock()
	defer site.RUnlock()

	if site.tariffs == nil {
		return nil
	}

	if t := site.tariffs.Planner; t != nil {
		switch t.Type() {
		case api.TariffTypePriceStatic, api.TariffTypePriceDynamic, api.TariffTypePriceForecast:
			return t
		}
	}

	return site.tariffs.Get(api.TariffUsageGrid)
}

// applyLmOptimizerInputs adds the fork's settings to the optimizer request
func (site *Site) applyLmOptimizerInputs(req *optimizer.OptimizationInput, batteries []optimizerBattery) {
	lmActive := len(config.Circuits().Devices()) > 0
	peakOn, limit, reserve := site.peakShavingConfigured()

	s := site.lms()
	s.mu.Lock()
	s.floorRaised = false
	s.plan = nil
	s.mu.Unlock()

	if peakOn && limit > 0 && (req.Grid.PMaxImp == 0 || float32(limit) < req.Grid.PMaxImp) {
		req.Grid.PMaxImp = float32(limit)
	}

	for i := range batteries {
		b := &batteries[i]

		switch b.detail.Type {
		case batteryTypeBattery:
			site.applyBatteryIdent(&b.cfg, &b.detail)
			site.applyLmBatteryInputs(&b.cfg, req, peakOn, limit, reserve)

			if lmActive && site.lmBatteryCircuit() != nil {
				b.cfg.CPriority = optimizerPriority(lm.Priority(site.lmBattery()))
			}

		case batteryTypeLoadpoint, batteryTypeVehicle:
			if !lmActive || b.detail.loadpoint == nil || *b.detail.loadpoint >= len(site.loadpoints) {
				continue
			}

			lp := site.loadpoints[*b.detail.loadpoint]
			if lp == nil {
				continue
			}

			if power := circuitPower(lp.GetCircuit()); power > 0 && float32(power) < b.cfg.CMax && float32(power) >= b.cfg.CMin {
				b.cfg.CMax = float32(power)
			}

			b.cfg.CPriority = optimizerPriority(lm.Priority(lp))
		}
	}
}

// circuitPower returns the lowest power limit of a circuit and its parents, 0
// without any
func circuitPower(c api.Circuit) float64 {
	var res float64
	for ; c != nil; c = c.GetParent() {
		if p := c.GetMaxPower(); p > 0 && (res == 0 || p < res) {
			res = p
		}
	}
	return res
}

// applyLmBatteryInputs sets the home battery's minimum soc and grid charge goals
func (site *Site) applyLmBatteryInputs(bat *optimizer.BatteryConfig, req *optimizer.OptimizationInput, peakOn bool, limit, reserve float64) {
	if bat.SCapacity <= 0 {
		return
	}

	dt := req.TimeSeries.Dt
	top := bat.SMax
	if top <= 0 {
		top = bat.SCapacity
	}

	wh := func(soc float64) float32 { return bat.SCapacity * float32(soc) / 100 }

	plan := site.newLmPlan(*bat)
	if req.EtaD > 0 {
		plan.etaD = req.EtaD
	}
	var floor float32
	if peakOn {
		floor = wh(reserve)
		plan.reserve, plan.limit = floor, float32(limit)
	}

	s := site.lms()

	s.mu.Lock()
	socOn, start, stop, running := s.socChargeEnabled, s.socChargeStart, s.socChargeStop, s.socChargeRunning
	s.mu.Unlock()

	if site.lmGridChargeBlocked() {
		bat.ChargeFromGrid = false
	}

	setGoal := func(i int, goal float32) {
		if len(bat.SGoal) != len(dt) {
			bat.SGoal = make([]float32, len(dt))
		}
		bat.SGoal[i] = max(bat.SGoal[i], goal)
		plan.grid = append(plan.grid, lmGridGoal{slot: i, level: goal})
	}

	if socOn && bat.ChargeFromGrid {
		floor = max(floor, wh(start))

		// while it runs the charge as the fork does it, slot by slot up to the
		// stop soc, so the plan charges now as well; where it starts later is
		// found by a second pass, see site_optimizer_soc_pass.go
		if goal := min(wh(stop), top); running && goal > bat.SInitial {
			if levels, i := plan.chargeLevels(req.TimeSeries, *bat, 0, bat.SInitial, goal, nil); i >= 0 {
				if plan.limit <= 0 {
					i = min(i, slotAfter(dt, site.gridChargeWindow()))
					levels = capLevels(levels, 0, i, goal)
				}
				for j, v := range levels[:len(levels)-1] {
					bat.SGoal = setLevel(bat.SGoal, len(dt), j, v)
				}
				setGoal(i, goal)
			}
		}
	}

	// one-time grid charging: the target by the chosen time, or as early as
	// the charge power can reach it, see site_lm_once.go. With peak shaving
	// only what the room below the limit allows.
	if o := site.gridChargeOnce(); o.Target > 0 && bat.ChargeFromGrid {
		if goal := min(wh(o.Target), top); goal > bat.SInitial {
			d := time.Until(o.Until)
			if o.Until.IsZero() {
				d = site.onceRequiredDuration(o.Target, float64(bat.SInitial/bat.SCapacity*100))
				// unknown grid charge power: the battery's maximum charge power
				if d <= 0 && bat.CMax > 0 {
					d = time.Duration(float64(goal-bat.SInitial) / (float64(bat.CMax) * site.identChargeEta()) * float64(time.Hour))
				}
			}
			i := slotAfter(dt, max(d, 0))
			if plan.limit > 0 {
				// right away, also once the time has passed
				if o.Until.IsZero() || d <= 0 {
					if j := plan.chargeSlot(req.TimeSeries, *bat, 0, bat.SInitial, goal, nil); j >= 0 {
						i = max(i, j)
					} else {
						i = len(dt) - 1
						goal = min(goal, plan.chargeBy(req.TimeSeries, *bat, bat.SInitial, i))
					}
				} else {
					goal = min(goal, plan.chargeBy(req.TimeSeries, *bat, bat.SInitial, i))
				}
			}
			if goal > bat.SInitial {
				setGoal(i, goal)
			}
		}
	}

	plan.target = min(floor, top)

	// never above the current soc, the optimizer cannot start below its minimum
	if floor = min(floor, bat.SInitial); floor > bat.SMin {
		bat.SMin = floor

		s.mu.Lock()
		s.floorRaised = true
		s.mu.Unlock()
	}
	plan.floor0 = bat.SMin

	s.mu.Lock()
	s.plan = &plan
	s.mu.Unlock()
}

// lmForecastLowest keeps the forecast from calling the battery empty when it
// only reaches the floor set here (reserve, start soc), see
// batteryForecastSocExtremes
func (site *Site) lmForecastLowest(low *batteryForecastSlot) *batteryForecastSlot {
	s := site.lms()

	s.mu.Lock()
	raised := s.floorRaised
	s.mu.Unlock()

	if low != nil && low.limit && raised {
		low.limit = false
	}
	return low
}

// setLevel raises the goal of slot i to v, the goals sized to n steps
func setLevel(goals []float32, n, i int, v float32) []float32 {
	if len(goals) != n {
		goals = make([]float32, n)
	}
	if i < n {
		goals[i] = max(goals[i], v)
	}
	return goals
}

// slotAfter returns the index of the time step in which d has passed
func slotAfter(dt []int, d time.Duration) int {
	var elapsed time.Duration
	for i, s := range dt {
		elapsed += time.Duration(s) * time.Second
		if elapsed >= d {
			return i
		}
	}
	return max(0, len(dt)-1)
}

// peakShavingConfigured returns whether peak shaving is on and set up, with
// its limit and reserve soc
func (site *Site) peakShavingConfigured() (bool, float64, float64) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.enabled && s.set != nil, s.limit, s.reserve
}

// lmGridChargeBlocked reports without side effects whether grid charging is
// currently refused: held off after load management shed it, or without a
// known charge power on a circuit. A peak pausing it is not: that holds only
// while the demand exceeds the limit, which the import limit already plans.
func (site *Site) lmGridChargeBlocked() bool {
	if site.lmBatteryCircuit() == nil {
		return false
	}

	if power, _ := site.lmBatteryChargePower(); power <= 0 {
		return true
	}

	s := site.lms()
	s.mu.Lock()
	shedUntil := s.batteryShedUntil
	s.mu.Unlock()

	return time.Now().Before(shedUntil)
}
