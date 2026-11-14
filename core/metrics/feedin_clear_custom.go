package metrics

// Custom extension: Konfiguration → System → Debug sets the feed-in of a time
// range to 0 kWh, for readings that were wrong. Only the feed-in of the grid
// meter's slots changes, the draw and all other meters stay as stored, the home
// consumption with them. Revenue, the EEG split and the energy flow are worked out
// from the feed-in when they are asked for (return_cost in QueryEnergy, the EEG
// split in QueryFeedInSplit), so they follow. The EEG counter, a separate meter,
// is a share of the feed-in and is set to 0 with it.

import (
	"errors"
	"time"

	"github.com/evcc-io/evcc/db"
	"gorm.io/gorm"
)

// feedInRange checks the range of the feed-in functions
func feedInRange(from, to time.Time) error {
	if from.IsZero() || to.IsZero() {
		return errors.New("missing from/to")
	}
	if !to.After(from) {
		return errors.New("'to' must be after 'from'")
	}
	return nil
}

// FeedInSummary returns the feed-in of the slots in [from,to) in kWh and its
// revenue, as the energy view shows it: standard and EEG tariff, slots with a
// known price. Nothing changes.
func FeedInSummary(from, to time.Time) (energy, revenue float64, err error) {
	if err := feedInRange(from, to); err != nil {
		return 0, 0, err
	}

	res, err := QueryFeedInSplit(from, to, "month")
	if err != nil {
		return 0, 0, err
	}

	for _, b := range res {
		energy += b.Export
		revenue += b.EegRevenue + b.StandardRevenue
	}

	return energy, revenue, nil
}

// ClearFeedIn sets the feed-in of the grid meter in the slots in [from,to) to 0,
// and the EEG counter's energy of those slots. It returns the number of changed
// grid slots.
func ClearFeedIn(from, to time.Time) (int64, error) {
	if err := feedInRange(from, to); err != nil {
		return 0, err
	}

	var rows int64

	err := db.Instance.Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`UPDATE meters SET return_energy = 0
			WHERE ts >= ? AND ts < ? AND return_energy != 0
			AND meter IN (SELECT id FROM entities WHERE "group" = ?)`,
			from.Unix(), to.Unix(), Grid)
		if res.Error != nil {
			return res.Error
		}
		rows = res.RowsAffected

		return tx.Exec(`UPDATE meters SET energy = 0
			WHERE ts >= ? AND ts < ? AND energy != 0
			AND meter IN (SELECT id FROM entities WHERE "group" = ? AND name = ?)`,
			from.Unix(), to.Unix(), Meter, FeedInEeg).Error
	})

	return rows, err
}
