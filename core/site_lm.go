package core

// Custom extension, kept in its own file to keep the merge surface with upstream
// evcc small. It adds three things:
//
//  1. the home battery participates in load management: its grid charging power
//     counts against circuit limits and is shed when the budget runs out
//  2. soc-based grid charging: a switch plus a start and a stop soc, independent
//     of the price-based grid charge limit
//  3. priority-based shedding across all circuit loads, with the priorities set
//     in the ui, see package core/lm
//
// Upstream touch points are core/site.go (config and state field, restore call,
// batteryGridChargeRequested), core/site_circuits.go (circuitLoads) and
// core/loadpoint.go (the circuit checks in setLimit and the two probes).

import (
	"fmt"
	"maps"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/hems/hems"
	"github.com/evcc-io/evcc/util/config"
)

const (
	defaultSocChargeStart = 20.0
	defaultSocChargeStop  = 80.0
)

// lmState is the runtime state of the load management extensions
type lmState struct {
	once     sync.Once   // defaults
	m        *lm.Manager // the load management of this site
	warnOnce sync.Once   // unknown battery charge power warning
	mu       sync.Mutex

	socChargeEnabled bool    // soc-based grid charging switch
	socChargeStart   float64 // start grid charging at or below this soc
	socChargeStop    float64 // stop grid charging at or above this soc
	socChargeRunning bool    // hysteresis state between start and stop

	prios map[string]int // shed priorities set in the ui, by load name

	// load management switched off, see site_lm_switch.go
	off          bool
	offLimits    map[string]float64 // lifted power limits by circuit name
	offDynamic   map[string]bool    // circuits with a plugin limit that stays
	offPublished bool
	circuit      string // the load management (peak) circuit, empty = all

	guardMinutes int             // shed guard, see site_lm_guard.go
	guarded      map[string]bool // loadpoints the shed guard protects, by name

	// advanced settings, see site_lm_advanced.go. Own lock: they are read while
	// the peak shaving state is locked.
	advMu sync.Mutex
	adv   lmAdvanced

	gridCharge  bool           // battery grid charging requested in the last cycle
	gridHeld    bool           // ... asked for, but held back for loadpoints short of power
	gridDenied  bool           // held by load management or evcc's circuit check, for the event log
	gridOnce    gridChargeOnce // one-time grid charging, see site_lm_once.go
	eeg         feedInEegState // second feed-in tariff, see site_feedin_eeg.go
	batteryLoad *batteryLoad

	// the optimizer's soc floor was raised above the battery's own minimum,
	// see site_optimizer_lm.go
	floorRaised bool

	// the home battery's inputs for the further optimizer passes, see
	// site_optimizer_reserve_pass.go
	plan *lmPlan

	// a forced optimizer run arrived while one was running
	optimizeAgain atomic.Bool

	// battery identification, see site_battery_ident.go
	identUse bool
	ident    []batteryIdentResult
	identAt  time.Time
}

// siteCustom is the state of this fork's features, one field in upstream's Site
type siteCustom struct {
	lm   lmState
	peak peakState
}

// restoreCustom restores the fork's settings, called at the end of upstream's
// restoreSettings
func (site *Site) restoreCustom() {
	site.restoreLmSettings()
	site.restorePeakSettings()
	site.restoreFeedInEeg()
}

// updateCustom runs the fork's per cycle work once the meters are read, before
// upstream decides on battery grid charging and the battery mode: peak shaving,
// the export under the second feed-in tariff and the battery identification
func (site *Site) updateCustom(state siteState) {
	site.updatePeakShaving(state)
	site.updateFeedInEeg()
	site.updateBatteryIdent()
}

// lms returns the load management state, applying defaults on first use
func (site *Site) lms() *lmState {
	site.custom.lm.once.Do(func() {
		site.custom.lm.m = lm.New()
		site.custom.lm.socChargeStart = defaultSocChargeStart
		site.custom.lm.socChargeStop = defaultSocChargeStop
	})
	return &site.custom.lm
}

