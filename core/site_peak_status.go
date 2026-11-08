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
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/core/peak"
)

// Peak states, checked in this order
const (
	peakStateOff           = "off"           // peak shaving is off
	peakStateStorage       = "storage"       // the storage is not reachable
	peakStateMeter         = "meter"         // the grid meter failed for longer than peak.MaxGap
	peakStateGridCharge    = "gridCharge"    // evcc charges the battery from the grid
	peakStateGridDischarge = "gridDischarge" // evcc discharges the battery into the grid
	peakStateHoldShaving   = "holdShaving"   // held, and a peak is being covered
	peakStateHold          = "hold"          // held
	peakStateShaving       = "shaving"       // below the reserve, covering a peak
	peakStateSaving        = "saving"        // below the reserve, nothing to cover
	peakStateFree          = "free"          // above the reserve
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
	Held        bool              `json:"held"`             // evcc holds the battery
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

	covering bool // a peak is being covered, for peakEnd
	epStart  time.Time
	epMax    float64

	bydStatus     string    // the status of the integration, read in this cycle, empty = unknown
	mismatchSince time.Time // its status differs from the battery mode since

	last      peakStatus // the last published
	published bool

	// publishing is serialised, and each publication derives the status anew under
	// the lock: the api path and the cycle must not put an older status over a newer
	pub sync.Mutex

	// changes of the mode, the meter and the storage are logged when they stayed for
	// peakEventDebounce: a change that returns within it is no event
	modeChange, meterChange, storageChange peakDebounce
}

// peakEventDebounce is how long a change of the battery mode, the meter or the
// storage has to stay before it is logged
const peakEventDebounce = time.Minute

// peakDebounce holds back the event of a change until it lasted
type peakDebounce struct {
	set     bool
	value   string // what was logged last
	pending string // the changed value, empty = none
	since   time.Time
}

// reset forgets the reference: the next value is taken as it is
func (d *peakDebounce) reset() {
	*d = peakDebounce{}
}

// step takes the current value and returns the change to log, if there is one
// that lasted: the value it was before and when it changed
func (d *peakDebounce) step(cur string, now time.Time) (change bool, from string, at time.Time) {
	switch {
	case !d.set:
		d.set, d.value = true, cur
	case cur == d.value:
		d.pending = "" // back within the time: no pair
	case d.pending != cur:
		d.pending, d.since = cur, now
	case now.Sub(d.since) >= peakEventDebounce:
		from, at = d.value, d.since
		d.value, d.pending = cur, ""
		return true, from, at
	}

	return false, "", time.Time{}
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
	on := s.enabled
	s.mu.Unlock()

	// peak shaving off: the integration is not asked
	if entity == "" || !on {
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
	optimizerHolds := site.optimizerHolds()

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

	// the running window only, as the control takes it: after a meter outage the old
	// one would show its numbers as if they were current
	if w := s.window; w.Start.Equal(s.clock.Now().Truncate(peak.Window)) && !s.metersLost {
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
	st.Held = held

	// the discharge is limited below the reserve and while the optimizer's hold gives
	// way to a peak; a hold for another reason (vehicle, api) limits nothing
	st.Limited = s.enabled && (s.shaving || (held && optimizerHolds))

	switch {
	case !s.enabled:
		st.State = peakStateOff
	case !st.StorageOk:
		st.State = peakStateStorage
	case s.metersLost:
		st.State = peakStateMeter
	case mode == api.BatteryCharge:
		st.State = peakStateGridCharge
	case mode == api.BatteryDischarge:
		st.State = peakStateGridDischarge
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

// publishPeakStatus reads the status sensor of the integration and publishes the
// status when it changed, logging its changes as events. Called at the end of
// the fork's part of the cycle, after the outputs were written (their failures
// are part of the status).
func (site *Site) publishPeakStatus() {
	by := site.readBydStatus()

	s := site.peak()

	s.mu.Lock()
	s.status.bydStatus = by
	s.mu.Unlock()

	site.publishPeakStatusCached()
}

// publishPeakStatusCached is publishPeakStatus with the status of the integration
// of the last cycle: for the api path, which asks Home Assistant nothing
func (site *Site) publishPeakStatusCached() {
	s := site.peak()

	// everything from here derives from the state as it is, so whoever publishes
	// last publishes the newest
	s.status.pub.Lock()
	defer s.status.pub.Unlock()

	mode := site.GetBatteryMode()

	s.mu.Lock()
	site.trackModeMismatch(s.status.bydStatus, mode)
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
		m.modeChange.reset()
		m.meterChange.reset()
		m.storageChange.reset()
		return
	}

	enabled := s.enabled
	storageOk := st.StorageOk
	below := s.shaving

	add2 := func(typ, load string, a, b float64, at time.Time) {
		site.lmm().AddEvent(lm.Event{At: at, Type: typ, Load: load, A: a, B: b})
	}
	add := func(typ, load string, a, b float64) { add2(typ, load, a, b, now) }

	if m.inited && enabled != m.enabled {
		add(lm.EventPeakSwitch, "", boolFloat(enabled), 0)
	}

	// the mode: evcc sets the first one after the start, that is no change
	if mode != api.BatteryUnknown {
		if change, _, at := m.modeChange.step(mode.String(), now); change {
			add2(lm.EventBatteryMode, mode.String(), 0, 0, at)
		}
	}

	if !enabled {
		// the state of the meter and the storage counts while peak shaving runs
		m.meterChange.reset()
		m.storageChange.reset()
	} else {
		if change, _, at := m.meterChange.step(boolString(s.metersLost), now); change {
			add2(lm.EventMeter, "", boolFloat(s.metersLost), 0, at)
		}

		if change, _, at := m.storageChange.step(boolString(!storageOk), now); change {
			add2(lm.EventStorage, "", boolFloat(!storageOk), 0, at)
		}
	}

	if m.inited && enabled && m.enabled && m.belowKnown && below != m.below {
		if below {
			add(lm.EventReserveBelow, "", st.Soc, st.Reserve)
		} else {
			add(lm.EventReserveAbove, "", st.Soc, st.Reserve)
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
	m.enabled, m.covering = enabled, covering

	// the reserve state counts while peak shaving runs only: switching it off
	// resets it and must not look like a rise above the reserve
	m.below, m.belowKnown = below, enabled
}

func boolString(b bool) string {
	if b {
		return "1"
	}

	return "0"
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}

	return 0
}
