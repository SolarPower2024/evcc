package core

// Custom extension: follow the peak. A capacity tariff bills the month's highest
// quarter hour, so once the month has a higher peak than the limit, shaving
// below it saves nothing and only drains the battery. With follow the peak on,
// the peak limit rises to the month's peak minus a buffer and drops back to the
// limit set by hand (the base) when a new month starts. Set up under
// Lastmanagement-Details → Peak Shaving.

import (
	"fmt"
	"math"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
)

const (
	defaultPeakFollowBuffer = 500.0  // W below the month's peak
	maxPeakFollowBuffer     = 5000.0 // W
	peakFollowStep          = 100.0  // W, the raised limit is rounded down to it
)

// peakFollowState is what the ui shows
type peakFollowState struct {
	Enabled bool    `json:"enabled"`
	Buffer  float64 `json:"buffer"`  // W
	Base    float64 `json:"base"`    // W, the limit set by hand
	Circuit string  `json:"circuit"` // circuit whose power limit rises along, empty = none
}

// restorePeakFollow restores follow the peak after the peak settings
func (site *Site) restorePeakFollow() {
	s := site.peak()

	s.mu.Lock()
	s.followBuffer = defaultPeakFollowBuffer
	if v, err := settings.Float(keys.PeakFollowBuffer); err == nil {
		s.followBuffer = v
	}
	if v, err := settings.Bool(keys.PeakFollow); err == nil {
		s.follow = v
	}
	s.followBase = s.limit
	if v, err := settings.Float(keys.PeakFollowBase); err == nil && v > 0 {
		s.followBase = v
	}
	if v, err := settings.String(keys.PeakFollowCircuit); err == nil {
		s.followCircuit = v
	}
	s.mu.Unlock()

	site.updatePeakFollow()
	site.publishPeakFollow()
}

func (site *Site) publishPeakFollow() {
	s := site.peak()

	s.mu.Lock()
	res := peakFollowState{Enabled: s.follow, Buffer: s.followBuffer, Base: s.followBase, Circuit: s.followCircuit}
	s.mu.Unlock()

	site.publish(keys.PeakFollow, res)
}

// peakFollowLimit is the limit for a base, the month's peak and a buffer
func peakFollowLimit(base, monthPeak, buffer float64) float64 {
	raised := math.Floor((monthPeak-buffer)/peakFollowStep) * peakFollowStep
	return max(base, min(raised, maxPeakLimit))
}

// updatePeakFollow sets the limit from the month's peak, once per cycle
func (site *Site) updatePeakFollow() {
	s := site.peak()

	s.mu.Lock()
	if !s.follow {
		s.mu.Unlock()
		return
	}

	var monthPeak float64
	if len(s.months) > 0 && s.months[0].Month == s.clock.Now().Local().Format("2006-01") {
		monthPeak = s.months[0].Peak
	}

	limit := peakFollowLimit(s.followBase, monthPeak, s.followBuffer)
	previous := s.limit
	s.limit = limit
	s.mu.Unlock()

	if limit == previous {
		return
	}

	if limit > previous {
		site.log.INFO.Printf("follow the peak: limit %.1f kW (month's peak %.1f kW)", limit/1e3, monthPeak/1e3)
	} else {
		site.log.INFO.Printf("follow the peak: limit back to %.1f kW", limit/1e3)
	}

	settings.SetFloat(keys.PeakShavingLimit, limit)
	site.publish(keys.PeakShavingLimit, limit)
}

// peakFollowSetBase takes a limit set by hand as the base while following,
// reporting whether it did
func (site *Site) peakFollowSetBase(limit float64) bool {
	s := site.peak()

	s.mu.Lock()
	follow := s.follow
	if follow {
		s.followBase = limit
	}
	s.mu.Unlock()

	if !follow {
		return false
	}

	settings.SetFloat(keys.PeakFollowBase, limit)
	site.updatePeakFollow()
	site.publishPeakFollow()

	return true
}

func (site *Site) GetPeakFollow() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.follow
}

// SetPeakFollow switches follow the peak. On, the current limit becomes the
// base; off, the limit returns to it.
func (site *Site) SetPeakFollow(val bool) error {
	s := site.peak()

	s.mu.Lock()
	if s.follow == val {
		s.mu.Unlock()
		return nil
	}
	s.follow = val
	if val {
		s.followBase = s.limit
	}
	base := s.followBase
	s.mu.Unlock()

	site.log.DEBUG.Println("set follow the peak:", val)
	settings.SetBool(keys.PeakFollow, val)
	settings.SetFloat(keys.PeakFollowBase, base)

	if val {
		site.updatePeakFollow()
	} else {
		s.mu.Lock()
		changed := s.limit != base
		s.limit = base
		s.mu.Unlock()

		if changed {
			site.log.INFO.Printf("follow the peak: off, limit back to %.1f kW", base/1e3)
			settings.SetFloat(keys.PeakShavingLimit, base)
			site.publish(keys.PeakShavingLimit, base)
		}
	}

	site.applyCircuitLimits()
	site.publishPeakFollow()

	return nil
}

func (site *Site) GetPeakFollowBuffer() float64 {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.followBuffer
}

// SetPeakFollowBuffer sets how far below the month's peak the limit stays
func (site *Site) SetPeakFollowBuffer(buffer float64) error {
	if buffer < 0 || buffer > maxPeakFollowBuffer || math.Mod(buffer, peakFollowStep) != 0 {
		return fmt.Errorf("buffer must be between 0 and %.0f W in steps of %.0f W", maxPeakFollowBuffer, peakFollowStep)
	}

	s := site.peak()

	s.mu.Lock()
	s.followBuffer = buffer
	s.mu.Unlock()

	site.log.DEBUG.Println("set follow the peak buffer:", buffer)
	settings.SetFloat(keys.PeakFollowBuffer, buffer)

	site.updatePeakFollow()
	site.applyCircuitLimits()
	site.publishPeakFollow()

	return nil
}

func (site *Site) GetPeakFollowCircuit() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.followCircuit
}

// SetPeakFollowCircuit sets the circuit whose power limit rises with the
// raised peak limit, empty = none
func (site *Site) SetPeakFollowCircuit(name string) error {
	if name != "" {
		if _, err := config.Circuits().ByName(name); err != nil {
			return fmt.Errorf("circuit %s: %w", name, err)
		}
	}

	s := site.peak()

	s.mu.Lock()
	s.followCircuit = name
	s.mu.Unlock()

	site.log.DEBUG.Println("set follow the peak circuit:", name)
	settings.SetString(keys.PeakFollowCircuit, name)

	site.applyCircuitLimits()
	site.publishPeakFollow()

	return nil
}