// lmm returns the load management of this site
func (site *Site) lmm() *lm.Manager {
	return site.lms().m
}

// restoreLmSettings restores the persisted load management settings
func (site *Site) restoreLmSettings() {
	s := site.lms()

	if v, err := settings.Float(keys.BatterySocGridChargeStart); err == nil {
		s.mu.Lock()
		s.socChargeStart = v
		s.mu.Unlock()
	}
	if v, err := settings.Float(keys.BatterySocGridChargeStop); err == nil {
		s.mu.Lock()
		s.socChargeStop = v
		s.mu.Unlock()
	}
	if v, err := settings.Bool(keys.BatterySocGridCharge); err == nil {
		s.mu.Lock()
		s.socChargeEnabled = v
		s.mu.Unlock()
	}
	if v, err := settings.Bool(keys.BatterySocGridChargeRunning); err == nil {
		s.mu.Lock()
		s.socChargeRunning = v
		s.mu.Unlock()
	}

	var prios map[string]int
	if err := settings.Json(keys.LmPriorities, &prios); err == nil {
		s.mu.Lock()
		s.prios = prios
		s.mu.Unlock()
	}

	site.lmm().SetPriorityLookup(site.lmPriorityLookup)
	site.restoreGridChargeOnce()
	site.unifyLmPriorities()

	site.restoreLmGuard()
	site.restoreLmAdvanced()
	site.restoreLmSwitch()
	site.restoreBatteryIdent()
	site.publishLmProfiles()

	site.publishLmSettings()

	// the priorities are published by restorePeakSettings, which runs next: the
	// list includes the battery once it is on a circuit, and that assignment is a
	// peak shaving setting
}

// publishLmSettings publishes the soc grid charge settings to the ui
func (site *Site) publishLmSettings() {
	s := site.lms()

	s.mu.Lock()
	enabled, start, stop := s.socChargeEnabled, s.socChargeStart, s.socChargeStop
	s.mu.Unlock()

	site.publish(keys.BatterySocGridCharge, enabled)
	site.publish(keys.BatterySocGridChargeStart, start)
	site.publish(keys.BatterySocGridChargeStop, stop)
}

//
// battery as a load management participant
//

// batteryLoad exposes the home battery as a circuit load so that its grid
// charging power counts against the circuit budget and can be shed
type batteryLoad struct {
	site *Site
}

var (
	_ api.CircuitLoad = (*batteryLoad)(nil)
	_ lm.Load         = (*batteryLoad)(nil)
)

func (b *batteryLoad) GetTitle() string {
	return "battery"
}

// LmPriority is the battery's priority without one set in the ui: shed first,
// the value set in the ui comes through lmPriorityLookup
func (b *batteryLoad) LmPriority() int {
	return 0
}

func (b *batteryLoad) GetCircuit() api.Circuit {
	return b.site.lmBatteryCircuit()
}

// GetChargePower returns the battery's charging power. evcc's sign convention is
// positive for discharging, so only the charging direction is a load here.
// Discharging feeds the circuit and is deliberately not counted as relief.
func (b *batteryLoad) GetChargePower() float64 {
	return max(0, -b.site.state().battery.Power)
}

func (b *batteryLoad) GetMaxPhaseCurrent() float64 {
	return powerToCurrent(b.GetChargePower(), b.site.lmBatteryPhases())
}

// lmBattery returns the battery's load management participant
func (site *Site) lmBattery() *batteryLoad {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.batteryLoad == nil {
		s.batteryLoad = &batteryLoad{site: site}
	}

	return s.batteryLoad
}

// lmBatteryCircuit returns the circuit the home battery draws from: the site's
// root circuit, as in evcc, nil without circuits
func (site *Site) lmBatteryCircuit() api.Circuit {
	return site.circuit
}

