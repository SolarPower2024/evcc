package profile

// Package profile holds battery profiles: named sets of settings, e.g. summer and winter, applied with
// one click on the battery page. A value left out (nil) is not touched when the
// profile is applied. The profiles are set up in the ui under
// Lastmanagement-Details → Profile, see core/site_lm_profiles.go. It has no
// dependencies, so the site api can use it without an import cycle.

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

// ProfileIcons are the icons a profile can show
var ProfileIcons = []string{"sun", "cloudsun", "snow", "umbrella", "home", "eco", "moon", "flower"}

// Profile is a named set of battery, peak shaving and wallbox settings
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`

	// grid charging
	GridCharge      *bool    `json:"gridCharge,omitempty"`
	GridChargeStart *float64 `json:"gridChargeStart,omitempty"` // soc %
	GridChargeStop  *float64 `json:"gridChargeStop,omitempty"`  // soc %

	// battery usage
	PrioritySoc      *float64 `json:"prioritySoc,omitempty"`      // surplus goes to the battery first up to %
	BufferSoc        *float64 `json:"bufferSoc,omitempty"`        // battery supports charging from %, 0 = off
	BufferStartSoc   *float64 `json:"bufferStartSoc,omitempty"`   // charging may start from %, 0 = off
	DischargeControl *bool    `json:"dischargeControl,omitempty"` // no discharge in fast and planned charging

	// peak shaving
	PeakShaving *bool    `json:"peakShaving,omitempty"`
	PeakReserve *float64 `json:"peakReserve,omitempty"` // soc %
	PeakLimit   *float64 `json:"peakLimit,omitempty"`   // W

	// wallboxes: solar share in %, by loadpoint name
	SolarShare map[string]float64 `json:"solarShare,omitempty"`
}

// maxProfileName is the longest name, it has to fit a button
const maxProfileName = 24

// Validate checks the profile on its own. Whether the battery usage values fit
// the ones it leaves out can only be checked when it is applied.
func (p Profile) Validate() error {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	name := strings.TrimSpace(p.Name)
	if name == "" {
		fail("name missing")
	}
	if utf8.RuneCountInString(name) > maxProfileName {
		fail("name longer than %d characters", maxProfileName)
	}

	if p.Icon != "" && !slices.Contains(ProfileIcons, p.Icon) {
		fail("unknown icon: %s", p.Icon)
	}

	soc := func(label string, v *float64, lo, hi float64) {
		if v != nil && (*v < lo || *v > hi || math.IsNaN(*v)) {
			fail("%s must be between %g and %g %%", label, lo, hi)
		}
	}

	soc("grid charge start", p.GridChargeStart, 0, 100)
	soc("grid charge stop", p.GridChargeStop, 0, 100)
	if p.GridChargeStart != nil && p.GridChargeStop != nil && *p.GridChargeStart >= *p.GridChargeStop {
		fail("grid charge start must be below stop")
	}

	soc("priority soc", p.PrioritySoc, 0, 100)
	soc("buffer soc", p.BufferSoc, 0, 100)
	soc("buffer start soc", p.BufferStartSoc, 0, 100)
	if err := CheckBatteryUsage(ptrOr(p.PrioritySoc, 0), ptrOr(p.BufferSoc, 0), ptrOr(p.BufferStartSoc, 0)); err != nil {
		errs = append(errs, err)
	}

	soc("peak reserve", p.PeakReserve, 1, 99)
	if v := p.PeakLimit; v != nil && (*v < 2000 || *v > 20000 || math.Mod(*v, 500) != 0) {
		fail("peak limit must be between 2000 and 20000 W in 500 W steps")
	}

	for lp, v := range p.SolarShare {
		if v < 0 || v > 100 {
			fail("solar share of %s must be between 0 and 100 %%", lp)
		}
	}

	return errors.Join(errs...)
}

// CheckBatteryUsage checks the order evcc's setters require: priority soc <=
// buffer soc <= buffer start soc, where 0 turns buffer and buffer start off
func CheckBatteryUsage(priority, buffer, bufferStart float64) error {
	if buffer != 0 && priority > buffer {
		return errors.New("priority soc must not be above the buffer soc")
	}
	if bufferStart != 0 && buffer > bufferStart {
		return errors.New("buffer soc must not be above the buffer start soc")
	}
	return nil
}

func ptrOr(v *float64, def float64) float64 {
	if v == nil {
		return def
	}
	return *v
}
