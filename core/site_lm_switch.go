package core

// Custom extension: runtime changes to the circuits' power limits, from two
// sources, without touching the configuration:
//
//   - load management off (Mehr → Lastmanagement (Peak)): the power limit of
//     the load management (peak) circuit is lifted, so wallboxes, heaters and
//     the battery's grid charging are no longer throttled or shed for it.
//     Without that circuit chosen, the power limits of all circuits.
//   - follow the peak: the load management (peak) circuit's limit rises with
//     the raised peak limit, never below its configured value, see
//     site_peak_follow.go
//
// The load management (peak) circuit is chosen under Lastmanagement-Details →
// Erweitert, so a circuit for the fuse can sit beside it untouched.
//
// The current limits (fuses) and a HEMS consumption limit (§14a) always apply.
// The configured value is kept when a circuit is first changed and put back
// once neither source needs a change. Circuits whose limit comes from a plugin
// are left alone. Battery peak shaving is separate and keeps running.

import (
	"fmt"
	"slices"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
)

// lmSwitchState is what the ui shows
type lmSwitchState struct {
	Enabled bool               `json:"enabled"`
	Limits  map[string]float64 `json:"limits"`  // W, the configured power limits of the changed circuits
	Dynamic []string           `json:"dynamic"` // circuits whose limit comes from a plugin and stays
	Circuit string             `json:"circuit"` // the load management (peak) circuit, empty = all
}

// restoreLmSwitch continues switched off load management across a restart
func (site *Site) restoreLmSwitch() {
	s := site.lms()

	circuit, err := settings.String(keys.LmCircuit)
	if err != nil {
		// lm3/lm4 chose it as the circuit following the peak
		if circuit, err = settings.String(keys.PeakFollowCircuit); err == nil && circuit != "" {
			settings.SetString(keys.LmCircuit, circuit)
		}
		_ = settings.Delete(keys.PeakFollowCircuit)
	}

	s.mu.Lock()
	s.circuit = circuit
	if v, err := settings.Bool(keys.LmOff); err == nil && v {
		s.off = true
	}
	s.mu.Unlock()

	site.applyCircuitLimits()
}

// circuitLimitWanted returns the power limit a circuit should have for its
// configured value, 0 = unlimited
func circuitLimitWanted(configured float64, off, follow bool, followLimit float64) float64 {
	switch {
	case off:
		return 0
	case follow:
		return max(configured, followLimit)
	default:
		return configured
	}
}

// applyCircuitLimits sets the circuits' power limits, once per cycle and on
// every change
func (site *Site) applyCircuitLimits() {
	p := site.peak()
	p.mu.Lock()
	following := p.follow && p.limit > p.followBase
	followLimit := p.limit
	p.mu.Unlock()

	s := site.lms()

	s.mu.Lock()
	off, lmCircuit := s.off, s.circuit

	// a chosen circuit that no longer exists: all circuits, nothing follows
	if lmCircuit != "" {
		if _, err := config.Circuits().ByName(lmCircuit); err != nil {
			lmCircuit = ""
		}
	}
	followCircuit := ""
	if following {
		followCircuit = lmCircuit
	}
	if s.offLimits == nil {
		s.offLimits = make(map[string]float64)
	}
	changed := !s.offPublished
	s.offPublished = true

	for _, dev := range config.Circuits().Devices() {
		name, c := dev.Config().Name, dev.Instance()

		configured, touched := s.offLimits[name]
		if !touched {
			configured = c.GetMaxPower()
		}

		lift := off && (lmCircuit == "" || name == lmCircuit)
		wanted := circuitLimitWanted(configured, lift, name == followCircuit, followLimit)

		if !touched {
			if wanted == configured || configured <= 0 || s.offDynamic[name] {
				continue
			}

			c.SetMaxPower(wanted)
			if c.GetMaxPower() != wanted {
				// the limit comes from a plugin: it stays
				c.SetMaxPower(configured)
				if s.offDynamic == nil {
					s.offDynamic = make(map[string]bool)
				}
				s.offDynamic[name] = true
				site.log.WARN.Printf("circuit %s: dynamic power limit, not changed", name)
				changed = true
				continue
			}

			s.offLimits[name] = configured
			site.log.INFO.Printf("circuit %s: power limit %s (configured %.1f kW)", name, limitText(wanted), configured/1e3)
			changed = true
			continue
		}

		if c.GetMaxPower() == wanted {
			continue
		}

		c.SetMaxPower(wanted)
		if wanted == configured {
			delete(s.offLimits, name)
			site.log.INFO.Printf("circuit %s: power limit back to %.1f kW", name, configured/1e3)
		} else {
			site.log.INFO.Printf("circuit %s: power limit %s (configured %.1f kW)", name, limitText(wanted), configured/1e3)
		}
		changed = true
	}

	if !off && followCircuit == "" && len(s.offDynamic) > 0 {
		s.offDynamic = nil
		changed = true
	}

	res := lmSwitchState{Enabled: !off, Limits: make(map[string]float64, len(s.offLimits)), Dynamic: make([]string, 0), Circuit: s.circuit}
	for name, v := range s.offLimits {
		res.Limits[name] = v
	}
	for name := range s.offDynamic {
		res.Dynamic = append(res.Dynamic, name)
	}
	slices.Sort(res.Dynamic)
	s.mu.Unlock()

	if changed {
		site.publish(keys.LmOff, res)
		site.publishCircuits()
		site.Optimize() // custom: the optimizer's import limit may change, see core/site_optimizer_lm.go
	}
}

func limitText(w float64) string {
	if w <= 0 {
		return "lifted"
	}
	return fmt.Sprintf("%.1f kW", w/1e3)
}

// GetLmEnabled reports whether load management limits the circuits' power
func (site *Site) GetLmEnabled() bool {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	return !s.off
}

// SetLmEnabled switches load management on or off
func (site *Site) SetLmEnabled(enabled bool) error {
	s := site.lms()

	s.mu.Lock()
	changed := s.off == enabled
	s.off = !enabled
	s.offPublished = false
	s.mu.Unlock()

	if changed {
		site.log.INFO.Println("load management:", map[bool]string{true: "on", false: "off"}[enabled])
		settings.SetBool(keys.LmOff, !enabled)
	}

	site.applyCircuitLimits()

	return nil
}

// GetLmCircuit returns the load management (peak) circuit, empty = all
func (site *Site) GetLmCircuit() string {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.circuit
}

// SetLmCircuit sets the load management (peak) circuit, empty = all
func (site *Site) SetLmCircuit(name string) error {
	if name != "" {
		if _, err := config.Circuits().ByName(name); err != nil {
			return fmt.Errorf("circuit %s: %w", name, err)
		}
	}

	s := site.lms()

	s.mu.Lock()
	s.circuit = name
	s.offPublished = false
	s.mu.Unlock()

	site.log.DEBUG.Println("set load management circuit:", name)
	settings.SetString(keys.LmCircuit, name)

	site.applyCircuitLimits()

	return nil
}
