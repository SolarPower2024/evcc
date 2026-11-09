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

// peakLimitRound is what the month's peak is rounded down to, in W
const peakLimitRound = 100.0

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

// restoreGridPeakBaseline restores evcc's limit when a failed restore step of evcc's
// before it left it out: peak shaving would run without a limit until the restart
func (site *Site) restoreGridPeakBaseline() {
	if v, err := settings.Float(keys.GridPeakBaseline); err == nil && v > 0 && site.GetGridPeakBaseline() == 0 {
		if err := site.SetGridPeakBaseline(v); err != nil {
			site.log.ERROR.Printf("restore %s: %v", keys.GridPeakBaseline, err)
		}
	}
}
