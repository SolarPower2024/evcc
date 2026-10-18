package core

// Custom extension: the highest quarter hour of each month with and without the
// battery, and how often the battery covered a peak. Shown under Mehr → Peak
// Shaving.

import (
	"cmp"
	"slices"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/db/settings"
)

// peakMonthsKept is how many months the statistics go back
const peakMonthsKept = 24

// peakMonth is one month of peak statistics
type peakMonth struct {
	Month         string    `json:"month"`              // YYYY-MM, local time
	Peak          float64   `json:"peak"`               // highest quarter hour average of the grid draw in W
	PeakAt        time.Time `json:"peakAt"`             // start of that quarter hour
	Demand        float64   `json:"demand"`             // highest quarter hour average without the battery in W
	DemandAt      time.Time `json:"demandAt"`           // start of that quarter hour
	Interventions int       `json:"interventions"`      // peaks the battery covered
	Baseline      float64   `json:"baseline,omitempty"` // highest limit set by hand in the month in W, 0 = none
}

// restorePeakMonths loads the statistics
func (site *Site) restorePeakMonths() {
	s := site.peak()

	var months []peakMonth
	if restoreJson(site.log, keys.PeakMonths, &months) {
		var changed bool
		months, changed = mergePeakMonths(months)

		s.mu.Lock()
		s.months = months
		s.monthsDirty = changed
		s.mu.Unlock()

		if changed {
			site.log.WARN.Printf("peak statistics: merged duplicate months")
			site.savePeakMonths()
			return
		}
	}

	site.publish(keys.PeakMonths, months)
}

// mergePeakMonths joins entries of the same month (highest peak and demand with
// their time, the interventions added up, the highest baseline), newest first, and
// reports whether it changed anything
func mergePeakMonths(in []peakMonth) ([]peakMonth, bool) {
	out := make([]peakMonth, 0, len(in))

	for _, m := range in {
		i := slices.IndexFunc(out, func(e peakMonth) bool { return e.Month == m.Month })
		if i < 0 {
			out = append(out, m)
			continue
		}

		e := &out[i]
		if m.Peak > e.Peak {
			e.Peak, e.PeakAt = m.Peak, m.PeakAt
		}
		if m.Demand > e.Demand {
			e.Demand, e.DemandAt = m.Demand, m.DemandAt
		}
		e.Interventions += m.Interventions
		e.Baseline = max(e.Baseline, m.Baseline)
	}

	slices.SortStableFunc(out, func(a, b peakMonth) int { return cmp.Compare(b.Month, a.Month) })
	if len(out) > peakMonthsKept {
		out = out[:peakMonthsKept]
	}

	return out, !slices.Equal(in, out)
}

// manualPeakLimit returns the limit set by hand: while following the peak that is
// the base, never the raised limit. Must be called with the lock held.
func (s *peakState) manualPeakLimit() float64 {
	if s.follow {
		return s.followBase
	}
	return s.limit
}

// peakMonthOf returns the entry of the month t lies in, adding it in its place
// (newest first) if missing, and whether it did. The running month starts with the
// limit set by hand, an earlier one that is entered late has no baseline. A month
// older than the statistics reach is not kept: the entry returned is then a scratch
// one. Must be called with the lock held.
func (s *peakState) peakMonthOf(t time.Time) (*peakMonth, bool) {
	month := t.Local().Format("2006-01")

	if i := slices.IndexFunc(s.months, func(m peakMonth) bool { return m.Month == month }); i >= 0 {
		return &s.months[i], false
	}

	m := peakMonth{Month: month}
	now := time.Now()
	if s.clock != nil {
		now = s.clock.Now()
	}
	if month == now.Local().Format("2006-01") {
		m.Baseline = s.manualPeakLimit()
	}

	i := slices.IndexFunc(s.months, func(e peakMonth) bool { return e.Month < month })
	if i < 0 {
		i = len(s.months)
	}
	s.months = slices.Insert(s.months, i, m)

	if len(s.months) > peakMonthsKept {
		s.months = s.months[:peakMonthsKept]
		if i >= peakMonthsKept {
			return &peakMonth{Month: month}, true
		}
	}

	return &s.months[i], true
}

// recordPeakWindow takes a completed quarter hour into its month. Must be called
// with the lock held.
func (s *peakState) recordPeakWindow(start time.Time, drawnWs, demandWs float64) {
	window := peak.Window.Seconds()
	m, _ := s.peakMonthOf(start)

	if avg := drawnWs / window; avg > m.Peak {
		m.Peak, m.PeakAt = avg, start
	}
	if avg := demandWs / window; avg > m.Demand {
		m.Demand, m.DemandAt = avg, start
	}

	s.monthsDirty = true
}

// recordPeakIntervention counts a peak the battery started covering. Must be
// called with the lock held.
func (s *peakState) recordPeakIntervention(t time.Time) {
	m, _ := s.peakMonthOf(t)
	m.Interventions++
	s.monthsDirty = true
}

// raisePeakBaseline lifts the baseline of the running month to a limit set by
// hand. Lowering the limit leaves it, the month was allowed the higher draw.
func (site *Site) raisePeakBaseline(limit float64) {
	s := site.peak()

	s.mu.Lock()
	now := s.clock.Now()
	m, added := s.peakMonthOf(now)
	if added {
		m.Baseline = limit // the month starts with this limit, not the one it replaces
	}
	if limit > m.Baseline || added {
		m.Baseline = max(m.Baseline, limit)
		s.monthsDirty = true
	}
	s.mu.Unlock()

	site.savePeakMonths()
}

// fillPeakBaselines gives the months without a baseline, those from before it
// existed, the limit set by hand now. They stay fixed afterwards.
func (site *Site) fillPeakBaselines() {
	s := site.peak()

	s.mu.Lock()
	if limit := s.manualPeakLimit(); limit > 0 {
		for i := range s.months {
			if s.months[i].Baseline == 0 {
				s.months[i].Baseline = limit
				s.monthsDirty = true
			}
		}
	}
	s.mu.Unlock()

	site.savePeakMonths()
}

// savePeakMonths persists and publishes the statistics after a change
func (site *Site) savePeakMonths() {
	s := site.peak()

	s.mu.Lock()
	if !s.monthsDirty {
		s.mu.Unlock()
		return
	}
	s.monthsDirty = false
	months := append([]peakMonth(nil), s.months...)
	s.mu.Unlock()

	if err := settings.SetJson(keys.PeakMonths, months); err != nil {
		site.log.ERROR.Printf("peak statistics: %v", err)
	}

	site.publish(keys.PeakMonths, months)
	site.publishPeakTariff()
}
