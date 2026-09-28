package core

// Custom extension: soc-based grid charging in the optimizer's plan. The fork
// charges the battery from the grid once it falls to the start soc, up to the
// stop soc. The optimizer only knows minimums and cannot plan that switch:
// given the stop soc as goal it simply stops discharging there, which the fork
// does not do. So the plan is solved again:
//
//  1. as requested (after the reserve pass, see site_optimizer_reserve_pass.go):
//     the battery discharges down to the start soc
//  2. from the slot it reaches the start soc on, with the plan's state then as
//     starting point and the stop soc as goal once the fork has charged it,
//     with peak shaving only with the room below the limit
//
// and joined. The plan then shows the discharge to the start soc and the grid
// charge to the stop soc as they will happen; whether it charges again later
// is up to the optimizer. Only the plan changes, nothing is switched.

import (
	optimizer "github.com/evcc-io/optimizer/client"
)

// socPassStart returns the first slot at whose end the home battery's plan
// reaches the start soc and the stop soc, -1 without soc-based grid charging to
// plan
func (site *Site) socPassStart(req *optimizer.OptimizationInput, home int, res *optimizer.OptimizationResult) (slot int, goal float32) {
	s := site.lms()
	s.mu.Lock()
	on, running, start, stop := s.socChargeEnabled, s.socChargeRunning, s.socChargeStart, s.socChargeStop
	s.mu.Unlock()

	// running: the stop soc is a goal of the first pass already
	if !on || running || home < 0 || home >= len(req.Batteries) || home >= len(res.Batteries) {
		return -1, 0
	}

	bat := req.Batteries[home]
	if !bat.ChargeFromGrid || bat.SCapacity <= 0 {
		return -1, 0
	}

	top := bat.SMax
	if top <= 0 {
		top = bat.SCapacity
	}
	startWh := bat.SCapacity * float32(start) / 100
	goal = min(bat.SCapacity*float32(stop)/100, top)

	// the start soc must be reachable: a floor above it keeps the battery from
	// ever getting there
	if bat.SMin > startWh+1 || goal <= startWh {
		return -1, 0
	}

	for i, v := range res.Batteries[home].StateOfCharge {
		if v <= startWh+1 {
			// a second pass needs some slots left
			if i+1 >= len(req.TimeSeries.Dt)-2 {
				return -1, 0
			}
			return i, goal
		}
	}

	return -1, 0
}

// socPassRequest is the request for the rest of the horizon after slot k
func socPassRequest(req optimizer.OptimizationInput, res *optimizer.OptimizationResult, k int) optimizer.OptimizationInput {
	from := k + 1
	cut := func(v []float32) []float32 {
		if len(v) <= from {
			return nil
		}
		return append([]float32(nil), v[from:]...)
	}

	out := req
	ts := req.TimeSeries
	out.TimeSeries = optimizer.TimeSeries{
		Dt: append([]int(nil), ts.Dt[from:]...),
		Gt: cut(ts.Gt),
		Ft: cut(ts.Ft),
		PN: cut(ts.PN),
		PE: cut(ts.PE),
	}

	out.Batteries = make([]optimizer.BatteryConfig, len(req.Batteries))
	for j, b := range req.Batteries {
		r := res.Batteries[j]
		b.SInitial = r.StateOfCharge[k]
		b.CActive = r.ChargingPower[k] > 0
		b.SMin = min(b.SMin, b.SInitial)
		b.SMax = max(b.SMax, b.SInitial)
		b.PDemand = cut(b.PDemand)
		b.SGoal = cut(b.SGoal)
		out.Batteries[j] = b
	}

	return out
}

