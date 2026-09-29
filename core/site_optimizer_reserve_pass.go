package core

// Custom extension: the peak shaving reserve in the optimizer's plan. Below
// the reserve the fork discharges the battery only for what exceeds the peak
// limit (see site_peakshaving.go), and it keeps what charges there for peaks.
// The optimizer knows one minimum soc for everything, so the plan is solved
// with the reserve (and start soc) as minimum first. Where that plan leaves
// peaks uncovered, or the battery is below that floor now, the floor becomes a
// minimum per slot that follows what the fork does, and the plan is solved
// again with the battery's own minimum: lowered by the peaks it covers, raised
// by what really charges (pv surplus, running or one-time grid charging).
// Grid charging goals are placed where the fork gets there, with peak shaving
// only with the room below the limit. Only the plan changes, nothing is
// switched; a pass that fails or is worse keeps the plan before it.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/evcc-io/evcc/util/sponsor"
	optimizer "github.com/evcc-io/optimizer/client"
)

// lmGridGoal is grid charging the fork runs: up to level by slot
type lmGridGoal struct {
	slot  int
	level float32
}

// lmPlan is what the passes need to know about the home battery's inputs
type lmPlan struct {
	devMin     float32 // the battery's own minimum soc (Wh)
	floor0     float32 // minimum soc of the first pass (Wh)
	target     float32 // the floor the fork holds: reserve, start soc (Wh)
	reserve    float32 // peak shaving reserve (Wh), 0 without peak shaving
	limit      float32 // peak limit (W), 0 without peak shaving
	power      float32 // grid charge power (W)
	controlled bool    // grid charge power set through an entity, trimmed to the room below the limit
	etaC, etaD float32 // efficiencies
	grid       []lmGridGoal
}

func (site *Site) newLmPlan(bat optimizer.BatteryConfig) lmPlan {
	power, _ := site.lmBatteryChargePower()
	if power <= 0 {
		power = float64(bat.CMax)
	}
	return lmPlan{
		devMin:     bat.SMin,
		power:      float32(power),
		controlled: site.chargePowerControlled(),
		etaC:       float32(site.identChargeEta()),
		etaD:       eta,
	}
}

func (site *Site) lmPlanInputs() *lmPlan {
	s := site.lms()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plan
}

// at is the value of v in slot i, 0 beyond it
func at(v []float32, i int) float32 {
	if i < len(v) {
		return v[i]
	}
	return 0
}

// gridCharge is the energy grid charging stores in slot i: the charge power,
// with peak shaving only what fits below the limit on top of the demand (ev:
// what the vehicles charge), not at all while the demand exceeds it
func (p lmPlan) gridCharge(ts optimizer.TimeSeries, bat optimizer.BatteryConfig, i int, ev []float32) float32 {
	h := float32(ts.Dt[i]) / 3600
	power := p.power
	if bat.CMax > 0 {
		power = min(power, bat.CMax)
	}

	if p.limit > 0 {
		net := (at(ts.Gt, i) - at(ts.Ft, i) + at(ev, i)) / h
		switch {
		case p.controlled:
			if power = min(power, max(0, p.limit-max(0, net))); power < minGridChargePower {
				power = 0
			}
		case net > p.limit:
			power = 0
		}
	}

	return power * h * p.etaC
}

// chargeStep is the stored energy after slot i of grid charging from en: while
// a peak pauses it, the battery covers what exceeds the limit, above the
// reserve it runs freely
func (p lmPlan) chargeStep(ts optimizer.TimeSeries, bat optimizer.BatteryConfig, i int, en float32, ev []float32) float32 {
	h := float32(ts.Dt[i]) / 3600
	if net := at(ts.Gt, i) - at(ts.Ft, i) + at(ev, i); p.limit > 0 && net > p.limit*h {
		drain := net - p.limit*h
		if en > p.reserve+1 {
			drain = net
		}
		en = max(min(en, p.devMin), en-min(drain, bat.DMax*h)/p.etaD)
	}
	en += p.gridCharge(ts, bat, i, ev)
	return min(en, bat.SMax)
}

