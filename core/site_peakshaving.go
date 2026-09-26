package core

// Custom extension: battery peak shaving for a demand charge (Leistungspreis).
//
// The battery's lower soc range is held back as a reserve. Above the reserve the
// battery runs ordinary self-consumption and the controller is told it may
// discharge freely. Below it, the battery is only allowed to cover what exceeds
// the peak limit, so the reserve is spent on demand peaks rather than base load.
//
// The limit applies to the average of the clock-aligned 15 minute window, which is
// what the demand charge is billed on, not to the momentary grid power: energy
// not drawn earlier in the window may be drawn later, so a short spike is only
// covered when the window as a whole would end above the limit. The energy drawn
// comes from the grid meter's import counter, else from a Home Assistant energy
// sensor, else from the grid power of each cycle.
//
// evcc only computes the setpoint and writes it to a number entity; the actual
// discharge is done by the Home Assistant automation reading that entity.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/evcc-io/evcc/util/homeassistant"
)

const (
	defaultPeakLimit   = 5000.0 // W
	defaultPeakReserve = 30.0   // %

	minPeakLimit  = 2000.0 // W
	maxPeakLimit  = 20000.0
	peakLimitStep = 500.0

	// a setpoint stays in place for a whole cycle, at the end of a window it
	// reaches into the next one
	peakCycle = 30 * time.Second

	// a sample older than this does not carry over into a new window
	peakMaxGap = 2 * time.Minute

	// from this minute of the window on the allowed power no longer grows: close
	// to the end, a clock off by a few seconds could move a large draw into the
	// next window
	defaultPeakFreeze = 12 * time.Minute

	// the allowed power is at most this multiple of the limit
	defaultPeakCap = 2.0

	// an energy counter standing still while the grid power says this much was
	// drawn over peakMaxGap has stopped updating
	peakStaleWs = 20 * 3600.0 // 20Wh
)

// where the energy drawn in the window comes from
const (
	peakSourceMeter  = "meter"  // grid meter's import counter
	peakSourceEntity = "entity" // Home Assistant energy sensor
	peakSourcePower  = "power"  // grid power of each cycle
)

// peakState is the runtime state of peak shaving
type peakState struct {
	once  sync.Once
	mu    sync.Mutex
	clock clock.Clock

	enabled     bool    // peak shaving switch
	limit       float64 // grid peak limit in W
	reserve     float64 // soc below which the battery is reserved for peaks
	entity      string  // Home Assistant number entity receiving the setpoint
	chargePower float64 // assumed grid charge power in W, 0 = derive it
	circuit     string  // circuit the battery draws from, empty = fall back to yaml

	shaving    bool // hysteresis state: below the reserve
	covering   bool // covering a peak right now, for the event log
	handedBack bool // free value written since the last setpoint, nothing more to send while off

	demand      float64   // grid demand without the battery in W, from the last cycle
	chargePause time.Time // grid charging gives way to peak shaving until then

	set func(float64) error // resolved from config

	// grid charge power control: the battery charges at a power evcc writes to
	// this entity, sized to stay below the peak limit and within the circuit
	chargeEntity   string
	chargeSet      func(float64) error
	chargeSetpoint float64 // last computed setpoint in W, 0 = not charging

	// current metering window, for the 15 minute average
	windowStart time.Time
	meteredFrom time.Time // start of metering in this window, later than windowStart after a restart
	windowWs    float64   // accumulated grid energy in Ws
	demandWs    float64   // the same without the battery
	lastSample  time.Time
	windowAvg   float64 // average grid power of the running window in W
	allowed     float64 // grid power that keeps the window average at the limit in W
	frozen      float64 // allowed power at the freeze minute
	isFrozen    bool

	// energy counters for the window, in the order they are used
	gridEnergy   *float64                // grid meter import in kWh, from this cycle
	energyEntity string                  // Home Assistant energy sensor
	energyGet    func() (float64, error) // resolved from energyEntity, kWh

	source       string    // source of the last sample
	lastEnergy   float64   // counter at the last sample in kWh, valid if source is a counter
	unmovedWs    float64   // drawn according to the grid power while the counter stood still
	unmovedSince time.Time // the counter has not moved since
	stale        bool      // counter stopped updating, grid power used for the rest of the window

	months      []peakMonth // statistics, newest first, see site_peak_stats.go
	monthsDirty bool
}

