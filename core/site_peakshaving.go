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
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/db/settings"
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

	if cfg := site.LoadManagement.PeakShaving.Set; cfg != nil {
		set, err := cfg.FloatSetter(context.TODO(), "peakshaving")
		if err != nil {
			site.log.ERROR.Printf("peak shaving: output: %v", err)
		} else {
			s.mu.Lock()
			s.set = set
			s.mu.Unlock()
		}
	}

	site.publishPeakSettings()
}

func (site *Site) publishPeakSettings() {
	s := site.peak()

	s.mu.Lock()
	enabled, limit, reserve := s.enabled, s.limit, s.reserve
	s.mu.Unlock()

	site.publish(keys.PeakShaving, enabled)
	site.publish(keys.PeakShavingLimit, limit)
	site.publish(keys.PeakShavingReserve, reserve)
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

	if val && site.LoadManagement.PeakShaving.Set == nil {
		return fmt.Errorf("peak shaving: no output entity configured, set site.loadmanagement.peakshaving.set")
	}

	site.log.DEBUG.Println("set peak shaving:", val)

	s := site.peak()

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