// lmHoldOff is how long battery grid charging stays off after a demand peak
func (site *Site) lmHoldOff() time.Duration {
	if v := site.advanced().HoldOff; v != nil {
		return time.Duration(*v) * time.Minute
	}
	return lm.DefaultHoldOff
}

// lmBatteryPhases returns the phase count used for the battery's current accounting
func (site *Site) lmBatteryPhases() int {
	if v := site.advanced().Phases; v != nil {
		return int(*v)
	}
	return lm.DefaultPhases
}

// charge power sources, reported to the ui so the assumed value is not invisible
const (
	chargePowerSourceSetting = "setting" // entered in the ui
	chargePowerSourceMeter   = "meter"   // the battery meters' maxchargepower
	chargePowerSourceUnknown = "unknown" // nothing to go by
)

// lmBatteryChargePower returns the power the battery is expected to draw while
// grid charging, and where that number came from.
//
// It has to be an assumption: a battery driven by mode scripts is switched on or
// off, so the draw can only be measured once charging already runs - while the
// question "would charging create a peak" has to be answered before it starts.
//
// Note that a meter only reports its limits when both maxchargepower and
// maxdischargepower are set; with just one of them the capability is absent and
// this falls through to unknown.
func (site *Site) lmBatteryChargePower() (float64, string) {
	if p := site.GetPeakShavingChargePower(); p > 0 {
		return p, chargePowerSourceSetting
	}

	var res float64
	for _, dev := range site.batteryMeters {
		if m, ok := api.Cap[api.BatteryPowerLimiter](dev.Instance()); ok {
			charge, _ := m.GetPowerLimits()
			res += charge
		}
	}

	if res > 0 {
		return res, chargePowerSourceMeter
	}

	return 0, chargePowerSourceUnknown
}

// circuitLoads returns the circuit participants: the loadpoints plus the home
// battery when it takes part in load management
func (site *Site) circuitLoads() []api.CircuitLoad {
	res := site.loadpointsAsCircuitDevices()

	if site.lmBatteryCircuit() != nil {
		res = append(res, site.lmBattery())
	}

	return res
}

// lmGridChargePower is the fork's input into evcc's grid charge check on the
// circuit, see batteryMaxChargePower: the power the battery is going to draw.
// A battery with a charge power entity gets this cycle's setpoint, which already
// fits below the peak limit and the circuit; otherwise it is the power entered in
// the ui. Without either evcc goes by the battery meters' limits.
func (site *Site) lmGridChargePower() (float64, bool) {
	if site.chargePowerControlled() {
		p := site.peak()

		p.mu.Lock()
		power := p.chargeSetpoint
		p.mu.Unlock()

		return power, power > 0
	}

	if power := site.GetPeakShavingChargePower(); power > 0 {
		return power, true
	}

	return 0, false
}

// lmGridHeld reports whether load management held back a switched battery's
// grid charging in the last cycle, for loadpoints short of power
func (site *Site) lmGridHeld() bool {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.gridHeld
}

// lmGridChargeDenied reports whether a grid charge is held: by load management
// for loadpoints short of power, or by evcc's circuit check, i.e. the battery is
// in hold mode with a circuit in place, and neither the hems nor the api asked
// for it
func (site *Site) lmGridChargeDenied(requested bool) bool {
	if site.lmGridHeld() {
		return true
	}

	if !requested || site.circuit == nil || site.GetBatteryMode() != api.BatteryHold || site.GetBatteryModeExternal() != api.BatteryUnknown {
		return false
	}

	dimmed := hems.Dimmed(site.hems)
	return dimmed == nil || !*dimmed
}

//
// soc-based grid charging
//

// batteryGridChargeRequested reports whether the battery should be grid-charged.
// It combines the upstream price-based limit with the soc-based switch. Both give
// way to a running demand peak. Whether the power fits the circuit is evcc's
// check, fed with lmGridChargePower.
func (site *Site) batteryGridChargeRequested(rate api.Rate) bool {
	res, held := site.lmGridChargeDecision(rate)

	s := site.lms()
	s.mu.Lock()
	s.gridCharge = res
	s.gridHeld = held
	s.mu.Unlock()

	return res
}

