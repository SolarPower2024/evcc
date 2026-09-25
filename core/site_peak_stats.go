package core

// Custom extension: the highest quarter hour of each month with and without the
// battery, and how often the battery covered a peak. Shown under Mehr → Peak
// Shaving.

import (
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/db/settings"
)

// peakMonthsKept is how many months the statistics go back
const peakMonthsKept = 24

// peakMonth is one month of peak statistics
type peakMonth struct {
	Month         string    `json:"month"`         // YYYY-MM, local time
	Peak          float64   `json:"peak"`          // highest quarter hour average of the grid draw in W
	PeakAt        time.Time `json:"peakAt"`        // start of that quarter hour
	Demand        float64   `json:"demand"`        // highest quarter hour average without the battery in W
	DemandAt      time.Time `json:"demandAt"`      // start of that quarter hour
	Interventions int       `json:"interventions"` // peaks the battery covered
}

// restorePeakMonths loads the statistics
func (site *Site) restorePeakMonths() {
	s := site.peak()

	var months []peakMonth
	if err := settings.Json(keys.PeakMonths, &months); err == nil {
		s.mu.Lock()
		s.months = months
		s.mu.Unlock()
	}

	site.publish(keys.PeakMonths, months)
}

// peakMonthOf returns the entry of the month t lies in, adding it if missing.
// Must be called with the lock held.
func (s *peakState) peakMonthOf(t time.Time) *peakMonth {
	month := t.Local().Format("2006-01")

	if len(s.months) == 0 || s.months[0].Month != month {
		s.months = append([]peakMonth{{Month: month}}, s.months...)
		if len(s.months) > peakMonthsKept {
			s.months = s.months[:peakMonthsKept]
		}
	}

	return &s.months[0]
}

// recordPeakWindow takes a completed quarter hour into its month. Must be called
// with the lock held.
func (s *peakState) recordPeakWindow(start time.Time, drawnWs, demandWs float64) {
	window := lm.PeakWindow.Seconds()
	m := s.peakMonthOf(start)

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
	s.peakMonthOf(t).Interventions++
	s.monthsDirty = true
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
}
