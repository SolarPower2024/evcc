package core

// Custom extension: the advanced load management settings, set in the ui under
// Lastmanagement-Details → Erweitert. Each one overrides the default. Unset values are not stored, so a default
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
	HoldOff    *float64 `json:"holdOff,omitempty"`    // battery grid charge hold-off in minutes
	Timeout    *float64 `json:"timeout,omitempty"`    // unserved demand expiry in minutes
	Phases     *float64 `json:"phases,omitempty"`     // battery phases for current accounting
	PeakFreeze *float64 `json:"peakFreeze,omitempty"` // minute of the window from which the peak budget no longer grows
	// hours the optimizer may take to reach the stop soc of soc-based grid charging
	GridChargeWindow *float64 `json:"gridChargeWindow,omitempty"`
	// home consumption forecast: 0 evcc, 1 per weekday, 2 from the uploaded load
	// profile, see site_load_manual.go
	HomeForecast *float64 `json:"homeForecast,omitempty"`
	// Home Assistant entity the optimizer's planned grid export is written to, empty = off,
	// see site_lm_export_forecast.go
	ExportForecastEntity string `json:"exportForecastEntity,omitempty"`
}

// home consumption forecasts, see HomeForecast
const (
	homeForecastEvcc = iota
	homeForecastWeekday
	homeForecastManual
)

// lmAdvancedState is what the ui shows: the values in effect
type lmAdvancedState struct {
	HoldOff              float64 `json:"holdOff"`
	Timeout              float64 `json:"timeout"`
	Phases               int     `json:"phases"`
	PeakFreeze           float64 `json:"peakFreeze"`
	GridChargeWindow     float64 `json:"gridChargeWindow"`
	HomeForecast         int     `json:"homeForecast"`
	ExportForecastEntity string  `json:"exportForecastEntity"`
	// PhasesMissing: the circuit has a current limit and the battery phases are not set, grid charging stays off
	PhasesMissing bool `json:"phasesMissing"`
}

// lmAdvancedLimit is a setting's valid range
type lmAdvancedLimit struct {
	min, max float64
	integer  bool
}

var lmAdvancedLimits = map[string]lmAdvancedLimit{
	"holdOff":          {1, 60, true},
	"timeout":          {1, 60, true},
	"phases":           {1, 3, true},
	"peakFreeze":       {1, 14, true},
	"gridChargeWindow": {1, 24, true},
	"homeForecast":     {0, 2, true},
}

// restoreLmAdvanced restores the persisted advanced settings
func (site *Site) restoreLmAdvanced() {
	s := site.lms()

	if adv, ok := restoreLmAdvancedValue(site.log); ok {
		if dropInvalidLmAdvanced(site.log, &adv) {
			_ = settings.SetJson(keys.LmAdvanced, adv)
		}
		s.advMu.Lock()
		s.adv = adv
		s.advMu.Unlock()
	}

	site.lmm().SetTimeout(site.lmTimeout())
	site.lmm().SetFollowCycles(site.lmFollowCycles)

	site.publishLmAdvanced()
	site.publishLmHomeProfile()
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
		HoldOff:              site.lmHoldOff().Minutes(),
		Timeout:              site.lmTimeout().Minutes(),
		Phases:               site.lmBatteryPhases(),
		PeakFreeze:           site.peakFreeze().Minutes(),
		GridChargeWindow:     site.gridChargeWindow().Hours(),
		HomeForecast:         site.homeForecast(),
		ExportForecastEntity: site.exportForecastEntity(),
		PhasesMissing:        site.batteryPhasesMissing(),
	})
}

// homeForecast is the home consumption forecast in effect, see HomeForecast
func (site *Site) homeForecast() int {
	adv := site.advanced()
	if v := adv.HomeForecast; v != nil {
		return int(*v)
	}
	return homeForecastEvcc
}

// lmTimeout is how long an unserved demand keeps reserving headroom
func (site *Site) lmTimeout() time.Duration {
	if v := site.advanced().Timeout; v != nil {
		return time.Duration(*v) * time.Minute
	}
	return lm.DefaultTimeout
}

// validLmAdvanced checks the value of one advanced setting
func validLmAdvanced(name string, value float64) error {
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
	return nil
}

// SetLmAdvanced sets one of the advanced settings
func (site *Site) SetLmAdvanced(name string, value float64) error {
	if err := validLmAdvanced(name, value); err != nil {
		return err
	}

	s := site.lms()

	s.advMu.Lock()
	v := value
	switch name {
	case "holdOff":
		s.adv.HoldOff = &v
	case "timeout":
		s.adv.Timeout = &v
	case "phases":
		s.adv.Phases = &v
	case "peakFreeze":
		s.adv.PeakFreeze = &v
	case "gridChargeWindow":
		s.adv.GridChargeWindow = &v
	case "homeForecast":
		s.adv.HomeForecast = &v
	}
	adv := s.adv
	s.advMu.Unlock()

	site.log.DEBUG.Printf("set load management %s: %g", name, value)

	if err := settings.SetJson(keys.LmAdvanced, adv); err != nil {
		return err
	}

	if name == "timeout" {
		site.lmm().SetTimeout(site.lmTimeout())
	}
	if name == "homeForecast" {
		site.Optimize() // the home demand forecast changed
	}

	site.publishLmAdvanced()

	return nil
}