// lmGridChargeDecision is the decision behind batteryGridChargeRequested, and
// whether load management held back a switched battery
func (site *Site) lmGridChargeDecision(rate api.Rate) (bool, bool) {
	// evaluated unconditionally so the hysteresis keeps tracking the soc
	socActive := site.batterySocChargeActive()
	onceActive := site.batteryGridChargeOnceActive()

	// a running demand peak needs the battery for shaving, not charging
	if !socActive && !onceActive && !site.batteryGridChargeActive(rate) || site.peakPausesGridCharge() {
		// release what the battery had reserved on the circuit, lower priority
		// loads would otherwise stay throttled until the reservation expires
		site.lmm().Forget(site.lmBattery())
		site.writeChargeValue(0)
		site.recordBatteryLimit(0, 0)
		return false, false
	}

	want, _ := site.lmBatteryChargePower()

	if !site.chargePowerControlled() {
		// switched on or off: evcc checks that the power fits the circuit. Load
		// management keeps it below the loadpoints: it does not start into power
		// they are short of, and a running one gives way to them, which evcc's
		// check does not see as long as they are capped at the limit. Running, it
		// asks for what it draws, not for more.
		allowed, held := want, false
		if c := site.lmBatteryCircuit(); c != nil {
			bat := site.lmBattery()
			draw := bat.GetChargePower()

			running := site.GetBatteryMode() == api.BatteryCharge

			req := want
			if running {
				req = draw
			}

			if req > 0 {
				if got := site.lmm().ValidatePower(bat, c, draw, req); got < req {
					site.log.DEBUG.Printf("battery grid charge: load management allows %.0fW of %.0fW, loadpoints first", got, req)
					allowed, held = got, true

					// as evcc's check does when it stops charging: restarting needs
					// room for what the battery drew
					if running {
						site.batteryChargeStopPower = draw
					}
				}
			}
		}
		site.recordBatteryLimit(want, allowed)

		return !held, held
	}

	power := site.batteryChargeSetpoint()
	site.writeChargeValue(power)
	site.recordBatteryLimit(want, power)

	return power > 0, false
}

// recordBatteryLimit keeps what the battery may grid-charge with, which load
// management compares with what it draws, see core/lm/follow.go
func (site *Site) recordBatteryLimit(requested, allowed float64) {
	site.lmm().Record(site.lmBattery(), requested, allowed, allowed > 0, time.Now())
}

// minGridChargePower is the smallest grid charge setpoint worth switching the
// battery into charge mode for
const minGridChargePower = 500.0

// batteryChargeSetpoint returns the grid charge power for a battery whose charge
// power is set through an entity: the expected charge power, trimmed to what fits
// below the peak limit and within the circuit. Zero when less than the minimum
// is left.
func (site *Site) batteryChargeSetpoint() float64 {
	power, _ := site.lmBatteryChargePower()
	if power <= 0 {
		site.lms().warnOnce.Do(func() {
			site.log.WARN.Println("load management: battery grid charge power unknown, setpoint 0 - set it under peak load management or configure the battery's maxchargepower")
		})
		return 0
	}

	if headroom, ok := site.peakChargeHeadroom(); ok {
		power = min(power, headroom)
	}

	// records what the circuit denies, so loads below the battery give way
	if c := site.lmBatteryCircuit(); c != nil {
		bat := site.lmBattery()
		power = min(power, site.lmm().ValidatePower(bat, c, bat.GetChargePower(), power))
	}

	power = math.Floor(power)
	if power < minGridChargePower {
		return 0
	}

	return power
}

