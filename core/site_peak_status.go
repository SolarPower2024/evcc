package core

// Custom extension: the peak status shown in the dialog of the battery page. It
// says what peak shaving does with the battery right now and why, for the battery
// types Marstek (Omnibattery) and BYD (the integration evcc Peak Fronius).
//
// Display only, nothing here feeds back into the control. The state is derived
// from what the cycle already knows (the reserve and its hysteresis, the setpoint,
// the window, the meter outage, the failed writes to Home Assistant, the battery
// mode); the ui gets a finished state and only translates it. Published as
// peakStatus at the end of the fork's part of the cycle, only when it changed.
// Changes of the state are logged as events of package lm, see
// peakStatusTransitions.

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
)

// Peak states, checked in this order
const (
	peakStateOff         = "off"         // peak shaving is off
	peakStateStorage     = "storage"     // the storage is not reachable
	peakStateMeter       = "meter"       // the grid meter failed for longer than peak.MaxGap
	peakStateGridCharge  = "gridCharge"  // evcc charges the battery from the grid
	peakStateHoldShaving = "holdShaving" // held, and a peak is being covered
	peakStateHold        = "hold"        // held
	peakStateShaving     = "shaving"     // below the reserve, covering a peak
	peakStateSaving      = "saving"      // below the reserve, nothing to cover
	peakStateFree        = "free"        // above the reserve
)

// Hints on the storage of the battery type BYD, from the status of the integration
const (
	peakHintNoSoc     = "noSoc"     // the integration sees no soc
	peakHintModeStuck = "modeStuck" // the integration does not take the mode evcc asks for
)

// peakModeStuckAfter is how long the status of the integration has to differ from
// the battery mode evcc set before it counts as not taken over
const peakModeStuckAfter = time.Minute

// statusNoEvcc and the other statuses of the integration evcc Peak Fronius: its
// status sensor, see custom_components/evcc_peak_fronius/regelung.py
const (
	bydStatusNoEvcc     = "no_evcc"
	bydStatusNoSoc      = "no_soc"
	bydStatusHold       = "hold"
	bydStatusShaving    = "shaving"
	bydStatusGridCharge = "grid_charge"
)

