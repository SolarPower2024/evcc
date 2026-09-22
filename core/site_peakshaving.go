package core

// Custom extension: battery peak shaving for a demand charge (Leistungspreis).
//
// The battery's lower soc range is held back as a reserve. Above the reserve the
// battery runs ordinary self-consumption and the controller is told it may
// discharge freely. Below it, the battery is only allowed to cover what exceeds
// the peak limit, so the reserve is spent on demand peaks rather than base load.
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
)

// peakState is the runtime state of peak shaving
type peakState struct {
	once sync.Once
	mu   sync.Mutex

	enabled     bool    // peak shaving switch
	limit       float64 // grid peak limit in W
	reserve     float64 // soc below which the battery is reserved for peaks
	entity      string  // Home Assistant number entity receiving the setpoint
	chargePower float64 // assumed grid charge power in W, 0 = derive it
	circuit     string  // circuit the battery draws from, empty = fall back to yaml

	shaving bool     // hysteresis state: below the reserve
	written *float64 // last value written, nil until the first successful write

	demand      float64   // grid demand without the battery in W, from the last cycle
	chargePause time.Time // grid charging gives way to peak shaving until then

	set func(float64) error // resolved from config

	// grid charge power control: the battery charges at a power evcc writes to
	// this entity, sized to stay below the peak limit and within the circuit
	chargeEntity   string
	chargeSet      func(float64) error
	chargeWritten  *float64
	chargeSetpoint float64 // last computed setpoint in W, 0 = not charging

	// current metering window, for the 15 minute average
	windowStart time.Time
	windowWs    float64 // accumulated grid energy in Ws
	lastSample  time.Time
	windowAvg   float64 // average grid power of the running window in W
}