// batterySocChargeActive implements soc-based grid charging: charging starts at
// or below the start soc and continues until the stop soc is reached
func (site *Site) batterySocChargeActive() bool {
	if !site.batteryConfigured() {
		return false
	}

	// read before locking, GetBatterySoc takes the site lock
	soc := site.GetBatterySoc()

	s := site.lms()

	s.mu.Lock()
	running := s.socChargeRunning

	switch {
	case !s.socChargeEnabled:
		running = false

	case s.socChargeStop > 0 && soc >= s.socChargeStop:
		if running {
			site.log.DEBUG.Printf("battery soc grid charge: stop soc reached (%.0f%% >= %.0f%%)", soc, s.socChargeStop)
		}
		running = false

	case soc <= s.socChargeStart:
		if !running {
			site.log.DEBUG.Printf("battery soc grid charge: start soc reached (%.0f%% <= %.0f%%)", soc, s.socChargeStart)
		}
		running = true
	}
	s.mu.Unlock()

	site.setSocChargeRunning(running)

	return running
}

// setSocChargeRunning updates the hysteresis state and persists it, so that a
// restart halfway between start and stop soc carries on charging
func (site *Site) setSocChargeRunning(running bool) {
	s := site.lms()

	s.mu.Lock()
	changed := s.socChargeRunning != running
	s.socChargeRunning = running
	s.mu.Unlock()

	if changed {
		settings.SetBool(keys.BatterySocGridChargeRunning, running)
		site.Optimize() // the plan charges from now on, or no longer, see core/site_optimizer_lm.go
	}
}

//
// api
//

// GetBatterySocGridCharge returns the soc-based grid charging switch
func (site *Site) GetBatterySocGridCharge() bool {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.socChargeEnabled
}

// SetBatterySocGridCharge sets the soc-based grid charging switch
func (site *Site) SetBatterySocGridCharge(val bool) error {
	if !site.hasBatteryControl() {
		return ErrBatteryControlNotAvailable
	}

	site.log.DEBUG.Println("set battery soc grid charge:", val)

	s := site.lms()

	s.mu.Lock()
	changed := s.socChargeEnabled != val
	s.socChargeEnabled = val
	s.mu.Unlock()

	if !val {
		site.setSocChargeRunning(false)
	}

	if changed {
		settings.SetBool(keys.BatterySocGridCharge, val)
		site.publish(keys.BatterySocGridCharge, val)
		site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go
	}

	return nil
}

// GetBatterySocGridChargeStart returns the soc at or below which grid charging starts
func (site *Site) GetBatterySocGridChargeStart() float64 {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.socChargeStart
}

// SetBatterySocGridChargeStart sets the soc at or below which grid charging starts
func (site *Site) SetBatterySocGridChargeStart(soc float64) error {
	if !site.hasBatteryControl() {
		return ErrBatteryControlNotAvailable
	}

	if soc < 0 || soc > 100 {
		return fmt.Errorf("invalid soc: %.0f", soc)
	}

	s := site.lms()

	s.mu.Lock()
	if soc >= s.socChargeStop {
		stop := s.socChargeStop
		s.mu.Unlock()
		return fmt.Errorf("start soc %.0f must be below stop soc %.0f", soc, stop)
	}
	changed := s.socChargeStart != soc
	s.socChargeStart = soc
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set battery soc grid charge start:", soc)
		settings.SetFloat(keys.BatterySocGridChargeStart, soc)
		site.publish(keys.BatterySocGridChargeStart, soc)
		site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go
	}

	return nil
}

// GetBatterySocGridChargeStop returns the soc at or above which grid charging stops
func (site *Site) GetBatterySocGridChargeStop() float64 {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.socChargeStop
}

