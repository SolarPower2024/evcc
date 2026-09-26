package core

// Custom extension: the load management overview in the ui (Mehr →
// Lastmanagement). Published once per cycle: every load on a circuit with what
// it is doing and why, the state of battery grid charging, and the event log of
// package lm. Circuit power and the peak shaving values are published elsewhere.

import (
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util/config"
)

// load states
const (
	lmStateRunning   = "running"   // drawing what it asks for
	lmStateThrottled = "throttled" // drawing less than it asks for
	lmStateShed      = "shed"      // switched off by load management, held off until
	lmStateWaiting   = "waiting"   // wants to start, not enough room
	lmStatePaused    = "paused"    // battery grid charging paused for a peak
	lmStateOff       = "off"       // not asking for anything
)

type lmLoadStatus struct {
	Name      string     `json:"name"`
	Title     string     `json:"title"`
	Battery   bool       `json:"battery,omitempty"`
	Priority  int        `json:"priority"`
	Protected bool       `json:"protected,omitempty"` // shed guard
	Power     float64    `json:"power"`               // drawn now in W
	State     string     `json:"state"`
	Requested float64    `json:"requested,omitempty"` // asked for in W
	Allowed   float64    `json:"allowed,omitempty"`   // allowed in W
	Until     *time.Time `json:"until,omitempty"`     // shed or paused until
	Optimizer bool       `json:"optimizer,omitempty"` // the optimizer decides, load management limits
}

type lmStatus struct {
	Loads  []lmLoadStatus `json:"loads"`
	Events []lm.Event     `json:"events"`
}

// publishLmStatus publishes the overview, only once load management is in use.
// gridCharge is this cycle's battery grid charge decision.
func (site *Site) publishLmStatus(gridCharge bool) {
	if len(config.Circuits().Devices()) == 0 {
		return
	}

	now := time.Now()
	res := lmStatus{Loads: make([]lmLoadStatus, 0), Events: lm.Events()}

	if site.lmBatteryCircuit() != nil {
		res.Loads = append(res.Loads, site.lmBatteryStatus(now, gridCharge))
	}

	s := site.lms()

	for _, dev := range config.Loadpoints().Devices() {
		lp, ok := dev.Instance().(*Loadpoint)
		if !ok || lp.GetCircuit() == nil {
			continue
		}

		name := dev.Config().Name

		s.mu.Lock()
		protected := s.guarded[name] && s.guardMinutes > 0
		s.mu.Unlock()

		st := lmLoadStatus{
			Name:      name,
			Title:     lp.GetTitle(),
			Priority:  lm.Priority(lp),
			Protected: protected,
			Power:     lp.GetChargePower(),
		}
		st.State, st.Requested, st.Allowed, st.Until = lmLoadpointState(lp, st.Power, now)
		st.Optimizer = lp.optimizerControlled()

		res.Loads = append(res.Loads, st)
	}

	site.publish(keys.LmStatus, res)
}

// lmLoadpointState derives what a loadpoint is doing from its last decision
func lmLoadpointState(lp lm.Load, power float64, now time.Time) (string, float64, float64, *time.Time) {
	if left := lm.Guarded(lp, now); left > 0 {
		until := now.Add(left)
		return lmStateShed, 0, 0, &until
	}

	d, ok := lm.LastDecision(lp)

	switch {
	case power > 0 && ok && d.Throttled:
		return lmStateThrottled, d.Requested, d.Allowed, nil
	case power > 0:
		return lmStateRunning, 0, 0, nil
	case ok && d.Requested > 0 && d.Allowed < d.Requested:
		return lmStateWaiting, d.Requested, d.Allowed, nil
	}

	return lmStateOff, 0, 0, nil
}

// lmBatteryStatus returns what battery grid charging is doing. Charging from pv
// is not load management's business and shows as off.
func (site *Site) lmBatteryStatus(now time.Time, gridCharge bool) lmLoadStatus {
	bat := site.lmBattery()

	st := lmLoadStatus{
		Name:     lmBatteryName,
		Battery:  true,
		Priority: lm.Priority(bat),
		State:    lmStateOff,
		// the optimizer's charge request passed the gate, see site_optimizer_gate.go
		Optimizer: site.optimizerInControl(),
	}

	if st.Optimizer && site.GetBatteryMode() == api.BatteryCharge {
		gridCharge = true
	}

	p := site.peak()
	p.mu.Lock()
	pause, setpoint := p.chargePause, p.chargeSetpoint
	p.mu.Unlock()

	s := site.lms()
	s.mu.Lock()
	shedUntil := s.batteryShedUntil
	s.mu.Unlock()

	switch {
	case gridCharge:
		// the setpoint of dynamic charging, 0 when switched on or off
		st.State, st.Power, st.Allowed = lmStateRunning, bat.GetChargePower(), setpoint
	case now.Before(pause):
		st.State, st.Until = lmStatePaused, &pause
	case now.Before(shedUntil):
		st.State, st.Until = lmStateShed, &shedUntil
	}

	return st
}
