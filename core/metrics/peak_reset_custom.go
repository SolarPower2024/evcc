package metrics

import (
	"slices"
	"sync"
	"time"

	"github.com/evcc-io/evcc/tariff"
)

// Custom extension: the month's peak after a reset, see core/site_peak_reset.go.
// The query is evcc's (dailyPeakEnergy) with the start of the month moved to the
// reset, the slots before it do not count.

// peakSince caches the persisted days per collector, as evcc's DailyPeakEnergy
// does per slot: the query runs once per slot, not once per cycle
var (
	peakSinceMu    sync.Mutex
	peakSinceCache = make(map[*Collector]peakSince)
)

type peakSince struct {
	slot, from time.Time
	days       []*float64
}

// DailyPeakEnergySince is DailyPeakEnergy for the slots starting at or after from.
// The slot a reset falls into does not count, nor the running slot if it started
// before the reset.
func (c *Collector) DailyPeakEnergySince(from time.Time) ([]*float64, error) {
	slotStart := c.accu.clock.Now().Truncate(tariff.SlotDuration)

	peakSinceMu.Lock()
	cached, ok := peakSinceCache[c]
	peakSinceMu.Unlock()

	if !ok || !slotStart.Equal(cached.slot) || !from.Equal(cached.from) {
		days, err := dailyPeakEnergy(c.entity.Group, slotStart, from)
		if err != nil {
			return nil, err
		}

		cached = peakSince{slot: slotStart, from: from, days: days}

		peakSinceMu.Lock()
		peakSinceCache[c] = cached
		peakSinceMu.Unlock()
	}

	res := slices.Clone(cached.days)

	// the running slot as in DailyPeakEnergy, if it started after the reset
	if today, energy := len(res)-1, c.accu.Energy; !slotStart.Before(from) && energy > 0 && !c.recovering() && (res[today] == nil || energy > *res[today]) {
		res[today] = &energy
	}

	return res, nil
}