// SetBatterySocGridChargeStop sets the soc at or above which grid charging stops
func (site *Site) SetBatterySocGridChargeStop(soc float64) error {
	if !site.hasBatteryControl() {
		return ErrBatteryControlNotAvailable
	}

	if soc <= 0 || soc > 100 {
		return fmt.Errorf("invalid soc: %.0f", soc)
	}

	s := site.lms()

	s.mu.Lock()
	if soc <= s.socChargeStart {
		start := s.socChargeStart
		s.mu.Unlock()
		return fmt.Errorf("stop soc %.0f must be above start soc %.0f", soc, start)
	}
	changed := s.socChargeStop != soc
	s.socChargeStop = soc
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set battery soc grid charge stop:", soc)
		settings.SetFloat(keys.BatterySocGridChargeStop, soc)
		site.publish(keys.BatterySocGridChargeStop, soc)
		site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go
	}

	return nil
}

//
// shed priorities
//

const (
	lmBatteryName = "battery" // name the battery's priority is stored under
	lmMaxPriority = 10
)

// lmPriority is a load's shed priority as published to the ui
type lmPriority struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	Battery  bool   `json:"battery,omitempty"`
}

// lmLoadName returns the name a load's priority is stored under: the loadpoint's
// config name, e.g. db:3, or lmBatteryName. Empty for an unknown load.
func (site *Site) lmLoadName(l lm.Load) string {
	switch l := l.(type) {
	case *batteryLoad:
		return lmBatteryName

	case *Loadpoint:
		for _, dev := range config.Loadpoints().Devices() {
			if dev.Instance() == loadpoint.API(l) {
				return dev.Config().Name
			}
		}
	}

	return ""
}

// lmPriorityLookup returns the battery's priority set in the ui. Loadpoints use
// their upstream priority, see site_lm_priority.go; a battery without a ui value
// has the lowest, 0.
func (site *Site) lmPriorityLookup(l lm.Load) (int, bool) {
	name := site.lmLoadName(l)
	if name != lmBatteryName {
		return 0, false
	}

	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	prio, ok := s.prios[name]
	return prio, ok
}

// lmPriorities returns the loads that take part in load management, i.e. that
// are on a circuit, with their effective priority
func (site *Site) lmPriorities() []lmPriority {
	res := make([]lmPriority, 0)

	if site.lmBatteryCircuit() != nil {
		res = append(res, lmPriority{
			Name:     lmBatteryName,
			Priority: site.lmm().Priority(site.lmBattery()),
			Battery:  true,
		})
	}

	for _, dev := range config.Loadpoints().Devices() {
		lp, ok := dev.Instance().(*Loadpoint)
		if !ok || lp.GetCircuit() == nil {
			continue
		}

		res = append(res, lmPriority{
			Name:     dev.Config().Name,
			Title:    lp.GetTitle(),
			Priority: site.lmm().Priority(lp),
		})
	}

	return res
}

func (site *Site) publishLmPriorities() {
	site.publish(keys.LmPriorities, site.lmPriorities())
}

// SetLmPriority sets a load's priority, lower is shed first. For a loadpoint
// that is its upstream priority, which also ranks pv surplus and plans.
func (site *Site) SetLmPriority(name string, prio int) error {
	if prio < 0 || prio > lmMaxPriority {
		return fmt.Errorf("priority must be between 0 and %d", lmMaxPriority)
	}

	if name != lmBatteryName {
		dev, err := config.Loadpoints().ByName(name)
		if err != nil {
			return fmt.Errorf("unknown loadpoint: %s", name)
		}
		lp, ok := dev.Instance().(*Loadpoint)
		if !ok {
			return fmt.Errorf("unknown loadpoint: %s", name)
		}

		lp.SetPriority(prio)
		site.publishLmPriorities()

		return nil
	}

	s := site.lms()

	s.mu.Lock()
	if s.prios == nil {
		s.prios = make(map[string]int)
	}
	s.prios[name] = prio
	prios := maps.Clone(s.prios)
	s.mu.Unlock()

	site.log.DEBUG.Printf("set load management priority: %s = %d", name, prio)

	if err := settings.SetJson(keys.LmPriorities, prios); err != nil {
		return err
	}

	site.publishLmPriorities()

	return nil
}
