package metrics

// Custom extension: rewriting the persisted feed-in rates once a tariff's final
// price becomes known after the fact, see core/site_feedin.go.

import (
	"time"

	"gorm.io/gorm"
)

// FeedInAverage returns the average persisted feed-in rate in [from,to), false
// when no slot in that range holds one
func FeedInAverage(tx *gorm.DB, from, to time.Time) (float64, bool, error) {
	var res struct {
		Avg   *float64
		Count int64
	}

	err := tx.Model(new(tariffValue)).
		Select("AVG(feedin) AS avg, COUNT(feedin) AS count").
		Where("ts >= ? AND ts < ? AND feedin IS NOT NULL", from.Truncate(15*time.Minute).Unix(), to.Unix()).
		Scan(&res).Error

	if err != nil || res.Count == 0 || res.Avg == nil {
		return 0, false, err
	}

	return *res.Avg, true, nil
}

// SetFeedIn overwrites the persisted feed-in rates in [from,to) with rate(ts).
// Only slots that hold a feed-in rate are touched. Returns the number of slots.
func SetFeedIn(tx *gorm.DB, from, to time.Time, rate func(time.Time) (float64, error)) (int, error) {
	var rows []tariffValue
	if err := tx.Where("ts >= ? AND ts < ? AND feedin IS NOT NULL", from.Unix(), to.Unix()).Find(&rows).Error; err != nil {
		return 0, err
	}

	for _, r := range rows {
		v, err := rate(time.Unix(r.Timestamp, 0))
		if err != nil {
			return 0, err
		}

		if err := tx.Model(new(tariffValue)).Where("ts = ?", r.Timestamp).Update("feedin", v).Error; err != nil {
			return 0, err
		}
	}

	return len(rows), nil
}
