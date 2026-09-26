package metrics

// Custom extension: export sold under two feed-in tariffs, see
// core/site_feedin_eeg.go. The EEG counter is persisted by a regular collector
// of group "meter" (monitoring only, never part of a balance), its price per
// 15 minute slot in a table of its own. The split is derived from the persisted
// slots only: EEG = the counter's slot energy, standard feed-in (OeMAG) = grid
// export minus EEG, clamped at zero.

import (
	"time"

	"github.com/evcc-io/evcc/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FeedInEeg is the name of the EEG counter's collector in group Meter
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

// FeedInSplit is the export of one period, split by feed-in tariff. Revenue only
// covers slots with a known price.
type FeedInSplit struct {
	Period          string  `json:"period"` // YYYY-MM or YYYY-MM-DD
	Export          float64 `json:"export"` // kWh, grid meter
	Eeg             float64 `json:"eeg"`    // kWh, EEG counter
	Standard        float64 `json:"standard"`
	EegRevenue      float64 `json:"eegRevenue"`
	StandardRevenue float64 `json:"standardRevenue"`
	EegPriced       float64 `json:"eegPriced"` // kWh with a known EEG price
	StandardPriced  float64 `json:"standardPriced"`
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

// QueryFeedInSplit returns the export in [from,to) per month ("month") or per
// day ("day") in the location of from, oldest first. Periods without export and
// without EEG energy are left out.
func QueryFeedInSplit(from, to time.Time, aggregate string) ([]FeedInSplit, error) {
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

	layout := "2006-01"
	if aggregate == "day" {
		layout = time.DateOnly
	}

	var res []FeedInSplit
	for _, s := range slots {
		period := time.Unix(s.Ts, 0).In(from.Location()).Format(layout)
		if len(res) == 0 || res[len(res)-1].Period != period {
			res = append(res, FeedInSplit{Period: period})
		}
		p := &res[len(res)-1]

		eeg, standard := splitSlot(s.Export, s.Eeg)
		p.Export += s.Export
		p.Eeg += eeg
		p.Standard += standard

		if s.Price != nil {
			p.EegRevenue += eeg * *s.Price
			p.EegPriced += eeg
		}
		if s.FeedIn != nil {
			p.StandardRevenue += standard * *s.FeedIn
			p.StandardPriced += standard
		}
	}

	var out []FeedInSplit
	for _, p := range res {
		if p.Export > 0 || p.Eeg > 0 {
			out = append(out, p)
		}
	}

	return out, nil
}
