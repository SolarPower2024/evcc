package core

// Custom extension: home consumption forecast per weekday. evcc forecasts the
// home base load for the optimizer from the average of the last 28 days per
// time of day, so weekends and working days blur into one profile. With it on
// (Lastmanagement-Details → Erweitert → Verbrauchsprognose), each forecast day takes the same weekday
// of the last 8 weeks instead. The percentile set there (upstream
// profilePercentile) applies to it like to the regular profile. A weekday
// without complete data yet falls back to the regular profile.

import (
	"errors"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/tariff"
	"github.com/jinzhu/now"
)

// homeWeekdayWeeks is how many weeks back the weekday profiles reach
const homeWeekdayWeeks = 8

// homeWeekday reports whether the home forecast is taken per weekday
func (site *Site) homeWeekday() bool {
	return site.homeForecast() == homeForecastWeekday
}

// homeProfileByWeekday returns the home base load in Wh for minLen 15min slots
// starting now, each day from the profile of its weekday
func (site *Site) homeProfileByWeekday(col *metrics.Collector, minLen int) ([]float64, error) {
	today := now.BeginningOfDay()
	from := today.AddDate(0, 0, -7*homeWeekdayWeeks)
	first := int(time.Now().Truncate(tariff.SlotDuration).Sub(today) / tariff.SlotDuration)

	var fallback *[96]float64
	profiles := make(map[time.Weekday]*[96]float64)

	res := make([]float64, minLen)
	for i := range res {
		slot := first + i
		weekday := today.AddDate(0, 0, slot/96).Weekday()

		p, ok := profiles[weekday]
		if !ok {
			var err error
			p, err = col.EnergyProfileWeekdaySince(weekday, from)
			if err != nil && !errors.Is(err, metrics.ErrIncomplete) {
				return nil, err
			}

			if err != nil || profileEmpty(p) {
				if fallback == nil {
					if fallback, err = col.EnergyProfile(today.AddDate(0, 0, -28)); err != nil {
						return nil, err
					}
				}
				p = fallback
			}

			profiles[weekday] = p
		}

		res[i] = p[slot%96] * 1e3
	}

	return res, nil
}

func profileEmpty(p *[96]float64) bool {
	for _, v := range p {
		if v != 0 {
			return false
		}
	}
	return true
}
