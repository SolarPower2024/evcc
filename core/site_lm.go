package core

// Custom extension, kept in its own file to keep the merge surface with upstream
// evcc small. It adds three things:
//
//  1. the home battery participates in load management: its grid charging power
//     counts against circuit limits and is shed when the budget runs out
//  2. soc-based grid charging: a switch plus a start and a stop soc, independent
//     of the price-based grid charge limit
//  3. priority-based shedding across all circuit loads, see package core/lm
//
// Upstream touch points are core/site.go (config and state field, restore call,
// batteryGridChargeRequested), core/site_circuits.go (circuitLoads) and
// core/loadpoint.go (the four circuit validation calls).

import (
	"fmt"
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
)

const (
	defaultSocChargeStart = 20.0
	defaultSocChargeStop  = 80.0
)

// lmState is the runtime state of the load management extensions
type lmState struct {
	once     sync.Once // defaults
	warnOnce sync.Once // incomplete battery config warning
	mu       sync.Mutex

	socChargeEnabled bool    // soc-based grid charging switch
	socChargeStart   float64 // start grid charging at or below this soc
	socChargeStop    float64 // stop grid charging at or above this soc
	socChargeRunning bool    // hysteresis state between start and stop

	batteryShedUntil time.Time   // battery grid charge hold-off after a shed
	batteryCircuit   api.Circuit // resolved from config
	batteryLoad      *batteryLoad
	batteryResolved  bool
}

// lms returns the load management state, applying defaults on first use
func (site *Site) lms() *lmState {
	site.loadMgmt.once.Do(func() {
		site.loadMgmt.socChargeStart = defaultSocChargeStart
		site.loadMgmt.socChargeStop = defaultSocChargeStop
	})
	return &site.loadMgmt
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

	lm.SetTimeout(site.LoadManagement.Timeout)

	site.publishLmSettings()
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

// LmPriority returns the configured shed priority on the same scale as the
// loadpoints' lmpriority: lower is shed first
func (b *batteryLoad) LmPriority() int {
	return b.site.LoadManagement.Battery.Priority
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

// lmBatteryCircuit returns the circuit the home battery draws from, nil when the
// battery is not part of load management
func (site *Site) lmBatteryCircuit() api.Circuit {
	ref := site.LoadManagement.Battery.CircuitRef
	if ref == "" {
		return nil
	}

	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	// circuits are configured before the site, so resolve on first use
	if !s.batteryResolved {
		s.batteryResolved = true

		if dev, err := config.Circuits().ByName(ref); err == nil {
			s.batteryCircuit = dev.Instance()
		} else {
			site.log.ERROR.Printf("load management: battery circuit %s: %v", ref, err)
		}
	}

	return s.batteryCircuit
}

// lmBatteryPhases returns the phase count used for the battery's current accounting
func (site *Site) lmBatteryPhases() int {
	if p := site.LoadManagement.Battery.Phases; p > 0 {
		return p
	}
	return lm.DefaultPhases
}

// lmBatteryChargePower returns the power the battery is expected to draw while
// grid charging: the configured value, else the batteries' max charge power
func (site *Site) lmBatteryChargePower() float64 {
	if p := site.LoadManagement.Battery.Power; p > 0 {
		return p
	}

	var res float64
	for _, dev := range site.batteryMeters {
		if m, ok := api.Cap[api.BatteryPowerLimiter](dev.Instance()); ok {
			charge, _ := m.GetPowerLimits()
			res += charge
		}
	}

	return res
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

// batteryCircuitAllows reports whether load management leaves enough headroom to
// grid-charge the battery. A battery driven via mode scripts can only be switched
// on or off, so the full expected charge power has to fit.
func (site *Site) batteryCircuitAllows() bool {
	c := site.lmBatteryCircuit()
	if c == nil {
		return true
	}

	s := site.lms()

	want := site.lmBatteryChargePower()
	if want <= 0 {
		// without an expected charge power there is nothing to check against
		s.warnOnce.Do(func() {
			site.log.WARN.Println("load management: battery grid charge power unknown, configure loadmanagement.battery.power or the battery's maxchargepower")
		})
		return true
	}

	s.mu.Lock()
	shedUntil := s.batteryShedUntil
	s.mu.Unlock()

	if now := time.Now(); now.Before(shedUntil) {
		site.log.DEBUG.Printf("battery grid charge: shed by load management, retrying in %s", shedUntil.Sub(now).Round(time.Second))
		return false
	}

	bat := site.lmBattery()

	// records the battery's unserved demand, so loads below its priority give way
	allowed := lm.ValidatePower(bat, c, bat.GetChargePower(), want)
	if allowed >= want {
		return true
	}

	holdOff := site.LoadManagement.Battery.HoldOff
	if holdOff <= 0 {
		holdOff = lm.DefaultHoldOff
	}

	s.mu.Lock()
	s.batteryShedUntil = time.Now().Add(holdOff)
	s.mu.Unlock()

	site.log.DEBUG.Printf("battery grid charge: load management allows %.0fW of %.0fW, holding off for %s", allowed, want, holdOff)

	return false
}

//
// soc-based grid charging
//

// batteryGridChargeRequested reports whether the battery should be grid-charged.
// It combines the upstream price-based limit with the soc-based switch and gates
// both on the available load management headroom.
func (site *Site) batteryGridChargeRequested(rate api.Rate) bool {
	// evaluated unconditionally so the hysteresis keeps tracking the soc
	socActive := site.batterySocChargeActive()

	if !socActive && !site.batteryGridChargeActive(rate) {
		return false
	}

	// grid charging draws from the grid and would create the very peak that the
	// reserve is being held for, see core/site_peakshaving.go
	if site.peakShavingActive() {
		site.log.DEBUG.Println("battery grid charge: blocked by peak shaving reserve")
		return false
	}

	return site.batteryCircuitAllows()
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
	defer s.mu.Unlock()

	if !s.socChargeEnabled {
		s.socChargeRunning = false
		return false
	}

	switch {
	case s.socChargeStop > 0 && soc >= s.socChargeStop:
		if s.socChargeRunning {
			site.log.DEBUG.Printf("battery soc grid charge: stop soc reached (%.0f%% >= %.0f%%)", soc, s.socChargeStop)
		}
		s.socChargeRunning = false

	case soc <= s.socChargeStart:
		if !s.socChargeRunning {
			site.log.DEBUG.Printf("battery soc grid charge: start soc reached (%.0f%% <= %.0f%%)", soc, s.socChargeStart)
		}
		s.socChargeRunning = true
	}

	return s.socChargeRunning
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
	if !val {
		s.socChargeRunning = false
	}
	s.mu.Unlock()

	if changed {
		settings.SetBool(keys.BatterySocGridCharge, val)
		site.publish(keys.BatterySocGridCharge, val)
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
	}

	return nil
}