// peak returns the peak shaving state, applying defaults on first use
func (site *Site) peak() *peakState {
	site.peakShaving.once.Do(func() {
		site.peakShaving.limit = defaultPeakLimit
		site.peakShaving.reserve = defaultPeakReserve
		if site.peakShaving.clock == nil {
			site.peakShaving.clock = clock.New()
		}
	})
	return &site.peakShaving
}

// restorePeakSettings restores the persisted peak shaving settings and resolves
// the output plugin
func (site *Site) restorePeakSettings() {
	s := site.peak()

	if v, err := settings.Float(keys.PeakShavingLimit); err == nil {
		s.mu.Lock()
		s.limit = v
		s.mu.Unlock()
	}
	if v, err := settings.Float(keys.PeakShavingReserve); err == nil {
		s.mu.Lock()
		s.reserve = v
		s.mu.Unlock()
	}
	if v, err := settings.Bool(keys.PeakShaving); err == nil {
		s.mu.Lock()
		s.enabled = v
		s.mu.Unlock()
	}

	if v, err := settings.String(keys.PeakShavingEntity); err == nil {
		s.mu.Lock()
		s.entity = v
		s.mu.Unlock()
	}
	if v, err := settings.Float(keys.PeakShavingChargePower); err == nil {
		s.mu.Lock()
		s.chargePower = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingCircuit); err == nil {
		s.mu.Lock()
		s.circuit = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingChargeEntity); err == nil {
		s.mu.Lock()
		s.chargeEntity = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingEnergyEntity); err == nil {
		s.mu.Lock()
		s.energyEntity = v
		s.mu.Unlock()
	}

	if err := site.rebuildPeakSetter(); err != nil {
		site.log.ERROR.Printf("peak shaving: %v", err)
	}
	if err := site.rebuildChargeSetter(); err != nil {
		site.log.ERROR.Printf("grid charge power: %v", err)
	}
	if err := site.rebuildEnergyGetter(); err != nil {
		site.log.ERROR.Printf("peak shaving energy: %v", err)
	}

	site.restorePeakMonths()
	site.publishPeakSettings()
	site.publishLmPriorities()
}

// peakURI returns the Home Assistant endpoint. Running as an add-on, the
// supervisor provides both the endpoint and the token, so nothing has to be
// configured; elsewhere the uri has to come from the yaml config.
func (site *Site) peakURI() (string, error) {
	if uri := site.LoadManagement.PeakShaving.URI; uri != "" {
		return uri, nil
	}

	if os.Getenv(homeassistant.SupervisorToken) != "" {
		return homeassistant.SupervisorURI, nil
	}

	return "", errors.New("no Home Assistant connection: running outside the add-on requires site.loadmanagement.peakshaving.uri")
}

// rebuildPeakSetter resolves the output from the configured entity, or from the
// full plugin config when one is given
func (site *Site) rebuildPeakSetter() error {
	s := site.peak()

	// an explicit plugin config wins and is resolved once
	if cfg := site.LoadManagement.PeakShaving.Set; cfg != nil {
		set, err := cfg.FloatSetter(context.TODO(), "peakshaving")
		if err != nil {
			return fmt.Errorf("output: %w", err)
		}

		s.mu.Lock()
		s.set = set
		s.handedBack = false
		s.mu.Unlock()

		return nil
	}

	s.mu.Lock()
	entity := s.entity
	s.mu.Unlock()

	if entity == "" {
		s.mu.Lock()
		s.set = nil
		s.mu.Unlock()

		return nil
	}

	set, err := site.numberSetter(entity)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.set = set
	s.handedBack = false // the new target gets the free value too
	s.mu.Unlock()

	return nil
}

// rebuildChargeSetter resolves the grid charge power output from its entity
func (site *Site) rebuildChargeSetter() error {
	s := site.peak()

	s.mu.Lock()
	entity := s.chargeEntity
	s.mu.Unlock()

	var set func(float64) error

	if entity != "" {
		var err error
		if set, err = site.numberSetter(entity); err != nil {
			return err
		}
	}

	s.mu.Lock()
	s.chargeSet = set
	s.mu.Unlock()

	return nil
}

