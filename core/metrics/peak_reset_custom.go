package metrics

import (
	"slices"
	"time"

	"github.com/evcc-io/evcc/tariff"
)

// Custom extension: the month's peak after a reset, see core/site_peak_reset.go.
// The query is evcc's (dailyPeakEnergy) with the start of the month moved to the
// reset, the slots before it do not count.

// PeakDays holds the persisted days of DailyPeakEnergySince for a slot and a
// reset, as evcc's DailyPeakEnergy keeps them per slot: the query runs once per
// slot, not once per cycle. The caller owns it and reads and writes it from one
// goroutine only.
type PeakDays struct {
	slot, from time.Time
	days       []*float64
}

// DailyPeakEnergySince is DailyPeakEnergy for the slots starting at or after from.
// The slot a reset falls into does not count, nor the running slot if it started
// before the reset. The cache belongs to the caller.
func (c *Collector) DailyPeakEnergySince(from time.Time, cache *PeakDays) ([]*float64, error) {
	slotStart := c.accu.clock.Now().Truncate(tariff.SlotDuration)

	if cache.days == nil || !slotStart.Equal(cache.slot) || !from.Equal(cache.from) {
		days, err := dailyPeakEnergy(c.entity.Group, slotStart, from)
		if err != nil {
			return nil, err
		}

		*cache = PeakDays{slot: slotStart, from: from, days: days}
	}

	res := slices.Clone(cache.days)

	// the running slot as in DailyPeakEnergy, if it started after the reset
	if today, energy := len(res)-1, c.accu.Energy; !slotStart.Before(from) && energy > 0 && !c.recovering() && (res[today] == nil || energy > *res[today]) {
		res[today] = &energy
	}

	return res, nil
}
