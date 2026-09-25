package core

// Custom extension: the advanced load management settings, set in the ui under
// Lastmanagement-Details → Erweitert. Each one overrides its yaml value, which
// in turn overrides the default. Unset values are not stored, so a default
// changed in a later version still applies.

import (
	"fmt"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/db/settings"
)

// lmAdvanced holds the values set in the ui, nil = not set
type lmAdvanced struct {
	Hysteresis *float64 `json:"hysteresis,omitempty"` // peak reserve soc band in %
	FreeValue  *float64 `json:"freeValue,omitempty"`  // "discharge freely" setpoint in W
	HoldOff    *float64 `json:"holdOff,omitempty"`    // battery grid charge hold-off in minutes
	Timeout    *float64 `json:"timeout,omitempty"`    // unserved demand expiry in minutes
	Phases     *float64 `json:"phases,omitempty"`     // battery phases for current accounting
	PeakFreeze *float64 `json:"peakFreeze,omitempty"` // minute of the window from which the peak budget no longer grows
	PeakCap    *float64 `json:"peakCap,omitempty"`    // allowed grid power at most this multiple of the peak limit
}

// lmAdvancedState is what the ui shows: the values in effect
type lmAdvancedState struct {
	Hysteresis float64 `json:"hysteresis"`
	FreeValue  float64 `json:"freeValue"`
	HoldOff    float64 `json:"holdOff"`
	Timeout    float64 `json:"timeout"`
	Phases     int     `json:"phases"`
	PeakFreeze float64 `json:"peakFreeze"`
	PeakCap    float64 `json:"peakCap"`
}

// lmAdvancedLimit is a setting's valid range
type lmAdvancedLimit struct {
	min, max float64
	integer  bool
}

var lmAdvancedLimits = map[string]lmAdvancedLimit{
	"hysteresis": {0, 20, false},
	"freeValue":  {1, 100000, true},
	"holdOff":    {1, 60, true},
	"timeout":    {1, 60, true},
	"phases":     {1, 3, true},
	"peakFreeze": {1, 14, true},
	"peakCap":    {1, 10, false},
}

// restoreLmAdvanced restores the persisted advanced settings
func (site *Site) restoreLmAdvanced() {
	s := site.lms()

	var adv lmAdvanced
	if err := settings.Json(keys.LmAdvanced, &adv); err == nil {
		s.advMu.Lock()
		s.adv = adv
		s.advMu.Unlock()
	}

	lm.SetTimeout(site.lmTimeout())

	site.publishLmAdvanced()
}

// advanced returns a copy of the values set in the ui
func (site *Site) advanced() lmAdvanced {
	s := site.lms()

	s.advMu.Lock()
	defer s.advMu.Unlock()

	return s.adv
}

func (site *Site) publishLmAdvanced() {
	site.publish(keys.LmAdvanced, lmAdvancedState{
		Hysteresis: site.peakHysteresis(),
		FreeValue:  site.peakFreeValue(),
		HoldOff:    site.lmHoldOff().Minutes(),
		Timeout:    site.lmTimeout().Minutes(),
		Phases:     site.lmBatteryPhases(),
		PeakFreeze: site.peakFreeze().Minutes(),
		PeakCap:    site.peakCap(),
	})
}

// lmTimeout is how long an unserved demand keeps reserving headroom
func (site *Site) lmTimeout() time.Duration {
	if v := site.advanced().Timeout; v != nil {
		return time.Duration(*v) * time.Minute
	}
	if d := site.LoadManagement.Timeout; d > 0 {
		return d
	}
	return lm.DefaultTimeout
}

// SetLmAdvanced sets one of the advanced settings
func (site *Site) SetLmAdvanced(name string, value float64) error {
	limit, ok := lmAdvancedLimits[name]
	if !ok {
		return fmt.Errorf("unknown setting: %s", name)
	}
	if value < limit.min || value > limit.max || limit.integer && value != float64(int(value)) {
		return fmt.Errorf("%s must be between %g and %g", name, limit.min, limit.max)
	}
	if name == "phases" && value == 2 {
		return fmt.Errorf("phases must be 1 or 3")
	}

	s := site.lms()

	s.advMu.Lock()
	v := value
	switch name {
	case "hysteresis":
		s.adv.Hysteresis = &v
	case "freeValue":
		s.adv.FreeValue = &v
	case "holdOff":
		s.adv.HoldOff = &v
	case "timeout":
		s.adv.Timeout = &v
	case "phases":
		s.adv.Phases = &v
	case "peakFreeze":
		s.adv.PeakFreeze = &v
	case "peakCap":
		s.adv.PeakCap = &v
	}
	adv := s.adv
	s.advMu.Unlock()

	site.log.DEBUG.Printf("set load management %s: %g", name, value)

	if err := settings.SetJson(keys.LmAdvanced, adv); err != nil {
		return err
	}

	if name == "timeout" {
		lm.SetTimeout(site.lmTimeout())
	}

	site.publishLmAdvanced()

	return nil
}