// rebuildEnergyGetter resolves the energy sensor
func (site *Site) rebuildEnergyGetter() error {
	s := site.peak()

	s.mu.Lock()
	entity := s.energyEntity
	s.mu.Unlock()

	var get func() (float64, error)

	if entity != "" {
		conn, err := site.haConnection()
		if err != nil {
			return err
		}
		get = func() (float64, error) { return conn.GetFloatState(entity) }
	}

	s.mu.Lock()
	s.energyGet = get
	s.mu.Unlock()

	return nil
}

func (site *Site) haConnection() (*homeassistant.Connection, error) {
	uri, err := site.peakURI()
	if err != nil {
		return nil, err
	}

	return homeassistant.NewConnection(util.NewLogger("peakshaving"), uri, "", site.LoadManagement.PeakShaving.Insecure)
}

// numberSetter returns a setter writing to a Home Assistant number entity
func (site *Site) numberSetter(entity string) (func(float64) error, error) {
	conn, err := site.haConnection()
	if err != nil {
		return nil, err
	}

	return func(val float64) error { return conn.CallNumberService(entity, val) }, nil
}

func (site *Site) publishPeakSettings() {
	s := site.peak()

	s.mu.Lock()
	enabled, limit, reserve, entity, charge, circuit := s.enabled, s.limit, s.reserve, s.entity, s.chargePower, s.circuit
	chargeEntity, energyEntity := s.chargeEntity, s.energyEntity
	s.mu.Unlock()

	site.publish(keys.PeakShaving, enabled)
	site.publish(keys.PeakShavingLimit, limit)
	site.publish(keys.PeakShavingReserve, reserve)
	site.publish(keys.PeakShavingEntity, entity)
	site.publish(keys.PeakShavingChargePower, charge)
	site.publish(keys.PeakShavingCircuit, circuit)
	site.publish(keys.PeakShavingChargeEntity, chargeEntity)
	site.publish(keys.PeakShavingEnergyEntity, energyEntity)

	site.publishChargePower()
}

// publishChargePower reports the charge power actually in use and where it came
// from, so the assumption the grid charge gate makes is visible in the ui
func (site *Site) publishChargePower() {
	effective, source := site.lmBatteryChargePower()

	site.publish(keys.PeakShavingChargePowerEffective, effective)
	site.publish(keys.PeakShavingChargePowerSource, source)
}

// peakFreeValue returns the value signalling unrestricted discharge
func (site *Site) peakFreeValue() float64 {
	if v := site.advanced().FreeValue; v != nil {
		return *v
	}
	if v := site.LoadManagement.PeakShaving.FreeValue; v > 0 {
		return v
	}
	return lm.DefaultFreeValue
}

// peakFreeze returns the minute of the window from which the allowed power no
// longer grows
func (site *Site) peakFreeze() time.Duration {
	if v := site.advanced().PeakFreeze; v != nil {
		return time.Duration(*v) * time.Minute
	}
	return defaultPeakFreeze
}

// peakCap returns the maximum allowed power as a multiple of the limit
func (site *Site) peakCap() float64 {
	if v := site.advanced().PeakCap; v != nil {
		return *v
	}
	return defaultPeakCap
}

func (site *Site) peakHysteresis() float64 {
	if v := site.advanced().Hysteresis; v != nil {
		return *v
	}
	if v := site.LoadManagement.PeakShaving.Hysteresis; v > 0 {
		return v
	}
	return lm.DefaultHysteresis
}

// peakShavingActive reports whether the battery is currently held back for peaks.
// Used to keep the battery in normal mode so the discharge controller is not
// blocked, see updateBatteryModePeakAware.
func (site *Site) peakShavingActive() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.enabled && s.shaving
}

