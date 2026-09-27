package core

// Custom extension: a feed-in price that is only published after the fact.
//
// The OeMAG market price of a month becomes known around mid of the following
// month. Until then the latest published value is used as the running feed-in
// price. From the tariff's finalize day on, that value is taken as the final
// price of the previous month, and everything stored with the provisional
// price is recalculated once:
//
//   - the persisted 15 minute feed-in rates of that month
//   - the price of the charging sessions started in that month, whose solar
//     share is valued at the feed-in price
//
// Each month is finalized automatically exactly once, even if the source changes
// its value later: the value on the finalize day is the one that counts. A month
// can be recalculated by hand from the ui, with a corrected price if need be.
// The finalized months are kept as a history the ui shows.

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/tariff"
	"gorm.io/gorm"
)

// finalFeedIn is a feed-in tariff whose price for a month is published after it
type finalFeedIn interface {
	FinalizeDay() int
	MarketPrice() (float64, error)
	TotalPrice(market float64, ts time.Time) float64
}

// feedInMonth is a finalized month as kept in the history
type feedInMonth struct {
	Month    string    `json:"month"`    // YYYY-MM
	Market   float64   `json:"market"`   // market price in EUR/kWh, 0 = unknown
	Price    float64   `json:"price"`    // feed-in rate applied, with charges and tax
	Slots    int       `json:"slots"`    // 15 minute rates recalculated
	Sessions int       `json:"sessions"` // charging sessions recalculated
	Skipped  int       `json:"skipped"`  // sessions without stored rates
	At       time.Time `json:"at"`
	Manual   bool      `json:"manual,omitempty"`
}

// feedInState is what the ui shows in the feed-in tariff's card
type feedInState struct {
	FinalizeDay int           `json:"finalizeDay"`
	Market      float64       `json:"market"` // latest published market price, 0 = none yet
	Months      []feedInMonth `json:"months"`
}

// feedInHistoryLength is how many months the history keeps
const feedInHistoryLength = 24

// feedInFinalizer returns the tariff's final price capability, looking through
// the wrappers evcc puts around tariffs: one for rates coarser than a slot, one
// for a tariff that could not be created at startup
func feedInFinalizer(t api.Tariff) (finalFeedIn, bool) {
	for range 4 {
		if f, ok := t.(finalFeedIn); ok {
			return f, true
		}

		w, ok := t.(interface{ Unwrap() api.Tariff })
		if !ok {
			break
		}
		t = w.Unwrap()
	}

	return nil, false
}

// feedInRetry is how long a failed finalization waits before the next attempt
const feedInRetry = time.Hour

// updateFeedInFinalization finalizes the previous month's feed-in price once
// the tariff's finalize day has been reached. Called every cycle, cheap when
// there is nothing to do.
func (site *Site) updateFeedInFinalization() {
	f, ok := feedInFinalizer(site.GetTariff(api.TariffUsageFeedIn))
	if !ok || db.Instance == nil {
		return
	}

	site.lms().feedInOnce.Do(site.backfillFeedInHistory)

	// the ui shows the latest market price, which changes a few times a month
	if site.feedInMarketChanged(f) {
		site.publishFeedIn(f)
	}

	now := time.Now()
	done, _ := settings.String(keys.FeedInFinalized)

	from, to, month, due := feedInDue(now, f.FinalizeDay(), done)
	if !due {
		return
	}

	s := site.lms()

	s.mu.Lock()
	wait := now.Sub(s.feedInTried) < feedInRetry
	if !wait {
		s.feedInTried = now
	}
	s.mu.Unlock()

	if wait {
		return
	}

	market, err := f.MarketPrice()
	if err != nil {
		site.log.ERROR.Printf("feed-in %s: %v", month, err)
		return
	}

	if err := site.finalizeFeedInMonth(f, month, from, to, market, false); err != nil {
		site.log.ERROR.Printf("feed-in %s: %v", month, err)
	}
}

// finalizeFeedInMonth recalculates a month at the given market price and
// records it in the history
func (site *Site) finalizeFeedInMonth(f finalFeedIn, month string, from, to time.Time, market float64, manual bool) error {
	res, err := finalizeFeedIn(db.Instance, from, to, func(ts time.Time) (float64, error) {
		return f.TotalPrice(market, ts), nil
	})
	if err != nil {
		return err
	}

	// the automatic finalization skips a month recalculated by hand
	if done, _ := settings.String(keys.FeedInFinalized); done < month {
		settings.SetString(keys.FeedInFinalized, month)
	}

	site.recordFeedInMonth(feedInMonth{
		Month:    month,
		Market:   market,
		Price:    res.price,
		Slots:    res.slots,
		Sessions: res.sessions,
		Skipped:  res.skipped,
		At:       time.Now(),
		Manual:   manual,
	})
	site.publishFeedIn(f)

	site.log.INFO.Printf("feed-in %s finalized at %.5f/kWh: %d rate slots and %d charging sessions recalculated, %d sessions without stored rates left as they were",
		month, res.price, res.slots, res.sessions, res.skipped)

	return nil
}

// feedInHistory returns the finalized months, newest first
func feedInHistory() []feedInMonth {
	var res []feedInMonth
	_ = settings.Json(keys.FeedInHistory, &res)
	return res
}

