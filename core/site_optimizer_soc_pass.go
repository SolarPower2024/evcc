package core

// Custom extension: soc-based grid charging in the optimizer's plan. The fork
// charges the battery from the grid once it falls to the start soc, up to the
// stop soc. The optimizer only knows minimums and cannot plan that switch:
// given the stop soc as goal it simply stops discharging there, which the fork
// does not do. So the plan is solved twice:
//
//  1. as requested, the start soc as minimum: the battery discharges down to it
//  2. from the slot it reaches the start soc on, with the plan's state then as
//     starting point and the stop soc as goal after the charging time
//
// and joined. The plan then shows the discharge to the start soc and the grid
// charge to the stop soc as they will happen; whether it charges again later
// is up to the optimizer. Only the plan changes, nothing is switched.

import (
	"context"
	"net/http"

	"github.com/evcc-io/evcc/util/sponsor"
	optimizer "github.com/evcc-io/optimizer/client"
)

// socPassStart returns the home battery and the first slot at whose end its
// plan reaches the start soc, -1 without soc-based grid charging to plan
func (site *Site) socPassStart(req *optimizer.OptimizationInput, details requestDetails, res *optimizer.OptimizationResult) (home, slot int, goal float32) {
	s := site.lms()
	s.mu.Lock()
	on, running, start, stop := s.socChargeEnabled, s.socChargeRunning, s.socChargeStart, s.socChargeStop
	s.mu.Unlock()

	// running: the stop soc is a goal of the first pass already
	if !on || running || len(res.Batteries) != len(req.Batteries) {
		return -1, -1, 0
	}

	home = -1
	for i, d := range details.BatteryDetails {
		if d.Type != batteryTypeBattery || i >= len(req.Batteries) {
			continue
		}
		if home >= 0 {
			return -1, -1, 0 // one home battery only
		}
		home = i
	}
	if home < 0 || !req.Batteries[home].ChargeFromGrid || req.Batteries[home].SCapacity <= 0 {
		return -1, -1, 0
	}

	bat := req.Batteries[home]
	top := bat.SMax
	if top <= 0 {
		top = bat.SCapacity
	}
	startWh := bat.SCapacity * float32(start) / 100
	goal = min(bat.SCapacity*float32(stop)/100, top)

	// the start soc must be the minimum: a peak shaving reserve above it keeps
	// the battery from ever getting there
	if bat.SMin > startWh+1 || goal <= startWh {
		return -1, -1, 0
	}

	for i, v := range res.Batteries[home].StateOfCharge {
		if v <= startWh+1 {
			// a second pass needs some slots left
			if i+1 >= len(req.TimeSeries.Dt)-2 {
				return -1, -1, 0
			}
			return home, i, goal
		}
	}

	return -1, -1, 0
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

// lmSocChargePass solves the rest of the horizon again from where the battery
// reaches the start soc of soc-based grid charging and joins both plans. The
// goal is added to the request, so the published request shows it. On any
// error the first plan stays.
func (site *Site) lmSocChargePass(client *optimizer.ClientWithResponses, req *optimizer.OptimizationInput, details requestDetails, res *optimizer.OptimizationResult) {
	if res == nil || (res.Status != optimizer.Optimal && res.Status != optimizer.Feasible) {
		return
	}

	home, k, goal := site.socPassStart(req, details, res)
	if k < 0 {
		return
	}

	req2 := socPassRequest(*req, res, k)
	bat := req2.Batteries[home]
	i := slotAfter(req2.TimeSeries.Dt, site.socChargeDuration(bat, bat.SInitial, goal))
	if len(bat.SGoal) != len(req2.TimeSeries.Dt) {
		bat.SGoal = make([]float32, len(req2.TimeSeries.Dt))
	}
	bat.SGoal[i] = max(bat.SGoal[i], goal)
	req2.Batteries[home] = bat

	resp, err := client.PostOptimizeChargeScheduleWithResponse(context.TODO(), req2, func(_ context.Context, r *http.Request) error {
		if sponsor.IsAuthorizedForApi() {
			r.Header.Set("Authorization", "Bearer "+sponsor.Token)
		}
		return nil
	})
	if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		site.log.DEBUG.Printf("optimizer: soc grid charge pass: %v", cmpErr(err, resp))
		return
	}
	res2 := *resp.JSON200
	if (res2.Status != optimizer.Optimal && res2.Status != optimizer.Feasible) || len(res2.Batteries) != len(res.Batteries) {
		site.log.DEBUG.Printf("optimizer: soc grid charge pass: %s", res2.Status)
		return
	}

	socPassJoin(res, res2, k)

	// the goal as part of the request, for the optimizer page
	b := &req.Batteries[home]
	if len(b.SGoal) != len(req.TimeSeries.Dt) {
		b.SGoal = make([]float32, len(req.TimeSeries.Dt))
	}
	b.SGoal[k+1+i] = max(b.SGoal[k+1+i], goal)

	site.log.DEBUG.Printf("optimizer: soc grid charge planned from slot %d, stop soc by slot %d", k+1, k+1+i)
}

func cmpErr(err error, resp *optimizer.PostOptimizeChargeScheduleResponse) any {
	if err != nil {
		return err
	}
	if resp != nil {
		return resp.Status()
	}
	return "no response"
}