// updatePeakShaving computes the battery power required to stay below the peak
// limit and writes it to the configured number entity. Called once per cycle.
func (site *Site) updatePeakShaving(state siteState) {
	s := site.peak()
	defer site.savePeakMonths()

	site.updatePeakWindow(state.gridPower, state.battery.Power)

	s.mu.Lock()
	enabled, limit, reserve, set, allowed := s.enabled, s.limit, s.reserve, s.set, s.allowed
	// read by peakPausesGridCharge later in the same cycle
	s.demand = state.gridPower + state.battery.Power
	s.mu.Unlock()

	if !enabled || set == nil || !site.batteryConfigured() {
		// don't leave a stale reserve state behind: peakShavingActive gates grid
		// charging and the battery mode, and must not keep doing so once peak
		// shaving stopped running
		s.mu.Lock()
		s.shaving = false
		s.covering = false
		s.mu.Unlock()

		site.publish(keys.PeakShavingActive, false)

		// hand control back once, retried until the write lands
		site.handBackPeak()

		return
	}

	soc := site.GetBatterySoc()
	hyst := site.peakHysteresis()

	s.mu.Lock()
	// below the reserve the battery is for peaks only; the band keeps a
	// fluctuating soc from flapping across the boundary
	switch {
	case soc <= reserve:
		s.shaving = true
	case soc >= reserve+hyst:
		s.shaving = false
	}
	shaving := s.shaving
	s.mu.Unlock()

	value := site.peakFreeValue()

	switch {
	// no discharging while the battery charges from the grid. A peak pauses the
	// charging in this same cycle, see peakPausesGridCharge, so the value then
	// falls through to the regular one right away.
	case site.GetBatteryMode() == api.BatteryCharge && state.gridPower+state.battery.Power <= limit:
		value = 0

	case shaving:
		value = peakSetpoint(state.gridPower, state.battery.Power, allowed)

	// the optimizer withholds discharging: only a peak is covered, see site_optimizer_lm.go
	case site.optimizerHolds():
		value = peakSetpoint(state.gridPower, state.battery.Power, allowed)
	}

	// log the start of a peak, not every cycle of it
	s.mu.Lock()
	covering := shaving && value > 0
	started := covering && !s.covering
	s.covering = covering
	if started {
		s.recordPeakIntervention(s.clock.Now())
	}
	s.mu.Unlock()

	if started {
		lm.AddEvent(lm.Event{At: s.clock.Now(), Type: lm.EventPeak, A: state.gridPower + state.battery.Power, B: limit})
	}

	// published explicitly rather than left for the ui to infer from the value:
	// a setpoint can legitimately equal the free value, e.g. a 15kW demand
	// against a 5kW limit asks for exactly 10000W
	site.publish(keys.PeakShavingActive, shaving)
	site.publish(keys.PeakShavingPower, value)
	site.writePeakValue(value)

	// also covers a setpoint written while switching off, which then gets
	// replaced by the free value in the next cycle
	s.mu.Lock()
	s.handedBack = false
	s.mu.Unlock()
}

// peakSetpoint returns the battery power needed to keep the grid draw at or
// below the allowed power, see peakAllowed.
//
// It deliberately does not use the grid power on its own. The grid meter already
// reflects whatever the battery is doing, so feeding that back would make the
// controller chase its own output: it would shave, see a compliant grid value,
// stop shaving, see the peak return, and oscillate every cycle. Adding the
// battery power back recovers the demand as it would be without the battery,
// which is a fixed quantity the setpoint can be derived from. evcc counts
// discharging as positive and charging as negative, so both directions are
// handled by the same sum. Whole watts are plenty, and some number entities
// reject fractions.
func peakSetpoint(gridPower, batteryPower, allowed float64) float64 {
	return math.Max(0, math.Round(gridPower+batteryPower-allowed))
}

// peakAllowed returns the grid power that may be drawn for the rest of the window
// with the window average still ending at the limit. Energy left unused earlier
// allows more, energy drawn above the limit allows less, down to nothing once the
// window's budget is spent.
func peakAllowed(limit, usedWs float64, elapsed time.Duration) float64 {
	budget := limit*lm.PeakWindow.Seconds() - usedWs
	remaining := lm.PeakWindow - elapsed

	// the part of the cycle reaching into the next window gets that window's
	// budget, rather than squeezing this window's rest into a few seconds
	if remaining < peakCycle {
		budget += limit * (peakCycle - remaining).Seconds()
		remaining = peakCycle
	}

	return max(0, budget/remaining.Seconds())
}

// writePeakValue writes the setpoint
func (site *Site) writePeakValue(value float64) bool {
	s := site.peak()

	s.mu.Lock()
	set := s.set
	s.mu.Unlock()

	return site.writeOutput("peak shaving", set, value)
}

