package core

// Custom extension: battery profiles, see core/lm/profile. Set up under
// Lastmanagement-Details → Profile, picked on the battery page. Applying a
// profile goes through the same setters as the ui, so every value is checked
// and published as if it had been set by hand.

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm/profile"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
)

// lmMaxProfiles keeps the battery page's selection manageable
const lmMaxProfiles = 10

// lmProfiles returns the stored profiles
func lmProfiles() []profile.Profile {
	var res []profile.Profile
	_ = settings.Json(keys.LmProfiles, &res)
	return res
}

func (site *Site) publishLmProfiles() {
	profiles := lmProfiles()
	if profiles == nil {
		profiles = []profile.Profile{}
	}

	active, _ := settings.String(keys.LmProfileActive)

	site.publish(keys.LmProfiles, profiles)
	site.publish(keys.LmProfileActive, active)
}

// SaveLmProfile creates a profile, or updates the one with the same id
func (site *Site) SaveLmProfile(p profile.Profile) (profile.Profile, error) {
	p.Name = strings.TrimSpace(p.Name)

	if err := p.Validate(); err != nil {
		return p, err
	}

	for name := range p.SolarShare {
		if err := lmCheckWallbox(name); err != nil {
			return p, err
		}
	}

	profiles := lmProfiles()

	if p.ID == "" {
		if len(profiles) >= lmMaxProfiles {
			return p, fmt.Errorf("at most %d profiles", lmMaxProfiles)
		}
		p.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
		profiles = append(profiles, p)
	} else {
		i := slices.IndexFunc(profiles, func(e profile.Profile) bool { return e.ID == p.ID })
		if i < 0 {
			return p, fmt.Errorf("unknown profile: %s", p.ID)
		}
		profiles[i] = p
	}

	if err := settings.SetJson(keys.LmProfiles, profiles); err != nil {
		return p, err
	}

	site.log.DEBUG.Printf("save profile: %s", p.Name)
	site.publishLmProfiles()

	return p, nil
}

// DeleteLmProfile deletes a profile
func (site *Site) DeleteLmProfile(id string) error {
	profiles := lmProfiles()

	i := slices.IndexFunc(profiles, func(e profile.Profile) bool { return e.ID == id })
	if i < 0 {
		return fmt.Errorf("unknown profile: %s", id)
	}

	site.log.DEBUG.Printf("delete profile: %s", profiles[i].Name)

	if err := settings.SetJson(keys.LmProfiles, slices.Delete(profiles, i, i+1)); err != nil {
		return err
	}

	if active, _ := settings.String(keys.LmProfileActive); active == id {
		settings.SetString(keys.LmProfileActive, "")
	}

	site.publishLmProfiles()

	return nil
}

// ApplyLmProfile applies a profile. It goes on with the other values when one
// fails and reports all failures; the profile only counts as active when every
// value was applied.
func (site *Site) ApplyLmProfile(id string) error {
	profiles := lmProfiles()

	i := slices.IndexFunc(profiles, func(e profile.Profile) bool { return e.ID == id })
	if i < 0 {
		return fmt.Errorf("unknown profile: %s", id)
	}
	p := profiles[i]

	site.log.INFO.Printf("apply profile: %s", p.Name)

	var errs []error
	add := func(label string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
		}
	}

	site.applyProfileGridCharge(p, add)
	site.applyProfileBatteryUsage(p, add)

	if v := p.DischargeControl; v != nil {
		add("discharge control", site.SetBatteryDischargeControl(*v))
	}

	if v := p.PeakLimit; v != nil {
		add("peak limit", site.SetPeakShavingLimit(*v))
	}
	if v := p.PeakReserve; v != nil {
		add("peak reserve", site.SetPeakShavingReserve(*v))
	}
	if v := p.PeakShaving; v != nil {
		add("peak shaving", site.SetPeakShaving(*v))
	}

	for name, share := range p.SolarShare {
		add("solar share "+name, lmSetSolarShare(name, share))
	}

	if len(errs) > 0 {
		err := errors.Join(errs...)
		site.log.ERROR.Printf("profile %s: %v", p.Name, err)
		return err
	}

	settings.SetString(keys.LmProfileActive, id)
	site.publish(keys.LmProfileActive, id)

	return nil
}

