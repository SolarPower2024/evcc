package metrics

// Custom extension: the weekday profile over a chosen history, for the home
// forecast per weekday, see core/site_load_weekday.go.

import "time"

// EnergyProfileWeekdaySince is EnergyProfileWeekday from the given time on
func (c *Collector) EnergyProfileWeekdaySince(weekday time.Weekday, from time.Time) (*[96]float64, error) {
	wd := int(weekday)
	return energyProfileFiltered(c.entity, from, &wd, profilePercentile())
}