// handBackPeak writes the free value unless it is already in the entity. While
// peak shaving is off nothing else is sent, a single write is enough.
func (site *Site) handBackPeak() {
	s := site.peak()

	s.mu.Lock()
	done := s.handedBack
	s.mu.Unlock()

	if done || !site.writePeakValue(site.peakFreeValue()) {
		return
	}

	s.mu.Lock()
	s.handedBack = true
	s.mu.Unlock()
}

// writeChargeValue writes the grid charge power setpoint
func (site *Site) writeChargeValue(value float64) {
	s := site.peak()

	s.mu.Lock()
	set := s.chargeSet
	s.chargeSetpoint = value
	s.mu.Unlock()

	site.publish(keys.PeakShavingChargeSetpoint, value)
	site.writeOutput("grid charge power", set, value)
}

// writeOutput writes a value through set and reports whether it landed. It is
// called every cycle and writes even an unchanged value, so an entity changed by
// hand, by an automation or by a Home Assistant restart is corrected in the next
// cycle.
func (site *Site) writeOutput(name string, set func(float64) error, value float64) bool {
	if set == nil {
		return false
	}

	if err := set(value); err != nil {
		site.log.ERROR.Printf("%s: write %.0fW: %v", name, value, err)
		return false
	}

	site.log.DEBUG.Printf("%s: %.0fW", name, value)

	return true
}

// peakEnergy returns the grid import counter in kWh and where it came from: the
// grid meter, else the Home Assistant sensor. Without either, the source is the
// grid power.
func (site *Site) peakEnergy() (float64, string) {
	s := site.peak()

	s.mu.Lock()
	meter, get, entity := s.gridEnergy, s.energyGet, s.energyEntity
	s.gridEnergy = nil // only valid for the cycle it was read in
	s.mu.Unlock()

	if meter != nil {
		return *meter, peakSourceMeter
	}

	if get != nil {
		v, err := get()
		if err == nil {
			return v, peakSourceEntity
		}
		site.log.WARN.Printf("peak shaving: energy %s: %v, using the grid power", entity, err)
	}

	return 0, peakSourcePower
}

// setPeakGridEnergy takes the grid meter's import counter of this cycle, nil if
// the meter has none
func (site *Site) setPeakGridEnergy(kWh *float64) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.gridEnergy = kWh
}

// drawn returns the energy drawn since the last sample in Ws. A counter is used
// when it was read now and last time from the same source, the grid power fills
// in otherwise. Must be called with the lock held.
func (s *peakState) drawn(now time.Time, imported, energy float64, source string) float64 {
	powerWs := imported * now.Sub(s.lastSample).Seconds()

	if source == peakSourcePower || source != s.source || s.stale {
		return powerWs
	}

	switch d := (energy - s.lastEnergy) * 3600e3; {
	case d < 0:
		// counter reset or replaced
		return powerWs

	case d > 0:
		s.unmovedWs, s.unmovedSince = 0, time.Time{}
		return d
	}

	// the counter stands still: nothing drawn, or it is late and catches up
	// with its next step. If it does not, the grid power takes over.
	if powerWs == 0 {
		return 0
	}
	if s.unmovedSince.IsZero() {
		s.unmovedSince = s.lastSample
	}
	s.unmovedWs += powerWs

	if s.unmovedWs < peakStaleWs || now.Sub(s.unmovedSince) <= peakMaxGap {
		return 0
	}

	s.stale = true
	return s.unmovedWs
}