// applyProfileGridCharge sets start and stop soc in the order that keeps start
// below stop on the way, then the switch
func (site *Site) applyProfileGridCharge(p profile.Profile, add func(string, error)) {
	if p.GridChargeStart != nil || p.GridChargeStop != nil {
		currentStop := site.GetBatterySocGridChargeStop()
		start := ptrValue(p.GridChargeStart, site.GetBatterySocGridChargeStart())
		stop := ptrValue(p.GridChargeStop, currentStop)

		switch {
		case start >= stop:
			add("grid charge", fmt.Errorf("start soc %.0f must be below stop soc %.0f", start, stop))
		case start < currentStop:
			add("grid charge start", site.SetBatterySocGridChargeStart(start))
			add("grid charge stop", site.SetBatterySocGridChargeStop(stop))
		default:
			add("grid charge stop", site.SetBatterySocGridChargeStop(stop))
			add("grid charge start", site.SetBatterySocGridChargeStart(start))
		}
	}

	if v := p.GridCharge; v != nil {
		add("grid charge", site.SetBatterySocGridCharge(*v))
	}
}

// applyProfileBatteryUsage sets priority, buffer and buffer start soc. evcc checks
// each against the other two, so raising all three one by one would fail on the
// way: the combination is checked first, then the buffers are cleared and the
// values set bottom up.
func (site *Site) applyProfileBatteryUsage(p profile.Profile, add func(string, error)) {
	if p.PrioritySoc == nil && p.BufferSoc == nil && p.BufferStartSoc == nil {
		return
	}

	priority := ptrValue(p.PrioritySoc, site.GetPrioritySoc())
	buffer := ptrValue(p.BufferSoc, site.GetBufferSoc())
	bufferStart := ptrValue(p.BufferStartSoc, site.GetBufferStartSoc())

	if err := profile.CheckBatteryUsage(priority, buffer, bufferStart); err != nil {
		add("battery usage", err)
		return
	}

	for _, step := range []struct {
		label string
		set   func(float64) error
		value float64
	}{
		{"buffer start soc", site.SetBufferStartSoc, 0},
		{"buffer soc", site.SetBufferSoc, 0},
		{"priority soc", site.SetPrioritySoc, priority},
		{"buffer soc", site.SetBufferSoc, buffer},
		{"buffer start soc", site.SetBufferStartSoc, bufferStart},
	} {
		if err := step.set(step.value); err != nil {
			add(step.label, err)
			return
		}
	}
}

// lmCheckWallbox checks that a loadpoint exists and is not a heating device
func lmCheckWallbox(name string) error {
	dev, err := config.Loadpoints().ByName(name)
	if err != nil {
		return fmt.Errorf("unknown loadpoint: %s", name)
	}

	if lp, ok := dev.Instance().(*Loadpoint); ok && lp.chargerHasFeature(api.Heating) {
		return fmt.Errorf("%s is a heating device", name)
	}

	return nil
}

// lmSetSolarShare sets a wallbox's solar share, given in %
func lmSetSolarShare(name string, percent float64) error {
	if err := lmCheckWallbox(name); err != nil {
		return err
	}

	dev, _ := config.Loadpoints().ByName(name)
	dev.Instance().SetSolarShare(percent / 100)

	return nil
}

// lmProfileWallbox is a wallbox as offered in the profile editor
type lmProfileWallbox struct {
	Name       string  `json:"name"`
	Title      string  `json:"title"`
	SolarShare float64 `json:"solarShare"` // current value in %
}

// publishLmWallboxes publishes the loadpoints a profile can set the solar share
// of: all but heating devices, with their current value. Every cycle, the
// value can be changed on the loadpoint any time.
func (site *Site) publishLmWallboxes() {
	res := make([]lmProfileWallbox, 0)

	for _, dev := range config.Loadpoints().Devices() {
		lp, ok := dev.Instance().(*Loadpoint)
		if !ok || lp.chargerHasFeature(api.Heating) {
			continue
		}

		res = append(res, lmProfileWallbox{
			Name:       dev.Config().Name,
			Title:      lp.GetTitle(),
			SolarShare: math.Round(lp.GetSolarShare() * 100),
		})
	}

	site.publish(keys.LmProfileWallboxes, res)
}

func ptrValue(v *float64, def float64) float64 {
	if v == nil {
		return def
	}
	return *v
}
