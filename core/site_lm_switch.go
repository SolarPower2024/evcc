package core

// Custom extension: runtime change to a circuit's power limit without touching
// the configuration: the peak limit as the circuit's limit (Erweitert → Grenze =
// Peak-Shaving-Grenze). While peak shaving is on, the load management (peak)
// circuit's limit is the peak limit, also below the configured value. Needs the
// circuit chosen; the configured value applies again once peak shaving is off.
// Otherwise the circuits limit with their configured value, as in evcc.
//
// The load management (peak) circuit is chosen under Lastmanagement-Details →
// Erweitert, so a circuit for the fuse can sit beside it untouched.
//
// The current limits (fuses) and a HEMS consumption limit (§14a) always apply.
// The configured value is kept when a circuit is first changed and put back
// once the change is not needed any more. Circuits whose limit comes from a plugin
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
	Limits  map[string]float64 `json:"limits"`  // W, the configured power limits of the changed circuits
	Dynamic []string           `json:"dynamic"` // circuits whose limit comes from a plugin and stays
	Circuit string             `json:"circuit"` // the load management (peak) circuit, empty = all
	// FromPeak: the circuit's limit is the peak limit right now, see circuitFromPeak
	FromPeak bool `json:"fromPeak"`
}

// restoreLmSwitch brings the load management (peak) circuit back after a restart
func (site *Site) restoreLmSwitch() {
	s := site.lms()

	circuit, err := settings.String(keys.LmCircuit)
	if err != nil {
		// lm3/lm4 stored it under this key
		if circuit, err = settings.String(keys.PeakFollowCircuit); err == nil && circuit != "" {
			settings.SetString(keys.LmCircuit, circuit)
		}
	}
	site.dropPeakFollowCircuit()

	s.mu.Lock()
	s.circuit = circuit
	s.mu.Unlock()

	site.dropLmOff()

	site.applyCircuitLimits()
}

// circuitLimitWanted returns the power limit a circuit should have for its
// configured value, 0 = unlimited.
func circuitLimitWanted(configured float64, fromPeak bool, peakLimit float64) float64 {
	if fromPeak {
		return peakLimit
	}
	return configured
}

// applyCircuitLimits sets the circuits' power limits, once per cycle and on
// every change
func (site *Site) applyCircuitLimits() {
	p := site.peak()
	p.mu.Lock()
	peakLimit := p.limit
	fromPeak := site.advanced().CircuitFromPeak && p.enabled && p.limit > 0
	p.mu.Unlock()

	s := site.lms()

	s.mu.Lock()
	lmCircuit := s.circuit

	// a chosen circuit that no longer exists: it takes no peak limit,
	// see dropOrphanCircuits
	orphan := lmCircuit != "" && !circuitExists(lmCircuit)
	peakCircuit := ""
	if fromPeak && !orphan {
		peakCircuit = lmCircuit
	}
	var peakConfigured float64
	if s.offLimits == nil {
		s.offLimits = make(map[string]float64)
	}
	changed := !s.offPublished
	s.offPublished = true

	exists := make(map[string]bool)
	for _, dev := range config.Circuits().Devices() {
		name, c := dev.Config().Name, dev.Instance()
		exists[name] = true

		configured, touched := s.offLimits[name]
		if !touched {
			configured = c.GetMaxPower()
		}

		wanted := circuitLimitWanted(configured, name == peakCircuit, peakLimit)
		if name == peakCircuit {
			peakConfigured = configured
		}

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

	// a circuit that is gone takes its entries with it
	for name := range s.offLimits {
		if !exists[name] {
			delete(s.offLimits, name)
			changed = true
		}
	}
	for name := range s.offDynamic {
		if !exists[name] {
			delete(s.offDynamic, name)
			changed = true
		}
	}

	if peakCircuit == "" && len(s.offDynamic) > 0 {
		s.offDynamic = nil
		changed = true
	}

	// a circuit without a power limit is left alone, one with a plugin limit stays
	fromPeakNow := peakCircuit != "" && peakConfigured > 0 && !s.offDynamic[peakCircuit]
	if fromPeakNow != s.offFromPeak {
		s.offFromPeak = fromPeakNow
		changed = true
	}

	res := lmSwitchState{Limits: make(map[string]float64, len(s.offLimits)), Dynamic: make([]string, 0), Circuit: s.circuit, FromPeak: fromPeakNow}
	for name, v := range s.offLimits {
		res.Limits[name] = v
	}
	for name := range s.offDynamic {
		res.Dynamic = append(res.Dynamic, name)
	}
	slices.Sort(res.Dynamic)
	s.mu.Unlock()

	if changed {
		// the circuits are published by the cycle only, see updateCircuits
		site.publish(keys.LmOff, res)
		site.Optimize() // custom: the optimizer's import limit may change, see core/site_optimizer_lm.go
	}
}

func limitText(w float64) string {
	if w <= 0 {
		return "lifted"
	}
	return fmt.Sprintf("%.1f kW", w/1e3)
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
