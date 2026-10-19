package metrics

// Custom extension: export sold under two feed-in tariffs, see
// core/site_feedin_eeg.go. The EEG counter is persisted by a regular collector
// of group "meter" (monitoring only, never part of a balance), its price per
// 15 minute slot in a table of its own. The split is derived from the persisted
// slots only: EEG = the counter's slot energy, standard feed-in = grid
// export minus EEG, clamped at zero.

import (
	"errors"
	"math"
	"time"

	"github.com/evcc-io/evcc/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FeedInEeg is the name and title of the EEG counter's collector in group Meter
const FeedInEeg = "feedin-eeg"

type eegPrice struct {
	Timestamp int64   `gorm:"column:ts;uniqueIndex"` // 15min boundary
	Price     float64 `gorm:"column:price"`
}

func (eegPrice) TableName() string {
	return "tariffs_eeg"
}

func init() {
	db.Register(func(_ *gorm.DB) error {
		return db.Instance.AutoMigrate(new(eegPrice))
	})
}

// PersistEegPrice stores the EEG price of the slot starting at ts. An existing
// value is kept, like the other tariffs.
func PersistEegPrice(ts time.Time, price float64) error {
	return db.Instance.Clauses(clause.OnConflict{DoNothing: true}).Create(&eegPrice{
		Timestamp: ts.Unix(),
		Price:     price,
	}).Error
}

// FeedInSplit is the export of one bucket, split by feed-in tariff. Revenue
// only covers slots with a known price.
type FeedInSplit struct {
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	Export          float64   `json:"export"`   // kWh, grid meter
	Eeg             float64   `json:"eeg"`      // kWh, EEG counter
	Standard        float64   `json:"standard"` // kWh, export minus EEG
	EegRevenue      float64   `json:"eegRevenue"`
	StandardRevenue float64   `json:"standardRevenue"`
	EegPriced       float64   `json:"eegPriced"` // kWh with a known EEG price
	StandardPriced  float64   `json:"standardPriced"`
}

type feedInSlot struct {
	Ts     int64
	Export float64
	Eeg    *float64
	FeedIn *float64
	Price  *float64
}

// splitSlot divides a slot's export: EEG as metered, the rest at the standard
// feed-in tariff
func splitSlot(export float64, eeg *float64) (float64, float64) {
	var e float64
	if eeg != nil {
		e = *eeg
	}
	return e, max(0, export-e)
}

// QueryFeedInSplit returns the export in [from,to) per bucket of the energy
// history (15m, hour, day, month), oldest first. Buckets without export and
// without EEG energy are left out.
func QueryFeedInSplit(from, to time.Time, aggregate string) ([]FeedInSplit, error) {
	addDuration, ok := aggregateDurations[aggregate]
	if !ok {
		return nil, errors.New("invalid aggregate value")
	}

	var slots []feedInSlot

	err := db.Instance.Raw(`
		SELECT g.ts AS ts, g.return_energy AS export, e.energy AS eeg, t.feedin AS feed_in, p.price AS price
		FROM meters g
		JOIN entities ge ON ge.id = g.meter AND ge."group" = ?
		LEFT JOIN entities ee ON ee."group" = ? AND ee.name = ?
		LEFT JOIN meters e ON e.meter = ee.id AND e.ts = g.ts
		LEFT JOIN tariffs t ON t.ts = g.ts
		LEFT JOIN tariffs_eeg p ON p.ts = g.ts
		WHERE g.ts >= ? AND g.ts < ?
		ORDER BY g.ts`,
		Grid, Meter, FeedInEeg, from.Unix(), to.Unix(),
	).Scan(&slots).Error
	if err != nil {
		return nil, err
	}

	var res []FeedInSplit
	for _, s := range slots {
		start := aggregateStarts[aggregate](time.Unix(s.Ts, 0))
		if len(res) == 0 || !res[len(res)-1].Start.Equal(start) {
			res = append(res, FeedInSplit{Start: start, End: addDuration(start)})
		}
		b := &res[len(res)-1]

		eeg, standard := splitSlot(s.Export, s.Eeg)
		b.Export += s.Export
		b.Eeg += eeg
		b.Standard += standard

		if s.Price != nil {
			b.EegRevenue += eeg * *s.Price
			b.EegPriced += eeg
		}
		if s.FeedIn != nil {
			b.StandardRevenue += standard * *s.FeedIn
			b.StandardPriced += standard
		}
	}

	out := make([]FeedInSplit, 0, len(res))
	for _, b := range res {
		if b.Export > 0 || b.Eeg > 0 {
			out = append(out, b)
		}
	}

	return out, nil
}

// ErrInvalidPrice is returned for a price that is not finite or negative
var ErrInvalidPrice = errors.New("invalid price")

// DeleteEegPrices removes the stored EEG prices of the slots in [from,to). The
// count is the number of removed rows. Slots without a price count as without
// revenue in the statistics.
func DeleteEegPrices(from, to time.Time) (int64, error) {
	if from.IsZero() || to.IsZero() {
		return 0, errors.New("missing from/to")
	}

	res := db.Instance.Where("ts >= ? AND ts < ?", from.Unix(), to.Unix()).Delete(new(eegPrice))
	return res.RowsAffected, res.Error
}

// SetEegPrices sets the price of the stored slots in [from,to). Slots without a
// row stay without a price. The count is the number of changed rows.
func SetEegPrices(from, to time.Time, price float64) (int64, error) {
	if from.IsZero() || to.IsZero() {
		return 0, errors.New("missing from/to")
	}
	if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return 0, ErrInvalidPrice
	}

	res := db.Instance.Model(new(eegPrice)).
		Where("ts >= ? AND ts < ?", from.Unix(), to.Unix()).
		Update("price", price)
	return res.RowsAffected, res.Error
}