// peak returns the peak shaving state, applying defaults on first use
func (site *Site) peak() *peakState {
	site.peakShaving.once.Do(func() {
		site.peakShaving.limit = defaultPeakLimit
		site.peakShaving.reserve = defaultPeakReserve
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

	if err := site.rebuildPeakSetter(); err != nil {
		site.log.ERROR.Printf("peak shaving: %v", err)
	}
	if err := site.rebuildChargeSetter(); err != nil {
		site.log.ERROR.Printf("grid charge power: %v", err)
	}

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
	s.written = nil // force a write with the new target
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
	s.chargeWritten = nil // force a write with the new target
	s.mu.Unlock()

	return nil
}

// numberSetter returns a setter writing to a Home Assistant number entity
func (site *Site) numberSetter(entity string) (func(float64) error, error) {
	uri, err := site.peakURI()
	if err != nil {
		return nil, err
	}

	conn, err := homeassistant.NewConnection(util.NewLogger("peakshaving"), uri, "", site.LoadManagement.PeakShaving.Insecure)
	if err != nil {
		return nil, err
	}

	return func(val float64) error { return conn.CallNumberService(entity, val) }, nil
}

func (site *Site) publishPeakSettings() {
	s := site.peak()

	s.mu.Lock()
	enabled, limit, reserve, entity, charge, circuit := s.enabled, s.limit, s.reserve, s.entity, s.chargePower, s.circuit
	chargeEntity := s.chargeEntity
	s.mu.Unlock()

	site.publish(keys.PeakShaving, enabled)
	site.publish(keys.PeakShavingLimit, limit)
	site.publish(keys.PeakShavingReserve, reserve)
	site.publish(keys.PeakShavingEntity, entity)
	site.publish(keys.PeakShavingChargePower, charge)
	site.publish(keys.PeakShavingCircuit, circuit)
	site.publish(keys.PeakShavingChargeEntity, chargeEntity)

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
	if v := site.LoadManagement.PeakShaving.FreeValue; v > 0 {
		return v
	}
	return lm.DefaultFreeValue
}

func (site *Site) peakHysteresis() float64 {
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

	site.updatePeakWindow(state.gridPower)

	s.mu.Lock()
	enabled, limit, reserve, set := s.enabled, s.limit, s.reserve, s.set
	// read by peakPausesGridCharge later in the same cycle
	s.demand = state.gridPower + state.battery.Power
	s.mu.Unlock()

	if !enabled || set == nil || !site.batteryConfigured() {
		// don't leave a stale reserve state behind: peakShavingActive gates grid
		// charging and the battery mode, and must not keep doing so once peak
		// shaving stopped running
		s.mu.Lock()
		s.shaving = false
		s.mu.Unlock()

		site.publish(keys.PeakShavingActive, false)

		// keep handing control back: the write when switching off may have
		// failed or raced a setpoint write. Unchanged values are skipped.
		site.writePeakValue(site.peakFreeValue())

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
		value = peakSetpoint(state.gridPower, state.battery.Power, limit)
	}

	// published explicitly rather than left for the ui to infer from the value:
	// a setpoint can legitimately equal the free value, e.g. a 15kW demand
	// against a 5kW limit asks for exactly 10000W
	site.publish(keys.PeakShavingActive, shaving)
	site.publish(keys.PeakShavingPower, value)
	site.writePeakValue(value)
}

// peakSetpoint returns the battery power needed to keep the grid draw at or
// below the limit.
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
func peakSetpoint(gridPower, batteryPower, limit float64) float64 {
	return math.Max(0, math.Round(gridPower+batteryPower-limit))
}

// writePeakValue writes the setpoint, skipping unchanged values
func (site *Site) writePeakValue(value float64) {
	s := site.peak()

	s.mu.Lock()
	set := s.set
	s.mu.Unlock()

	site.writeOutput("peak shaving", set, &s.written, value)
}

// writeChargeValue writes the grid charge power setpoint, skipping unchanged values
func (site *Site) writeChargeValue(value float64) {
	s := site.peak()

	s.mu.Lock()
	set := s.chargeSet
	s.chargeSetpoint = value
	s.mu.Unlock()

	site.publish(keys.PeakShavingChargeSetpoint, value)
	site.writeOutput("grid charge power", set, &s.chargeWritten, value)
}

// writeOutput writes a value through set unless it is the last one written.
// last points into peakState and is guarded by its mutex. A failed write clears
// it, so the next cycle retries.
func (site *Site) writeOutput(name string, set func(float64) error, last **float64, value float64) {
	if set == nil {
		return
	}

	s := site.peak()

	s.mu.Lock()
	unchanged := *last != nil && **last == value
	s.mu.Unlock()

	if unchanged {
		return
	}

	if err := set(value); err != nil {
		site.log.ERROR.Printf("%s: write %.0fW: %v", name, value, err)

		s.mu.Lock()
		*last = nil
		s.mu.Unlock()

		return
	}

	site.log.DEBUG.Printf("%s: %.0fW", name, value)

	s.mu.Lock()
	*last = &value
	s.mu.Unlock()
}

// updatePeakWindow tracks the average grid power of the running 15 minute
// metering window, which is what a demand charge is billed on
func (site *Site) updatePeakWindow(gridPower float64) {
	s := site.peak()
	now := time.Now()

	// clock-aligned windows, matching how the meter registers them
	start := now.Truncate(lm.PeakWindow)

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.windowStart.Equal(start) {
		s.windowStart = start
		s.windowWs = 0
		s.lastSample = now
	}

	if !s.lastSample.IsZero() {
		// only the import direction contributes to the demand peak
		s.windowWs += math.Max(0, gridPower) * now.Sub(s.lastSample).Seconds()
	}
	s.lastSample = now

	if elapsed := now.Sub(start).Seconds(); elapsed > 0 {
		s.windowAvg = s.windowWs / elapsed
	}

	site.publish(keys.PeakShavingWindowAvg, s.windowAvg)
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

	now := time.Now()

	if s.demand > s.limit {
		if !now.Before(s.chargePause) {
			site.log.DEBUG.Printf("battery grid charge: paused, demand %.0fW exceeds the %.0fW peak limit", s.demand, s.limit)
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
// is being held for peaks, as hold would block the discharge controller. Grid
// charging is the exception: it has already been cleared against both the
// circuit and a running peak, see batteryGridChargeRequested.
func (site *Site) updateBatteryModePeakAware(gridCharge, gridDischarge bool, rate api.Rate) {
	if gridCharge || !site.peakShavingActive() {
		site.updateBatteryMode(gridCharge, gridDischarge, rate)
		return
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
	}
	s.mu.Unlock()

	if changed {
		settings.SetBool(keys.PeakShaving, val)
		site.publish(keys.PeakShaving, val)

		// hand control back when switching off
		if !val {
			site.writePeakValue(site.peakFreeValue())
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