// recordFeedInMonth adds a month to the history, replacing an earlier entry
func (site *Site) recordFeedInMonth(m feedInMonth) {
	months := slices.DeleteFunc(feedInHistory(), func(e feedInMonth) bool { return e.Month == m.Month })
	months = append(months, m)

	// YYYY-MM sorts in date order
	slices.SortFunc(months, func(a, b feedInMonth) int { return cmp.Compare(b.Month, a.Month) })
	if len(months) > feedInHistoryLength {
		months = months[:feedInHistoryLength]
	}

	if err := settings.SetJson(keys.FeedInHistory, months); err != nil {
		site.log.ERROR.Printf("feed-in history: %v", err)
	}
}

// backfillFeedInHistory adds a month finalized before the history existed, with
// the rate its slots hold now
func (site *Site) backfillFeedInHistory() {
	done, err := settings.String(keys.FeedInFinalized)
	if err != nil || done == "" {
		return
	}

	if slices.ContainsFunc(feedInHistory(), func(m feedInMonth) bool { return m.Month == done }) {
		return
	}

	from, err := time.ParseInLocation("2006-01", done, time.Local)
	if err != nil {
		return
	}

	m := feedInMonth{Month: done}
	if avg, ok, err := metrics.FeedInAverage(db.Instance, from, from.AddDate(0, 1, 0)); err == nil && ok {
		m.Price = avg
	}

	site.recordFeedInMonth(m)
}

// feedInMarketChanged reports whether the market price differs from the one
// last published, or none was published yet
func (site *Site) feedInMarketChanged(f finalFeedIn) bool {
	market, _ := f.MarketPrice()

	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.feedInMarket != nil && *s.feedInMarket == market {
		return false
	}
	s.feedInMarket = &market

	return true
}

func (site *Site) publishFeedIn(f finalFeedIn) {
	market, _ := f.MarketPrice()

	months := feedInHistory()
	if months == nil {
		months = []feedInMonth{}
	}

	site.publish(keys.FeedInFinal, feedInState{
		FinalizeDay: f.FinalizeDay(),
		Market:      market,
		Months:      months,
	})
}

// FinalizeFeedIn recalculates a past month at the given market price in EUR/kWh:
// for a price that was wrong on the finalize day, or a month not finalized yet
func (site *Site) FinalizeFeedIn(month string, market float64) error {
	f, ok := feedInFinalizer(site.GetTariff(api.TariffUsageFeedIn))
	if !ok {
		return errors.New("the feed-in tariff has no final price")
	}
	if db.Instance == nil {
		return errors.New("no database")
	}

	if err := tariff.ValidMarketPrice(market); err != nil {
		return err
	}

	from, err := time.ParseInLocation("2006-01", month, time.Local)
	if err != nil {
		return fmt.Errorf("invalid month: %s", month)
	}

	now := time.Now()
	if !from.Before(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)) {
		return fmt.Errorf("month is not over yet: %s", month)
	}

	return site.finalizeFeedInMonth(f, month, from, from.AddDate(0, 1, 0), market, true)
}

// feedInDue returns the month to finalize at now: the previous one, once the
// finalize day is reached and unless that month is done already
func feedInDue(now time.Time, day int, done string) (from, to time.Time, month string, due bool) {
	from = time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, now.Location())
	to = from.AddDate(0, 1, 0)
	month = from.Format("2006-01")

	// YYYY-MM compares in date order
	due = now.Day() >= day && done < month

	return from, to, month, due
}

type feedInResult struct {
	price                    float64
	slots, sessions, skipped int
}

// finalizeFeedIn applies the final feed-in price to [from,to) in one
// transaction. Sessions come first, they need the provisional rates the slots
// still hold.
func finalizeFeedIn(conn *gorm.DB, from, to time.Time, rate func(time.Time) (float64, error)) (feedInResult, error) {
	var res feedInResult

	price, err := rate(from)
	if err != nil {
		return res, err
	}
	res.price = price

	err = conn.Transaction(func(tx *gorm.DB) error {
		// without a loadpoint there has never been a session table
		var sessions []session.Session
		if tx.Migrator().HasTable("sessions") {
			if err := tx.Table("sessions").Where("created >= ? AND created < ?", from, to).Find(&sessions).Error; err != nil {
				return err
			}
		}

		for _, s := range sessions {
			if s.Price == nil || s.SolarPercentage == nil || *s.SolarPercentage <= 0 || s.ChargedEnergy <= 0 {
				continue
			}

			finished := s.Finished
			if finished.IsZero() || !finished.After(s.Created) {
				finished = s.Created.Add(15 * time.Minute)
			}

			old, ok, err := metrics.FeedInAverage(tx, s.Created, finished)
			if err != nil {
				return err
			}
			if !ok {
				// the provisional price it was valued at is unknown
				res.skipped++
				continue
			}

			final, err := rate(s.Created)
			if err != nil {
				return err
			}

			price, perKWh := revaluedSessionPrice(s, old, final)

			if err := tx.Table("sessions").Where("id = ?", s.ID).
				Updates(map[string]any{"price": price, "price_per_kwh": perKWh}).Error; err != nil {
				return err
			}

			res.sessions++
		}

		n, err := metrics.SetFeedIn(tx, from, to, rate)
		res.slots = n

		return err
	})

	return res, err
}

// revaluedSessionPrice returns a session's price with its solar share valued at
// the final instead of the provisional feed-in rate. evcc prices solar energy at
// the feed-in rate it could otherwise have earned.
func revaluedSessionPrice(s session.Session, old, final float64) (float64, float64) {
	solar := s.ChargedEnergy * *s.SolarPercentage / 100
	price := *s.Price + (final-old)*solar
	return price, price / s.ChargedEnergy
}