// updatePeakWindow tracks the grid energy of the running 15 minute metering
// window, which is what a demand charge is billed on, and the grid power allowed
// for the rest of it. A completed window goes into the monthly statistics, with
// the battery power added back for the demand without it.
func (site *Site) updatePeakWindow(gridPower, batteryPower float64) {
	s := site.peak()
	energy, source := site.peakEnergy()
	now := s.clock.Now()
	freeze, capFactor := site.peakFreeze(), site.peakCap()

	// clock-aligned windows, matching how the meter registers them
	start := now.Truncate(lm.PeakWindow)

	// only the import direction contributes to the demand peak
	imported := math.Max(0, gridPower)

	s.mu.Lock()
	defer s.mu.Unlock()

	var drawn, demand float64
	if !s.lastSample.IsZero() {
		wasStale := s.stale
		drawn = s.drawn(now, imported, energy, source)
		demand = max(0, drawn+batteryPower*now.Sub(s.lastSample).Seconds())

		if s.stale && !wasStale {
			site.log.WARN.Printf("peak shaving: the %s energy counter stopped updating, using the grid power until the window ends", source)
		}
	}

	if !s.windowStart.Equal(start) {
		// a sample from the previous window carries over: the part of the
		// interval since the boundary is metered, the rest was the last window's
		if !s.lastSample.IsZero() && s.lastSample.Before(start) && now.Sub(s.lastSample) <= peakMaxGap {
			after := now.Sub(start).Seconds() / now.Sub(s.lastSample).Seconds()

			// only a window metered from its start counts for the statistics
			if s.meteredFrom.Equal(s.windowStart) && !s.windowStart.IsZero() {
				s.recordPeakWindow(s.windowStart, s.windowWs+drawn*(1-after), s.demandWs+demand*(1-after))
			}

			s.meteredFrom = start
			s.windowWs, s.demandWs = drawn*after, demand*after
		} else {
			s.meteredFrom = now
			s.windowWs, s.demandWs = 0, 0
		}

		s.windowStart = start
		s.isFrozen = false
		s.stale = false
		s.unmovedWs, s.unmovedSince = 0, time.Time{}
	} else {
		s.windowWs += drawn
		s.demandWs += demand
	}

	s.lastSample = now
	s.source, s.lastEnergy = source, energy
	if s.stale {
		source = peakSourcePower
	}

	if metered := now.Sub(s.meteredFrom).Seconds(); metered > 0 {
		s.windowAvg = s.windowWs / metered
	}

	// what evcc did not see, after a start or a gap, is counted at the limit:
	// assuming less could spend a budget that was already used
	used := s.windowWs + s.limit*s.meteredFrom.Sub(start).Seconds()
	elapsed := now.Sub(start)
	allowed := min(peakAllowed(s.limit, used, elapsed), s.limit*capFactor)

	if elapsed >= freeze {
		if !s.isFrozen {
			s.frozen, s.isFrozen = allowed, true
		}
		allowed = min(allowed, s.frozen)
	}
	s.allowed = allowed

	site.publish(keys.PeakShavingWindowAvg, s.windowAvg)
	site.publish(keys.PeakShavingAllowed, s.allowed)
	site.publish(keys.PeakShavingSource, source)
	site.publish(keys.PeakShavingWindowEnd, start.Add(lm.PeakWindow))
}

// peakPausesGridCharge reports whether grid charging has to give way to peak
// shaving. While the demand without the battery is above the peak limit, the
// battery is needed to cover it, and charging it from the grid at the same time
// would only add to the peak.
//
// The charge power itself is deliberately not counted against the peak limit:
// the charger alone may well draw more than the limit, and whether it fits is
// the circuit's call, see batteryCircuitAllows. After a peak, charging stays
// off for the hold-off, so a demand hovering around the limit does not flip
// the battery between charging and discharging every cycle.
func (site *Site) peakPausesGridCharge() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled || s.set == nil {
		return false
	}

	now := s.clock.Now()

	if s.demand > s.limit {
		if !now.Before(s.chargePause) {
			site.log.DEBUG.Printf("battery grid charge: paused, demand %.0fW exceeds the %.0fW peak limit", s.demand, s.limit)
			lm.AddEvent(lm.Event{At: now, Type: lm.EventGridChargePaused, A: s.demand, B: s.limit})
		}
		s.chargePause = now.Add(site.lmHoldOff())
		return true
	}

	return now.Before(s.chargePause)
}

// peakChargeHeadroom returns how much grid charge power fits below the peak
// limit on top of the current demand. ok is false while peak shaving is off,
// there is no limit to fit under then.
func (site *Site) peakChargeHeadroom() (headroom float64, ok bool) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled || s.set == nil {
		return 0, false
	}

	return max(0, s.limit-s.demand), true
}

