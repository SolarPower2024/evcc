package core

// Custom extension: the fork's settings as optimizer inputs, see
// site_optimizer_gate.go for the gate the automatic mode passes.
//
// Inputs, not overrides: what the optimizer has to respect goes into its
// request, so its plan already contains it, in the advisory as well as in the
// automatic mode:
//
//   - peak shaving: the peak limit as hard grid import limit, the reserve as
//     the home battery's minimum soc
//   - soc-based grid charging: the start soc as minimum soc, so the optimizer
//     plans the charging ahead before the battery would fall below it; while
//     charging runs the stop soc as goal within the grid charge window
//   - grid charging the load management or a peak currently refuses is not
//     offered (charge_from_grid off)
//   - load management: a loadpoint plans with at most its circuits' power, and
//     the priorities rank the batteries (c_priority)
//   - a price tariff set as planner tariff is the grid price the optimizer
//     plans with, statistics keep the grid tariff
//
// Without circuits, peak shaving and soc-based grid charging the request is
// unchanged.

import (
	"cmp"
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
	s.mu.Unlock()

	if peakOn && limit > 0 && (req.Grid.PMaxImp == 0 || float32(limit) < req.Grid.PMaxImp) {
		req.Grid.PMaxImp = float32(limit)
	}

	for i := range batteries {
		b := &batteries[i]

		switch b.detail.Type {
		case batteryTypeBattery:
			site.applyBatteryIdent(&b.cfg, &b.detail)
			site.applyLmBatteryInputs(&b.cfg, req, peakOn, reserve)

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

// applyLmBatteryInputs sets the home battery's minimum soc and grid charge goal
func (site *Site) applyLmBatteryInputs(bat *optimizer.BatteryConfig, req *optimizer.OptimizationInput, peakOn bool, reserve float64) {
	if bat.SCapacity <= 0 {
		return
	}

	dt := req.TimeSeries.Dt
	top := bat.SMax
	if top <= 0 {
		top = bat.SCapacity
	}

	wh := func(soc float64) float32 { return bat.SCapacity * float32(soc) / 100 }

	var floor float32
	if peakOn {
		floor = wh(reserve)
	}

	s := site.lms()

	s.mu.Lock()
	socOn, start, stop, running := s.socChargeEnabled, s.socChargeStart, s.socChargeStop, s.socChargeRunning
	s.mu.Unlock()

	if site.lmGridChargeBlocked() {
		bat.ChargeFromGrid = false
	}

	if socOn && bat.ChargeFromGrid {
		floor = max(floor, wh(start))

		// the stop soc within the window from each time charging starts: now
		// while it runs, else where the battery is expected to fall to the start soc
		goal := min(wh(stop), top)
		for _, i := range socChargeGoals(*bat, req, running, wh(start), goal, site.gridChargeWindow()) {
			if len(bat.SGoal) != len(dt) {
				bat.SGoal = make([]float32, len(dt))
			}
			bat.SGoal[i] = max(bat.SGoal[i], goal)
		}
	}

	// one-time grid charging: the target by the chosen time, or as early as
	// the charge power can reach it, see site_lm_once.go
	if o := site.gridChargeOnce(); o.Target > 0 && bat.ChargeFromGrid {
		if goal := wh(o.Target); goal > bat.SInitial {
			d := time.Until(o.Until)
			if o.Until.IsZero() {
				d = site.onceRequiredDuration(o.Target, float64(bat.SInitial/bat.SCapacity*100))
				// unknown grid charge power: the battery's maximum charge power
				if d <= 0 && bat.CMax > 0 {
					d = time.Duration(float64(goal-bat.SInitial) / (float64(bat.CMax) * site.identChargeEta()) * float64(time.Hour))
				}
			}
			if len(bat.SGoal) != len(dt) {
				bat.SGoal = make([]float32, len(dt))
			}
			i := slotAfter(dt, max(d, 0))
			bat.SGoal[i] = max(bat.SGoal[i], min(goal, top))
		}
	}

	// never above the current soc, the optimizer cannot start below its minimum
	if floor = min(floor, bat.SInitial); floor > bat.SMin {
		bat.SMin = floor

		s.mu.Lock()
		s.floorRaised = true
		s.mu.Unlock()
	}
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

// socChargeGoals returns the slots by which soc-based grid charging reaches
// its stop soc. Charging starts right away while running, else where the
// battery, covering the home demand the solar forecast leaves and storing its
// surplus, falls to the start soc; after reaching the stop soc it may fall
// again. The optimizer itself cannot foresee this switching.
func socChargeGoals(bat optimizer.BatteryConfig, req *optimizer.OptimizationInput, running bool, start, stop float32, window time.Duration) []int {
	ts := req.TimeSeries
	if stop <= start || len(ts.Dt) == 0 {
		return nil
	}

	top := bat.SMax
	if top <= 0 {
		top = bat.SCapacity
	}
	etaC, etaD := cmp.Or(req.EtaC, 1), cmp.Or(req.EtaD, 1)

	var res []int
	e := bat.SInitial
	for i := 0; i < len(ts.Dt); i++ {
		if running || e <= start {
			running = false
			g := i + slotAfter(ts.Dt[i:], window)
			if e < stop {
				res = append(res, g)
			}
			e = max(e, stop)
			i = g
			continue
		}

		if i >= len(ts.Gt) || i >= len(ts.Ft) {
			break
		}
		h := float32(ts.Dt[i]) / 3600
		if net := ts.Gt[i] - ts.Ft[i]; net > 0 {
			e -= min(net, bat.DMax*h) / etaD
		} else {
			e = min(top, e+min(-net, bat.CMax*h)*etaC)
		}
	}

	return res
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
// currently refused: held off after load management shed it, paused by a
// peak, or without a known charge power on a circuit
func (site *Site) lmGridChargeBlocked() bool {
	if site.lmBatteryCircuit() != nil {
		if power, _ := site.lmBatteryChargePower(); power <= 0 {
			return true
		}

		s := site.lms()
		s.mu.Lock()
		shedUntil := s.batteryShedUntil
		s.mu.Unlock()

		if time.Now().Before(shedUntil) {
			return true
		}
	}

	p := site.peak()

	p.mu.Lock()
	defer p.mu.Unlock()

	return p.enabled && p.set != nil && (p.demand > p.limit || p.clock.Now().Before(p.chargePause))
}
