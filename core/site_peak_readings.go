package core

// Custom extension: peak shaving and meter values evcc does not report as a failure.
//
// evcc reports a meter that fails with an error: the cycle then never reaches peak
// shaving, and peakCheckMeters hands the battery back after peak.MaxGap. Three kinds
// of outage come without an error and would keep the setpoint running on old values:
//
//   - the grid meter has no power (Home Assistant sensor unavailable or unknown, a
//     device answering "not available"): evcc keeps the last grid power, see
//     setPeakGridEnergy;
//   - the grid power is a real number that stands still, e.g. a template sensor or
//     an mqtt topic that does not expire: the battery moves, the grid power does not;
//   - the battery power cannot be read: evcc leaves it at 0 and the setpoint, which adds
//     the battery power back, jumps between the value with and without it, see
//     setPeakMeterRead.
//
// All three count as a meter outage: the cycle keeps the limit, and once peak.MaxGap
// has passed without good values peakCheckMeters ends the reserve and grid
// charging pauses, as for a meter failing with an error.

import (
	"fmt"
	"math"
	"time"

	"github.com/evcc-io/evcc/core/peak"
)

// peakFrozenBatteryDelta is how far in W the battery power has to move while the
// grid power stands still for peak.MaxGap, to count the standstill as an outage:
// a battery that follows the demand shifts the grid power, so a grid power that
// stays exactly the same cannot be a reading
const peakFrozenBatteryDelta = 500.0

// Why the meter values of a cycle are not used
const (
	outageGridUnread    = "the grid meter delivered no power"
	outageGridFrozen    = "the grid power stands still while the battery power moves"
	outageBatteryUnread = "the battery power could not be read"
)

// peakReadings is what peak shaving knows about the meter values of the cycle
type peakReadings struct {
	gridUnread    bool         // the grid meter had no power this cycle, set by setPeakGridEnergy
	batteryFailed map[int]bool // by battery meter: its power could not be read, set by setPeakMeterRead

	gridBits   uint64    // the grid power the standstill is counted for
	gridSince  time.Time // it has had this value since
	batteryMin float64   // the range of the battery power since
	batteryMax float64

	lost string // why the values were not used in the last cycle, empty = they were
}

// outage returns why this cycle's meter values must not be used, empty if they
// can, and a detail for the log. A grid power that stands still moves updated back
// to its start, as the outage began with it. Called with mu held.
func (s *peakState) outage(now time.Time, gridPower, batteryPower float64) (cause, detail string) {
	r := &s.read

	if r.gridUnread {
		return outageGridUnread, ""
	}

	for _, failed := range r.batteryFailed {
		if failed {
			return outageBatteryUnread, ""
		}
	}

	if bits := math.Float64bits(gridPower); r.gridSince.IsZero() || bits != r.gridBits {
		r.gridBits, r.gridSince = bits, now
		r.batteryMin, r.batteryMax = batteryPower, batteryPower
		return "", ""
	}

	r.batteryMin, r.batteryMax = min(r.batteryMin, batteryPower), max(r.batteryMax, batteryPower)

	if moved := r.batteryMax - r.batteryMin; now.Sub(r.gridSince) > peak.MaxGap && moved > peakFrozenBatteryDelta {
		if s.updated.IsZero() || r.gridSince.Before(s.updated) {
			s.updated = r.gridSince
		}

		return outageGridFrozen, fmt.Sprintf(" (%.0fW since %s, the battery moved by %.0fW)", gridPower, r.gridSince.Format("15:04:05"), moved)
	}

	return "", ""
}

// peakReadingsOk checks the meter values of the cycle before peak shaving works
// with them. Good values renew the time of the last values, see metersStale;
// without them the cycle keeps the setpoint of the last good one and leaves the
// rest to peakCheckMeters. back reports that the values are back after an outage,
// also one shorter than peak.MaxGap, which metersLost does not see.
func (site *Site) peakReadingsOk(state siteState) (ok, back bool) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()

	// peak shaving off or not set up: nothing to keep and nothing to report, the
	// window is metered as before
	if !s.enabled || !s.configured() {
		s.read.lost = ""
		s.updated = now
		return true, false
	}

	cause, detail := s.outage(now, state.gridPower, state.battery.Power)

	previous := s.read.lost
	s.read.lost = cause

	if cause == "" {
		s.updated = now
		return true, previous != ""
	}

	if cause != previous {
		site.log.WARN.Printf("peak shaving: %s%s, the setpoint stays as it is and the battery runs freely after %s without values", cause, detail, peak.MaxGap)
	}

	return false, false
}

// setPeakMeterRead takes the result of reading the power of a meter. evcc leaves
// the power of a meter it cannot read at 0 without a mark, and for a Home
// Assistant sensor that is unavailable not even a log line. Only the battery
// matters here: the setpoint adds its power back, so a battery that is
// discharging but reads 0 makes the setpoint jump from cycle to cycle.
func (site *Site) setPeakMeterRead(key string, i int, err error) {
	if key != "battery" {
		return
	}

	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.read.batteryFailed == nil {
		s.read.batteryFailed = make(map[int]bool)
	}

	s.read.batteryFailed[i] = err != nil
}