// chargeLevels is the stored energy after each slot from `from` on while grid
// charging from en runs up to goal, as the fork charges, and the slot it gets
// there; -1 if it never does within the horizon
func (p lmPlan) chargeLevels(ts optimizer.TimeSeries, bat optimizer.BatteryConfig, from int, en, goal float32, ev []float32) ([]float32, int) {
	var levels []float32
	for i := from; i < len(ts.Dt); i++ {
		if en = p.chargeStep(ts, bat, i, en, ev); en >= goal-1 {
			return append(levels, goal), i
		}
		levels = append(levels, en)
	}
	return nil, -1
}

// chargeSlot is the slot from which on grid charging from en reaches goal, -1
// if it never does within the horizon
func (p lmPlan) chargeSlot(ts optimizer.TimeSeries, bat optimizer.BatteryConfig, from int, en, goal float32, ev []float32) int {
	_, i := p.chargeLevels(ts, bat, from, en, goal, ev)
	return i
}

// capLevels ends charge levels starting at slot from with the goal by slot
// last at the latest
func capLevels(levels []float32, from, last int, goal float32) []float32 {
	if n := last - from + 1; n > 0 && n < len(levels) {
		levels = append(levels[:n-1:n-1], goal)
	}
	return levels
}

// chargeBy is the stored energy grid charging from en reaches by slot until
func (p lmPlan) chargeBy(ts optimizer.TimeSeries, bat optimizer.BatteryConfig, en float32, until int) float32 {
	for i := 0; i <= until && i < len(ts.Dt); i++ {
		en = p.chargeStep(ts, bat, i, en, nil)
	}
	return en
}

// others is what the batteries other than home charge in each slot of a plan
func others(res *optimizer.OptimizationResult, home, n int) []float32 {
	ev := make([]float32, n)
	for j, b := range res.Batteries {
		if j == home {
			continue
		}
		for i := range min(n, len(b.ChargingPower)) {
			ev[i] += b.ChargingPower[i]
		}
	}
	return ev
}

// floor is the minimum per slot the fork holds the battery to, following the
// plan res; nil where it is the first pass's minimum throughout
func (p lmPlan) floor(req *optimizer.OptimizationInput, home int, res *optimizer.OptimizationResult) []float32 {
	ts, bat := req.TimeSeries, req.Batteries[home]
	rb := res.Batteries[home]
	s1 := rb.StateOfCharge
	n := len(ts.Dt)
	if len(s1) != n || len(rb.DischargingPower) != n {
		return nil
	}

	etaC, etaD := req.EtaC, req.EtaD
	if etaC <= 0 || etaD <= 0 {
		etaC, etaD = eta, eta
	}
	ev := others(res, home, n)

	// z follows the battery as the fork runs it. Above the floor it is the plan
	// less what the fork spent on peaks the plan leaves uncovered (short);
	// below it only what exceeds the limit leaves the battery.
	z, short := bat.SInitial, float32(0)
	below := z < p.target-1
	lowered := false

	out := make([]float32, n)
	for i := range n {
		h := float32(ts.Dt[i]) / 3600
		net := at(ts.Gt, i) - at(ts.Ft, i)
		load := net + ev[i]
		surplus := max(0, -load)

		if !below {
			if p.limit > 0 {
				if room := bat.DMax*h - rb.DischargingPower[i]; at(res.GridImportOvershoot, i) > 1 && room > 1 {
					short += min(at(res.GridImportOvershoot, i), room) / etaD
				}
			}
			short = max(0, short-max(at(res.GridExport, i), surplus)*etaC) // pv surplus refills it

			if zi := s1[i] - short; zi >= p.target-1 && s1[i] >= p.target-1 {
				z = zi
			} else {
				// reaching the floor within this slot: free down to it, below
				// only what exceeds the limit
				below = true
				free := max(0, min(max(0, load)/etaD, z-p.target))
				var exc float32
				if p.limit > 0 {
					exc = min(max(0, max(0, load)-free*etaD-p.limit*h), bat.DMax*h) / etaD
				}
				z = max(p.devMin, z-free-exc)
				if out[i] = min(p.target, z); out[i] < p.floor0-1 {
					lowered = true
				}
				continue
			}
		}

		if below {
			// what really charges below the floor: pv surplus, and running or
			// one-time grid charging up to its goal
			charge := min(surplus, bat.CMax*h) * etaC
			for _, g := range p.grid {
				if i <= g.slot && z < g.level {
					charge = max(charge, min(g.level-z, p.gridCharge(ts, bat, i, ev)+charge))
				}
			}

			var dis float32
			if p.limit > 0 && load > p.limit*h {
				dis = load - p.limit*h
				if z > p.reserve+1 {
					dis = load // above the reserve a paused grid charge leaves the battery free
				}
			}
			z += charge - min(dis, bat.DMax*h)/etaD

			if z >= p.target-1 && s1[i] >= p.target-1 {
				below = false
				short = max(0, s1[i]-z)
			}
		}

		z = max(p.devMin, z)
		z = min(z, bat.SMax)
		if out[i] = min(p.target, z); out[i] < p.floor0-1 {
			lowered = true
		}
	}

	if !lowered && p.floor0 >= p.target-1 {
		return nil
	}
	return out
}