// socPassJoin joins the plan of the second pass onto the first after slot k
func socPassJoin(res *optimizer.OptimizationResult, res2 optimizer.OptimizationResult, k int) {
	join := func(a, b []float32) []float32 {
		if len(a) <= k || len(b) == 0 {
			return a
		}
		return append(a[:k+1:k+1], b...)
	}

	for j := range res.Batteries {
		b, b2 := &res.Batteries[j], res2.Batteries[j]
		b.StateOfCharge = join(b.StateOfCharge, b2.StateOfCharge)
		b.ChargingPower = join(b.ChargingPower, b2.ChargingPower)
		b.DischargingPower = join(b.DischargingPower, b2.DischargingPower)
	}

	res.GridImport = join(res.GridImport, res2.GridImport)
	res.GridExport = join(res.GridExport, res2.GridExport)
	res.GridImportOvershoot = join(res.GridImportOvershoot, res2.GridImportOvershoot)
	res.GridExportOvershoot = join(res.GridExportOvershoot, res2.GridExportOvershoot)
	if len(res.FlowDirection) > k && len(res2.FlowDirection) > 0 {
		res.FlowDirection = append(res.FlowDirection[:k+1:k+1], res2.FlowDirection...)
	}

	res.ObjectiveValue += res2.ObjectiveValue
	res.LimitViolations.GridImportLimitExceeded = res.LimitViolations.GridImportLimitExceeded || res2.LimitViolations.GridImportLimitExceeded
	res.LimitViolations.GridExportLimitHit = res.LimitViolations.GridExportLimitHit || res2.LimitViolations.GridExportLimitHit
	if res2.Status != optimizer.Optimal {
		res.Status = res2.Status
	}
}

// socPassGoals are the goals of the second pass (indexed from slot k+1): the
// charge levels as the fork charges (from slot k+1 on), the stop soc in slot g
// and, after it, the floor raised by what the grid charge adds below it, until
// pv has refilled the floor anyway
func socPassGoals(goals []float32, k, g int, stop float32, levels, floor []float32, target float32) []float32 {
	from := k + 1
	for j, v := range levels {
		if j < len(goals) {
			goals[j] = max(goals[j], v)
		}
	}
	goals[g-from] = max(goals[g-from], stop)
	if floor == nil || g >= len(floor) {
		return goals
	}

	raised := min(target, max(stop, floor[g]))
	for i := g + 1; i < len(floor) && i-from < len(goals); i++ {
		raised = max(floor[i], min(target, raised+floor[i]-floor[i-1]))
		goals[i-from] = max(goals[i-from], raised)
	}
	return goals
}

// lmSocChargePass solves the rest of the horizon again from where the battery
// reaches the start soc of soc-based grid charging and joins both plans. The
// goals are added to the request, so the published request shows them. On any
// error the plan before stays.
func (site *Site) lmSocChargePass(client *optimizer.ClientWithResponses, req *optimizer.OptimizationInput, home int, plan lmPlan, floor []float32, res *optimizer.OptimizationResult) {
	k, stop := site.socPassStart(req, home, res)
	if k < 0 {
		return
	}

	bat := req.Batteries[home]
	soc := res.Batteries[home].StateOfCharge
	levels, g := plan.chargeLevels(req.TimeSeries, bat, k+1, soc[k], stop, others(res, home, len(req.TimeSeries.Dt)))
	if g < 0 {
		site.log.DEBUG.Println("optimizer: soc grid charge pass: no room below the limit within the horizon")
		return
	}
	if plan.limit <= 0 {
		g = min(g, k+1+slotAfter(req.TimeSeries.Dt[k+1:], site.gridChargeWindow()))
		levels = capLevels(levels, k+1, g, stop)
	}

	req2 := socPassRequest(*req, res, k)
	b := &req2.Batteries[home]
	if len(b.SGoal) != len(req2.TimeSeries.Dt) {
		b.SGoal = make([]float32, len(req2.TimeSeries.Dt))
	}
	b.SGoal = socPassGoals(b.SGoal, k, g, stop, levels, floor, plan.target)

	res2, err := lmSolve(client, req2)
	if err != nil || !lmPlanValid(req2, home, res2) {
		site.log.DEBUG.Printf("optimizer: soc grid charge pass: %v", passErr(err, res2))
		return
	}

	socPassJoin(res, *res2, k)

	// the goals as part of the request, for the optimizer page
	rb := &req.Batteries[home]
	if len(rb.SGoal) != len(req.TimeSeries.Dt) {
		rb.SGoal = make([]float32, len(req.TimeSeries.Dt))
	}
	for i, v := range b.SGoal {
		rb.SGoal[k+1+i] = max(rb.SGoal[k+1+i], v)
	}

	site.log.DEBUG.Printf("optimizer: soc grid charge planned from slot %d, stop soc by slot %d", k+1, g)
}