// updateBatteryModePeakAware keeps the battery in normal mode while the reserve
// is being held for peaks, as hold would block the discharge controller. This is
// the one place the fork overrides upstream's battery mode, and only below the
// reserve: grid charging has already been cleared against both the circuit and a
// running peak (see batteryGridChargeRequested), and a mode set from outside
// through the api stays the caller's decision.
func (site *Site) updateBatteryModePeakAware(gridCharge, gridDischarge bool, rate api.Rate) {
	// the last hook of the cycle: everything the overview shows is decided now
	defer site.publishLmStatus(gridCharge)
	defer site.publishLmWallboxes()
	defer site.checkLmFollowing()

	if gridCharge || site.optimizerCharges() || !site.peakShavingActive() || site.GetBatteryModeExternal() != api.BatteryUnknown {
		site.updateBatteryMode(gridCharge, gridDischarge, rate)
		return
	}

	// the optimizer's requests pass the gate only on the path above
	if site.optimizerInControl() {
		site.releaseGridCharge()
	}

	if site.GetBatteryMode() == api.BatteryNormal {
		return
	}

	site.log.DEBUG.Println("battery mode: peak shaving reserve")

	if err := site.applyBatteryMode(api.BatteryNormal); err != nil {
		site.log.ERROR.Println("battery mode:", err)
		return
	}

	site.SetBatteryMode(api.BatteryNormal)
}

//
// api
//

func (site *Site) GetPeakShaving() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.enabled
}

func (site *Site) SetPeakShaving(val bool) error {
	if !site.batteryConfigured() {
		return ErrBatteryNotConfigured
	}

	s := site.peak()

	s.mu.Lock()
	configured := s.set != nil
	s.mu.Unlock()

	if val && !configured {
		return errors.New("no target entity configured")
	}

	site.log.DEBUG.Println("set peak shaving:", val)

	s.mu.Lock()
	changed := s.enabled != val
	s.enabled = val
	if !val {
		s.shaving = false
		s.handedBack = false
	}
	s.mu.Unlock()

	if changed {
		settings.SetBool(keys.PeakShaving, val)
		site.publish(keys.PeakShaving, val)

		// hand control back when switching off
		if !val {
			site.handBackPeak()
		}
	}

	return nil
}

func (site *Site) GetPeakShavingEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.entity
}

// SetPeakShavingEntity sets the Home Assistant number entity receiving the
// setpoint and rebuilds the connection
func (site *Site) SetPeakShavingEntity(entity string) error {
	if entity != "" && !strings.HasPrefix(entity, "number.") && !strings.HasPrefix(entity, "input_number.") {
		return fmt.Errorf("must be a number or input_number entity: %s", entity)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.entity != entity
	previous := s.entity
	s.entity = entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	if err := site.rebuildPeakSetter(); err != nil {
		// keep the working target rather than leaving peak shaving mute
		s.mu.Lock()
		s.entity = previous
		s.mu.Unlock()

		return err
	}

	site.log.DEBUG.Println("set peak shaving entity:", entity)
	settings.SetString(keys.PeakShavingEntity, entity)
	site.publish(keys.PeakShavingEntity, entity)

	// an empty target cannot do anything, so don't pretend it is running
	if entity == "" {
		return site.SetPeakShaving(false)
	}

	return nil
}

// GetPeakShavingChargeEntity returns the entity receiving the grid charge power
func (site *Site) GetPeakShavingChargeEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.chargeEntity
}

// SetPeakShavingChargeEntity sets the Home Assistant number entity receiving the
// grid charge power. With it, grid charging is throttled to stay below the peak
// limit instead of being switched off; empty returns to on/off charging.
func (site *Site) SetPeakShavingChargeEntity(entity string) error {
	if entity != "" && !strings.HasPrefix(entity, "number.") && !strings.HasPrefix(entity, "input_number.") {
		return fmt.Errorf("must be a number or input_number entity: %s", entity)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.chargeEntity != entity
	previous := s.chargeEntity
	s.chargeEntity = entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	if err := site.rebuildChargeSetter(); err != nil {
		s.mu.Lock()
		s.chargeEntity = previous
		s.mu.Unlock()

		return err
	}

	site.log.DEBUG.Println("set grid charge power entity:", entity)
	settings.SetString(keys.PeakShavingChargeEntity, entity)
	site.publish(keys.PeakShavingChargeEntity, entity)

	return nil
}

// GetPeakShavingEnergyEntity returns the Home Assistant energy sensor the window
// is metered with when the grid meter has no import counter
func (site *Site) GetPeakShavingEnergyEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.energyEntity
}

