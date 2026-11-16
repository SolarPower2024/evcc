package core

// Custom extension: the power the charge planner plans with. evcc's planner
// divides the energy by the loadpoint's maximum power, which knows the wall box,
// the vehicle and the loadpoint's own circuit. What the main circuit leaves is
// not in it: the peak limit, the limit of the main circuit and what the home
// draws meanwhile. The site keeps these per cycle, so the loadpoint reads them
// without the site's lock (loadpoint_planpower.go).

import (
	"sync"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/core/circuit"
	optimizer "github.com/evcc-io/optimizer/client"
)

const (
	// planHomeMaxAge is how old the optimizer request may be for its home forecast to count
	planHomeMaxAge = 2 * time.Hour
	// planHomeDefaultSpan is the window the home draw is averaged over for a plan without time
	planHomeDefaultSpan = 12 * time.Hour

	planReasonPeak    = "peak"
	planReasonCircuit = "circuit"
)

// planHomeSlot is the home draw in W over a stretch of time
type planHomeSlot struct {
	start, end time.Time
	power      float64
}

// planPowerState is the cache of the charge planner's inputs
type planPowerState struct {
	mu    sync.Mutex
	clock clock.Clock // for tests, nil = real time

	limit     float64        // peak limit in W, 0 = none
	rootPower float64        // limit of the main circuit in W (current on 3 phases), 0 = none
	home      []planHomeSlot // home forecast of the last optimizer request
	homeAt    time.Time      // time of that request
}

// planPower returns the state of the charge planner's inputs
func (site *Site) planPower() *planPowerState {
	return &site.custom.planPower
}

// now returns the current time
func (s *planPowerState) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

// updatePlanPower takes the peak limit and the limit of the main circuit, once per cycle
func (site *Site) updatePlanPower() {
	p := site.peak()
	p.mu.Lock()
	limit := p.limit
	p.mu.Unlock()

	var root float64
	if c := circuit.Root(); c != nil {
		root = circuitLimit(c, 3)
	}

	s := site.planPower()
	s.mu.Lock()
	s.limit, s.rootPower = limit, root
	s.mu.Unlock()
}

// setPlanHomeForecast keeps the home forecast of an optimizer request: the energy
// per slot in Wh over the slot's seconds, from at on. It holds the home profile
// without the vehicles the optimizer plans, with heating demand and the load of
// vehicles it cannot model.
func (site *Site) setPlanHomeForecast(req optimizer.OptimizationInput, at time.Time) {
	ts := req.TimeSeries

	res := make([]planHomeSlot, 0, len(ts.Dt))
	start := at
	for i, dt := range ts.Dt {
		if i >= len(ts.Gt) {
			break
		}
		if dt <= 0 {
			continue
		}
		end := start.Add(time.Duration(dt) * time.Second)
		res = append(res, planHomeSlot{start: start, end: end, power: float64(ts.Gt[i]) * 3600 / float64(dt)})
		start = end
	}

	s := site.planPower()
	s.mu.Lock()
	s.home, s.homeAt = res, at
	s.mu.Unlock()
}

// homeMean returns the mean home draw in W between now and until, false without
// a forecast of at most two hours old or without one for that time. Without a
// time, or one that has passed, it is the next twelve hours.
func (s *planPowerState) homeMean(until time.Time) (float64, bool) {
	now := s.now()
	if !until.After(now) {
		until = now.Add(planHomeDefaultSpan)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.homeAt.IsZero() || now.Sub(s.homeAt) > planHomeMaxAge {
		return 0, false
	}

	var wh, span float64
	for _, slot := range s.home {
		from, to := slot.start, slot.end
		if from.Before(now) {
			from = now
		}
		if to.After(until) {
			to = until
		}
		if !to.After(from) {
			continue
		}

		d := to.Sub(from).Hours()
		wh += slot.power * d
		span += d
	}

	if span <= 0 {
		return 0, false
	}
	return wh / span, true
}

// planRoom returns what the main circuit leaves a loadpoint for a plan ending at
// until and why: the lower of the peak limit and the main circuit's limit, less
// the home draw expected until then (none without a fresh forecast), 0 without
// a limit. The reason is the peak limit if that is the lower one, else the circuit.
func (site *Site) planRoom(until time.Time) (float64, string) {
	s := site.planPower()

	s.mu.Lock()
	limit, root := s.limit, s.rootPower
	s.mu.Unlock()

	room, reason := root, planReasonCircuit
	if limit > 0 && (room <= 0 || limit <= room) {
		room, reason = limit, planReasonPeak
	}
	if room <= 0 {
		return 0, ""
	}

	if home, ok := s.homeMean(until); ok {
		// 0 means no limit, the loadpoint holds its minimum power instead
		room = max(room-home, 1)
	}

	return room, reason
}