// lmSolve runs a further pass
func lmSolve(client *optimizer.ClientWithResponses, req optimizer.OptimizationInput) (*optimizer.OptimizationResult, error) {
	resp, err := client.PostOptimizeChargeScheduleWithResponse(context.TODO(), req, func(_ context.Context, r *http.Request) error {
		if sponsor.IsAuthorizedForApi() {
			r.Header.Set("Authorization", "Bearer "+sponsor.Token)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, errors.New(resp.Status())
	}
	return resp.JSON200, nil
}

// lmPlanValid reports whether a pass's plan is usable: solved, complete, and
// the home battery within its bounds
func lmPlanValid(req optimizer.OptimizationInput, home int, res *optimizer.OptimizationResult) bool {
	if res == nil || (res.Status != optimizer.Optimal && res.Status != optimizer.Feasible) || len(res.Batteries) != len(req.Batteries) {
		return false
	}
	n := len(req.TimeSeries.Dt)
	for _, b := range res.Batteries {
		if len(b.StateOfCharge) != n || len(b.ChargingPower) != n || len(b.DischargingPower) != n {
			return false
		}
	}
	if len(res.GridImport) != n || len(res.GridExport) != n {
		return false
	}

	bat := req.Batteries[home]
	lo, hi := min(bat.SMin, bat.SInitial)-50, max(bat.SMax, bat.SInitial)+50
	for _, v := range res.Batteries[home].StateOfCharge {
		if v < lo || v > hi {
			return false
		}
	}
	return true
}

func sumOf(v []float32) float32 {
	var res float32
	for _, x := range v {
		res += x
	}
	return res
}

// lmHomeBattery is the index of the only home battery, -1 without exactly one
func lmHomeBattery(details requestDetails, req *optimizer.OptimizationInput, res *optimizer.OptimizationResult) int {
	home := -1
	for i, d := range details.BatteryDetails {
		if d.Type != batteryTypeBattery || i >= len(req.Batteries) {
			continue
		}
		if home >= 0 {
			return -1
		}
		home = i
	}
	if home < 0 || len(res.Batteries) != len(req.Batteries) || req.Batteries[home].SCapacity <= 0 {
		return -1
	}
	return home
}

// withFloor is req with the home battery's own minimum and floor as goals
func withFloor(req optimizer.OptimizationInput, home int, plan lmPlan, floor, goals []float32) optimizer.OptimizationInput {
	req.Batteries = slices.Clone(req.Batteries)
	b := &req.Batteries[home]
	b.SMin = plan.devMin
	b.SGoal = make([]float32, len(floor))
	for i, f := range floor {
		b.SGoal[i] = max(f, at(goals, i))
	}
	return req
}

// lmReservePass solves the plan again with the floor as the fork holds it,
// see above. It returns the floor used, nil if the first plan stays.
func (site *Site) lmReservePass(client *optimizer.ClientWithResponses, req *optimizer.OptimizationInput, home int, plan lmPlan, res *optimizer.OptimizationResult) []float32 {
	if plan.target <= plan.devMin+1 {
		return nil
	}

	floor := plan.floor(req, home, res)
	if floor == nil {
		return nil
	}

	goals := slices.Clone(req.Batteries[home].SGoal)
	req2 := withFloor(*req, home, plan, floor, goals)
	res2, err := lmSolve(client, req2)
	if err != nil || !lmPlanValid(req2, home, res2) {
		site.log.DEBUG.Printf("optimizer: reserve pass: %v", passErr(err, res2))
		return nil
	}

	over := sumOf(res.GridImportOvershoot)
	over2 := sumOf(res2.GridImportOvershoot)
	if over2 > over+1 {
		site.log.DEBUG.Printf("optimizer: reserve pass: more over the limit (%.0f Wh, first pass %.0f Wh)", over2, over)
		return nil
	}

	// the floor checked against this plan: where it drifted from the first
	// one, once more with the floor it implies
	if floor2 := plan.floor(&req2, home, res2); floor2 != nil {
		s2 := res2.Batteries[home].StateOfCharge
		if drifted(floor2, s2) {
			req3 := withFloor(*req, home, plan, floor2, goals)
			if res3, err := lmSolve(client, req3); err == nil && lmPlanValid(req3, home, res3) && sumOf(res3.GridImportOvershoot) <= over2+1 {
				req2, res2, floor = req3, res3, floor2
			} else {
				site.log.DEBUG.Printf("optimizer: reserve pass recheck: %v", passErr(err, res3))
			}
		}
	}

	*req, *res = req2, *res2

	// the minimum is the battery's own now, reaching it is empty
	s := site.lms()
	s.mu.Lock()
	s.floorRaised = false
	s.mu.Unlock()

	site.log.DEBUG.Printf("optimizer: reserve pass, lowest floor %.0f Wh", slices.Min(floor))

	return floor
}

// drifted reports whether the plan s goes below the floor f anywhere
func drifted(f, s []float32) bool {
	for i := range min(len(f), len(s)) {
		if f[i] > s[i]+20 {
			return true
		}
	}
	return false
}

func passErr(err error, res *optimizer.OptimizationResult) any {
	switch {
	case err != nil:
		return err
	case res == nil:
		return "no result"
	default:
		return "unusable result: " + string(res.Status)
	}
}

// lmOptimizeLater remembers a forced run requested while one is running: a
// changed setting or grid charging starting then waits for the next slot
// otherwise, which with several passes per run happens often
func (site *Site) lmOptimizeLater(minAge time.Duration) {
	if minAge == 0 {
		site.lms().optimizeAgain.Store(true)
	}
}

// lmOptimizeAgain runs a remembered forced run once the running one is done
func (site *Site) lmOptimizeAgain() {
	if site.lms().optimizeAgain.Swap(false) {
		go site.optimizerUpdateAsync(0)
	}
}

// lmOptimizerPasses runs the passes after the first solve: the reserve as
// the fork holds it, then soc-based grid charging
func (site *Site) lmOptimizerPasses(client *optimizer.ClientWithResponses, req *optimizer.OptimizationInput, details requestDetails, res *optimizer.OptimizationResult) {
	if res == nil || (res.Status != optimizer.Optimal && res.Status != optimizer.Feasible) {
		return
	}

	plan := site.lmPlanInputs()
	home := lmHomeBattery(details, req, res)
	if plan == nil || home < 0 {
		return
	}

	floor := site.lmReservePass(client, req, home, *plan, res)
	site.lmSocChargePass(client, req, home, *plan, floor, res)
}
