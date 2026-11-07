package core

// Custom extension: one peak limit, evcc's grid import peak limit (Konfiguration →
// Externe Begrenzung → Netzbezugsspitze). A capacity tariff bills the month's
// highest quarter hour, so once the month has a higher peak, shaving below it
// saves nothing and only drains the battery: the battery's limit is the higher of
// the two and a new month starts at the limit again. Without a limit the battery
// keeps no reserve, see configured.

import (
	"math"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
)

const (
	peakLimitRound   = 100.0  // W, the month's peak is rounded down to it
	defaultPeakLimit = 5000.0 // W, in use with peak shaving on and no limit stored
)

// peakLimitFor is the battery's limit for evcc's limit and the month's peak
func peakLimitFor(baseline, monthPeak float64) float64 {
	if baseline <= 0 {
		return 0
	}
	// a typo in the dialog must not drain the battery
	return max(baseline, minPeakLimit, math.Floor(min(monthPeak, maxPeakLimit)/peakLimitRound)*peakLimitRound)
}

// updatePeakLimit takes evcc's limit and the month's peak, once per cycle and on
// every change. The month's peak is the statistics': it only knows completed
// quarter hours, so the running one's budget does not move with its own draw.
func (site *Site) updatePeakLimit() {
	baseline := site.GetGridPeakBaseline()

	s := site.peak()

	s.mu.Lock()
	var monthPeak float64
	if len(s.months) > 0 && s.months[0].Month == s.clock.Now().Local().Format("2006-01") {
		monthPeak = s.months[0].Peak
	}

	limit := peakLimitFor(baseline, monthPeak)
	previous, baselineChanged := s.limit, s.baseline != baseline
	s.limit, s.baseline = limit, baseline
	s.mu.Unlock()

	if baselineChanged && baseline > 0 {
		site.raisePeakBaseline(baseline) // the month's savings are counted above it, see site_peak_stats.go
	}

	if limit == previous {
		return
	}

	site.log.DEBUG.Printf("peak shaving limit: %.1f kW (limit %.1f kW, month's peak %.1f kW)", limit/1e3, baseline/1e3, monthPeak/1e3)

	site.publish(keys.PeakShavingLimit, limit)
	site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go
}

// migratePeakLimit takes the limit stored before there was one over into evcc's,
// if that has none, and removes the old keys. Only with peak shaving on: evcc's
// limit also throttles the loads, which a limit never in use must not start doing.
func (site *Site) migratePeakLimit() {
	limit, ok := restoreFloat(site.log, keys.PeakShavingLimit, validPeakLimit)
	if follow, err := settings.Bool(keys.PeakFollow); err == nil && follow {
		// following the peak, the limit set by hand was the base
		if v, found := restoreFloat(site.log, keys.PeakFollowBase, validPeakLimit); found {
			limit, ok = v, true
		}
	}

	if !ok {
		limit = defaultPeakLimit
	}

	if on, err := settings.Bool(keys.PeakShaving); err == nil && on && site.GetGridPeakBaseline() == 0 {
		site.log.INFO.Printf("peak shaving: limit %.1f kW taken over as grid import peak limit", limit/1e3)
		if err := site.SetGridPeakBaseline(limit); err != nil {
			site.log.ERROR.Printf("restore %s: %v", keys.PeakShavingLimit, err)
			return
		}
	}

	for _, key := range []string{keys.PeakShavingLimit, keys.PeakFollow, keys.PeakFollowBase, keys.PeakFollowBuffer} {
		if _, err := settings.String(key); err != nil {
			continue
		}
		if err := settings.Delete(key); err != nil {
			site.log.ERROR.Printf("restore %s: delete: %v", key, err)
		}
	}
}
