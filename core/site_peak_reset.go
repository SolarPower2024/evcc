package core

// Custom extension: resets the month's peak, Konfiguration → System → Debug. For
// a reading that was wrong (a meter spike) the limit would stay raised the whole
// month: the battery's limit is the higher of evcc's limit and the month's peak.
//
// Only the time of the reset is stored. The quarter hours before it no longer
// count, for evcc's grid peak limit (its days come from the metrics, queried from
// the reset on) and for the statistics of the fork; the measurements themselves
// stay as they are. In the next month the time has no effect any more.

import (
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db/settings"
)

// peakResetAt returns the time of the last reset, zero without one
func (site *Site) peakResetAt() time.Time {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.resetAt
}

// restorePeakReset restores the time of the reset
func (site *Site) restorePeakReset() {
	v, err := settings.Int(keys.PeakResetAt)
	if err != nil || v <= 0 {
		return
	}

	s := site.peak()

	s.mu.Lock()
	s.resetAt = time.Unix(v, 0)
	s.mu.Unlock()
}

// ResetMonthPeak lets the month start again: the quarter hours up to now no
// longer count for the peak
func (site *Site) ResetMonthPeak() error {
	s := site.peak()

	s.mu.Lock()
	now := s.clock.Now()
	s.resetAt = now

	if len(s.months) > 0 && s.months[0].Month == now.Local().Format("2006-01") {
		m := &s.months[0]
		m.Peak, m.PeakAt, m.Demand, m.DemandAt = 0, time.Time{}, 0, time.Time{}
		s.monthsDirty = true
	}
	s.mu.Unlock()

	settings.SetInt(keys.PeakResetAt, now.Unix())
	site.log.INFO.Printf("peak of the month reset")

	site.savePeakMonths()
	site.updateGridPeak()
	site.updatePeakLimit()

	return nil
}

// peakAfterReset takes the days of the month's peak, from the quarter hours since
// the reset if there was one this month, the days before it empty
func (site *Site) peakAfterReset(c *metrics.Collector, energy []*float64) []*float64 {
	at := site.peakResetAt()
	if at.IsZero() || at.Local().Format("2006-01") != site.peak().clock.Now().Local().Format("2006-01") {
		return energy
	}

	days, err := c.DailyPeakEnergySince(at)
	if err != nil {
		site.log.ERROR.Printf("grid peak since reset: %v", err)
		return energy
	}

	return days
}