type peakStatusWindow struct {
	Avg     float64   `json:"avg"`     // grid power average so far in W
	Allowed float64   `json:"allowed"` // grid power still allowed for the rest in W
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

type peakStatusMonth struct {
	Peak          float64   `json:"peak"`   // highest quarter hour in W
	PeakAt        time.Time `json:"peakAt"` // its start
	Interventions int       `json:"interventions"`
}

// peakStatus is the finished state for the ui. An empty state means peak shaving
// is not set up: nothing to show.
type peakStatus struct {
	State       string            `json:"state"`
	Type        string            `json:"type,omitempty"` // byd or marstek
	Soc         float64           `json:"soc"`
	Reserve     float64           `json:"reserve"`
	FreeAt      *float64          `json:"freeAt,omitempty"` // soc from which the battery is free again, with a hysteresis (byd) only
	Limit       float64           `json:"limit"`            // peak limit in W
	Grid        float64           `json:"grid"`             // grid power in W
	House       float64           `json:"house"`            // grid power plus battery power in W
	Battery     float64           `json:"battery"`          // battery power in W, + discharging
	Setpoint    float64           `json:"setpoint"`         // discharge power evcc passes on in W, only with limited
	Limited     bool              `json:"limited"`          // the discharge is limited to the setpoint
	ChargePower float64           `json:"chargePower,omitempty"`
	BatteryMode string            `json:"batteryMode"`
	StorageOk   bool              `json:"storageOk"`
	StorageHint string            `json:"storageHint,omitempty"`
	Window      *peakStatusWindow `json:"window,omitempty"`
	Month       *peakStatusMonth  `json:"month,omitempty"`
}

// peakStatusMemo is what the transitions of the status are counted against
type peakStatusMemo struct {
	inited     bool
	enabled    bool
	below      bool // the reserve state, valid while enabled
	belowKnown bool
	mode       api.BatteryMode
	metersLost bool
	storageOk  bool

	covering bool // a peak is being covered, for peakEnd
	epStart  time.Time
	epMax    float64

	bydStatus     string    // the status of the integration, read in this cycle, empty = unknown
	mismatchSince time.Time // its status differs from the battery mode since

	last      peakStatus // the last published
	published bool
}

// peakStorageWrites are the outputs written in every cycle while peak shaving
// runs (see applyOmniProtection) and the connection to Home Assistant: if one of
// them failed the last time, the storage is not reachable. Others, as the grid
// charge power, are only written now and then and keep their failure for long.
func peakStorageWrites(batteryType string) []string {
	name := batteryTypeBYD
	if batteryType == batteryTypeMarstek {
		name = batteryTypeMarstek
	}

	return []string{
		name + " connect", "peak shaving soc threshold", "peak shaving limit read", "peak shaving limit",
		name + " peak shaving switch read", name + " peak shaving switch",
	}
}

// bydStatusEntity returns the status sensor of the integration, derived from its
// peak shaving switch: switch.<x>_peak_shaving has sensor.<x>_status. Empty if
// the switch is named otherwise: the extra hints of the battery type BYD are then
// left out.
func bydStatusEntity(sw string) string {
	x, ok := strings.CutPrefix(sw, "switch.")
	if !ok {
		return ""
	}

	x, ok = strings.CutSuffix(x, "_peak_shaving")
	if !ok || x == "" {
		return ""
	}

	return "sensor." + x + "_status"
}

// readBydStatus reads the status sensor of the integration once per cycle, with
// the protection against a hung Home Assistant of the shared connection. A sensor
// that is missing or has no value counts as unknown.
func (site *Site) readBydStatus() string {
	if site.omniType() {
		return ""
	}

	s := site.peak()

	s.mu.Lock()
	entity := bydStatusEntity(s.protSwitch)
	s.mu.Unlock()

	if entity == "" {
		return ""
	}

	conn, err := site.haConnection()
	if err != nil {
		return ""
	}

	state, _, err := omniRead(conn, entity)
	if err != nil {
		if !errors.Is(err, errHAHung) {
			site.log.TRACE.Printf("peak status: %s: %v", entity, err)
		}

		return ""
	}

	return state
}

// peakStatusNow derives the status from the state of the last cycle
func (site *Site) peakStatusNow() peakStatus {
	return site.peakStatusWith(site.GetBatteryMode())
}

// peakStatusWith is peakStatusNow with the battery mode read by the caller
func (site *Site) peakStatusWith(mode api.BatteryMode) peakStatus {
	// before s.mu: the site lock is taken
	soc, socKnown := site.batterySocRead()
	omni := site.omniType()
	configured := site.batteryConfigured()
	hyst := site.peakHysteresis()

	site.RLock()
	grid, battery := site.gridPower, site.battery.Power
	site.RUnlock()

	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.entitiesSet() || !configured {
		return peakStatus{}
	}

	st := peakStatus{
		State:       peakStateFree,
		Type:        batteryTypeBYD,
		Soc:         soc,
		Reserve:     s.effectiveReserve(),
		Limit:       s.limit,
		Grid:        grid,
		House:       s.demand,
		Battery:     battery,
		Setpoint:    s.setpoint,
		BatteryMode: mode.String(),
		StorageOk:   true,
	}

	if omni {
		st.Type = batteryTypeMarstek
	}

	if hyst > 0 {
		freeAt := st.Reserve + hyst
		st.FreeAt = &freeAt
	}

	if w := s.window; !w.Start.IsZero() {
		st.Window = &peakStatusWindow{Avg: w.Avg, Allowed: w.Allowed, Start: w.Start, End: w.End()}
	}

	month := s.clock.Now().Local().Format("2006-01")
	if i := slices.IndexFunc(s.months, func(m peakMonth) bool { return m.Month == month }); i >= 0 {
		m := s.months[i]
		st.Month = &peakStatusMonth{Peak: m.Peak, PeakAt: m.PeakAt, Interventions: m.Interventions}
	}

	// the storage: the writes to its entities work and it delivers values
	for _, name := range peakStorageWrites(s.batteryType) {
		if _, failed := s.writeErr[name]; failed {
			st.StorageOk = false
		}
	}

	if !socKnown {
		st.StorageOk = false
	}

	for _, failed := range s.read.batteryFailed {
		if failed {
			st.StorageOk = false
		}
	}

	if !omni {
		switch by := s.status.bydStatus; {
		case by == bydStatusNoEvcc:
			st.StorageOk = false
		case by == bydStatusNoSoc:
			st.StorageHint = peakHintNoSoc
		case !s.status.mismatchSince.IsZero() && s.clock.Now().Sub(s.status.mismatchSince) > peakModeStuckAfter:
			st.StorageHint = peakHintModeStuck
		}
	}

	if mode == api.BatteryCharge {
		st.ChargePower = s.chargeSetpoint
		if st.ChargePower <= 0 && battery < 0 {
			st.ChargePower = -battery
		}
	}

	held := mode == api.BatteryHold || mode == api.BatteryHoldCharge
	st.Limited = s.enabled && (s.shaving || held)

	switch {
	case !s.enabled:
		st.State = peakStateOff
	case !st.StorageOk:
		st.State = peakStateStorage
	case s.metersLost:
		st.State = peakStateMeter
	case mode == api.BatteryCharge:
		st.State = peakStateGridCharge
	case held && s.covering:
		st.State = peakStateHoldShaving
	case held:
		st.State = peakStateHold
	case s.shaving && s.covering:
		st.State = peakStateShaving
	case s.shaving:
		st.State = peakStateSaving
	}

	return st
}

// publishPeakStatus publishes the status when it changed and logs its changes as
// events. Called at the end of the fork's part of the cycle, after the outputs
// were written (their failures are part of the status), and when peak shaving is
// switched.
func (site *Site) publishPeakStatus() {
	by := site.readBydStatus()

	s := site.peak()
	mode := site.GetBatteryMode()

	s.mu.Lock()
	s.status.bydStatus = by
	site.trackModeMismatch(by, mode)
	s.mu.Unlock()

	st := site.peakStatusWith(mode)

	s.mu.Lock()
	site.peakStatusTransitions(st, mode)
	changed := !s.status.published || !reflect.DeepEqual(s.status.last, st)
	s.status.last, s.status.published = st, true
	s.mu.Unlock()

	if changed {
		site.publish(keys.PeakStatus, st)
	}
}

// trackModeMismatch notes since when the status of the integration differs from
// the battery mode evcc set. Called with s.mu held.
func (site *Site) trackModeMismatch(by string, mode api.BatteryMode) {
	s := site.peak()

	var mismatch bool

	switch {
	case by == "" || site.omniTypeLocked():
	case mode == api.BatteryHold || mode == api.BatteryHoldCharge:
		// held, or shaving: the integration covers a peak while it holds
		mismatch = by != bydStatusHold && by != bydStatusShaving
	case mode == api.BatteryCharge:
		mismatch = by != bydStatusGridCharge
	}

	switch {
	case !mismatch:
		s.status.mismatchSince = time.Time{}
	case s.status.mismatchSince.IsZero():
		s.status.mismatchSince = s.clock.Now()
	}
}

// omniTypeLocked is omniType for a caller holding s.mu
func (site *Site) omniTypeLocked() bool {
	return site.peak().batteryType == batteryTypeMarstek
}

// peakStatusTransitions logs the changes against the last status as events. The
// first status after the start only sets the reference. Called with s.mu held.
func (site *Site) peakStatusTransitions(st peakStatus, mode api.BatteryMode) {
	s := site.peak()
	m := &s.status
	now := s.clock.Now()

	if st.State == "" {
		m.inited = false
		return
	}

	enabled := s.enabled
	storageOk := st.StorageOk
	below := s.shaving

	add := func(typ, load string, a, b float64) {
		site.lmm().AddEvent(lm.Event{At: now, Type: typ, Load: load, A: a, B: b})
	}

	if m.inited {
		if enabled != m.enabled {
			add(lm.EventPeakSwitch, "", boolFloat(enabled), 0)
		}

		// evcc sets the first mode after the start: that is no change
		if mode != m.mode && m.mode != api.BatteryUnknown && mode != api.BatteryUnknown {
			add(lm.EventBatteryMode, mode.String(), 0, 0)
		}

		if enabled && m.enabled {
			if s.metersLost != m.metersLost {
				add(lm.EventMeter, "", boolFloat(s.metersLost), 0)
			}

			if storageOk != m.storageOk {
				add(lm.EventStorage, "", boolFloat(!storageOk), 0)
			}

			if m.belowKnown && below != m.below {
				if below {
					add(lm.EventReserveBelow, "", st.Soc, st.Reserve)
				} else {
					add(lm.EventReserveAbove, "", st.Soc, st.Reserve)
				}
			}
		}
	}

	// the end of a peak with the most the battery gave
	covering := enabled && s.covering
	switch {
	case covering && !m.covering:
		m.epStart, m.epMax = now, 0
	case !covering && m.covering && m.inited:
		add(lm.EventPeakEnd, "", m.epMax, now.Sub(m.epStart).Seconds())
	}

	if covering {
		m.epMax = max(m.epMax, st.Battery)
	}

	m.inited = true
	m.enabled, m.mode, m.metersLost, m.storageOk, m.covering = enabled, mode, s.metersLost, storageOk, covering

	// the reserve state counts while peak shaving runs only: switching it off
	// resets it and must not look like a rise above the reserve
	m.below, m.belowKnown = below, enabled
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}

	return 0
}