// SetPeakShavingEnergyEntity sets the Home Assistant energy sensor, a counter of
// the grid import in kWh or Wh. Empty returns to the grid power.
func (site *Site) SetPeakShavingEnergyEntity(entity string) error {
	if entity != "" {
		if !strings.HasPrefix(entity, "sensor.") && !strings.HasPrefix(entity, "input_number.") {
			return fmt.Errorf("must be a sensor or input_number entity: %s", entity)
		}

		conn, err := site.haConnection()
		if err != nil {
			return err
		}
		state, err := conn.GetState(entity)
		if err != nil {
			return fmt.Errorf("%s: %w", entity, err)
		}
		if unit := state.Attributes.UnitOfMeasurement; unit != "kWh" && unit != "Wh" {
			return fmt.Errorf("%s must be an energy counter in kWh or Wh, not %q", entity, unit)
		}
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.energyEntity != entity
	previous := s.energyEntity
	s.energyEntity = entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	if err := site.rebuildEnergyGetter(); err != nil {
		s.mu.Lock()
		s.energyEntity = previous
		s.mu.Unlock()

		return err
	}

	site.log.DEBUG.Println("set peak shaving energy entity:", entity)
	settings.SetString(keys.PeakShavingEnergyEntity, entity)
	site.publish(keys.PeakShavingEnergyEntity, entity)

	return nil
}

// chargePowerControlled reports whether the grid charge power is set through an
// entity rather than charging being switched on or off
func (site *Site) chargePowerControlled() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.chargeSet != nil
}

// GetPeakShavingChargePower returns the assumed grid charge power, 0 = derived
func (site *Site) GetPeakShavingChargePower() float64 {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.chargePower
}

// SetPeakShavingChargePower sets the assumed grid charge power in W. Zero falls
// back to the yaml config and then to the battery meters' maxchargepower.
func (site *Site) SetPeakShavingChargePower(power float64) error {
	if power < 0 || power > maxPeakLimit {
		return fmt.Errorf("charge power must be between 0 and %.0fW", maxPeakLimit)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.chargePower != power
	s.chargePower = power
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set peak shaving charge power:", power)
		settings.SetFloat(keys.PeakShavingChargePower, power)
		site.publish(keys.PeakShavingChargePower, power)
		site.publishChargePower()
	}

	return nil
}

// GetPeakShavingCircuit returns the circuit the battery draws from
func (site *Site) GetPeakShavingCircuit() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.circuit
}

// SetPeakShavingCircuit assigns the battery to a circuit. That link is what
// makes the battery take part in load management and what the grid charge gate
// checks against; an empty value falls back to the yaml config.
func (site *Site) SetPeakShavingCircuit(name string) error {
	if name != "" {
		if _, err := config.Circuits().ByName(name); err != nil {
			return fmt.Errorf("unknown circuit: %s", name)
		}
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.circuit != name
	s.circuit = name
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set peak shaving circuit:", name)
		settings.SetString(keys.PeakShavingCircuit, name)
		site.publish(keys.PeakShavingCircuit, name)

		// the battery only appears among the priorities once it is on a circuit
		site.publishLmPriorities()
	}

	return nil
}

func (site *Site) GetPeakShavingLimit() float64 {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.limit
}

func (site *Site) SetPeakShavingLimit(limit float64) error {
	if limit < minPeakLimit || limit > maxPeakLimit {
		return fmt.Errorf("peak limit must be between %.0fW and %.0fW", minPeakLimit, maxPeakLimit)
	}
	if math.Mod(limit, peakLimitStep) != 0 {
		return fmt.Errorf("peak limit must be a multiple of %.0fW", peakLimitStep)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.limit != limit
	s.limit = limit
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set peak shaving limit:", limit)
		settings.SetFloat(keys.PeakShavingLimit, limit)
		site.publish(keys.PeakShavingLimit, limit)
	}

	return nil
}

func (site *Site) GetPeakShavingReserve() float64 {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.reserve
}

func (site *Site) SetPeakShavingReserve(soc float64) error {
	if soc <= 0 || soc >= 100 {
		return fmt.Errorf("invalid reserve soc: %.0f", soc)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.reserve != soc
	s.reserve = soc
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set peak shaving reserve:", soc)
		settings.SetFloat(keys.PeakShavingReserve, soc)
		site.publish(keys.PeakShavingReserve, soc)
	}

	return nil
}
