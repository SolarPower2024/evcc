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

	enabled bool    // peak shaving switch
	limit   float64 // grid peak limit in W
	reserve float64 // soc below which the battery is reserved for peaks
	entity  string  // Home Assistant number entity receiving the setpoint

	shaving bool     // hysteresis state: below the reserve
	written *float64 // last value written, nil until the first successful write

	set func(float64) error // resolved from config

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

	if err := site.rebuildPeakSetter(); err != nil {
		site.log.ERROR.Printf("peak shaving: %v", err)
	}

	site.publishPeakSettings()
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

	uri, err := site.peakURI()
	if err != nil {
		return err
	}

	conn, err := homeassistant.NewConnection(util.NewLogger("peakshaving"), uri, "", site.LoadManagement.PeakShaving.Insecure)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.set = func(val float64) error { return conn.CallNumberService(entity, val) }
	s.written = nil // force a write with the new target
	s.mu.Unlock()

	return nil
}

func (site *Site) publishPeakSettings() {
	s := site.peak()

	s.mu.Lock()
	enabled, limit, reserve, entity := s.enabled, s.limit, s.reserve, s.entity
	s.mu.Unlock()

	site.publish(keys.PeakShaving, enabled)
	site.publish(keys.PeakShavingLimit, limit)
	site.publish(keys.PeakShavingReserve, reserve)
	site.publish(keys.PeakShavingEntity, entity)
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
// Used to keep the battery in normal mode and to block grid charging.
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
	s.mu.Unlock()

	if !enabled || set == nil || !site.batteryConfigured() {
		// don't leave a stale reserve state behind: peakShavingActive gates grid
		// charging and the battery mode, and must not keep doing so once peak
		// shaving stopped running
		s.mu.Lock()
		s.shaving = false
		s.mu.Unlock()

		site.publish(keys.PeakShavingActive, false)

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

	if shaving {
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
// handled by the same sum.
func peakSetpoint(gridPower, batteryPower, limit float64) float64 {
	return math.Max(0, gridPower+batteryPower-limit)
}

// writePeakValue writes the setpoint, skipping unchanged values
func (site *Site) writePeakValue(value float64) {
	s := site.peak()

	s.mu.Lock()
	unchanged := s.written != nil && *s.written == value
	set := s.set
	s.mu.Unlock()

	if unchanged || set == nil {
		return
	}

	if err := set(value); err != nil {
		site.log.ERROR.Printf("peak shaving: write %.0fW: %v", value, err)

		// invalidate so the next cycle retries
		s.mu.Lock()
		s.written = nil
		s.mu.Unlock()

		return
	}

	site.log.DEBUG.Printf("peak shaving: %.0fW", value)

	s.mu.Lock()
	s.written = &value
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

// peakChargeAllowed reports whether grid-charging the battery would stay below
// the peak limit.
//
// Blocking grid charging outright whenever the reserve is armed would deadlock:
// below the reserve the battery could then only ever be refilled from pv, so an
// empty battery would stay empty through the night and have nothing left to
// shave the next peak with. What actually has to be prevented is grid charging
// creating the peak itself, which is a question of power, not of soc.
func (site *Site) peakChargeAllowed() bool {
	s := site.peak()

	s.mu.Lock()
	enabled, limit := s.enabled, s.limit
	s.mu.Unlock()

	if !enabled {
		return true
	}

	charge := site.lmBatteryChargePower()
	if charge <= 0 {
		// without a known charge power a peak cannot be ruled out
		site.log.DEBUG.Println("battery grid charge: charge power unknown, not risking a peak")
		return false
	}

	// the grid meter already reflects any ongoing charging, so add the battery
	// power back to get the demand the charging would be added to. Same reason
	// as in peakSetpoint: using the raw grid value would oscillate.
	st := site.state()

	if !peakChargeFits(st.gridPower, st.battery.Power, charge, limit) {
		site.log.DEBUG.Printf("battery grid charge: %.0fW demand plus %.0fW charge exceeds the %.0fW peak limit",
			st.gridPower+st.battery.Power, charge, limit)
		return false
	}

	return true
}

// peakChargeFits reports whether adding chargePower to the current demand stays
// within the limit. Like peakSetpoint it works on the demand rather than the raw
// grid value, so an already running charge does not make the check flip.
func peakChargeFits(gridPower, batteryPower, chargePower, limit float64) bool {
	return gridPower+batteryPower+chargePower <= limit
}

// updateBatteryModePeakAware keeps the battery in normal mode while the reserve
// is being held for peaks. Hold or charge would block the discharge controller,
// and grid charging would create the very peak we are trying to cap.
func (site *Site) updateBatteryModePeakAware(gridCharge, gridDischarge bool, rate api.Rate) {
	if !site.peakShavingActive() {
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
