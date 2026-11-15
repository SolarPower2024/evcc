package core

// Custom extension: resets the month's peak, Konfiguration → System → Debug. For
// a reading that was wrong (a meter spike) the limit would stay raised the whole
// month: the battery's limit is the higher of evcc's limit and the month's peak.
//
// The time of the reset is stored. The quarter hours before it no longer count for
// the limits: evcc's grid peak limit (its days come from the metrics, queried from
// the reset on) and the battery's (the highest quarter hour since, `resetPeak`,
// stored with it). The statistics and the peak tariff card keep the real peak, the
// measurements stay as they are. In the next month the reset has no effect any more.

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
	if p, err := settings.Float(keys.PeakResetPeak); err == nil {
		s.resetPeak, s.resetPeakSaved = p, p
	}
	s.mu.Unlock()
}

// ResetMonthPeak lets the month start again: the quarter hours up to now no
// longer count for the peak. Called from a handler: it only sets the time, the
// next cycle does the rest.
func (site *Site) ResetMonthPeak() error {
	s := site.peak()

	s.mu.Lock()
	now := s.clock.Now()
	s.resetAt = now

	s.resetPeak, s.resetPeakSaved = 0, 0
	s.mu.Unlock()

	settings.SetInt(keys.PeakResetAt, now.Unix())
	settings.SetFloat(keys.PeakResetPeak, 0)
	site.log.INFO.Printf("peak of the month reset")
	// no call at the collector from here: the next cycle of the site loop works the
	// limits out anew

	return nil
}

// gridPeakEnergy is the collector's DailyPeakEnergy, from the quarter hours since the
// reset if there was one this month (the days before it empty): one query per slot,
// never both. Called from the site loop only (the cache is not locked).
func (site *Site) gridPeakEnergy(c *metrics.Collector) ([]*float64, error) {
	at := site.peakResetAt()
	if at.IsZero() || at.Local().Format("2006-01") != site.peak().clock.Now().Local().Format("2006-01") {
		return c.DailyPeakEnergy()
	}

	return c.DailyPeakEnergySince(at, &site.peak().resetDays)
}
