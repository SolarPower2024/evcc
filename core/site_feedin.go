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
// Each month is finalized exactly once, even if the source changes its value
// later: the value on the finalize day is the one that counts.

import (
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"gorm.io/gorm"
)

// finalFeedIn is a feed-in tariff whose price for a month is published after it
type finalFeedIn interface {
	FinalizeDay() int
	FinalPrice(ts time.Time) (float64, error)
}

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

	res, err := finalizeFeedIn(db.Instance, from, to, f.FinalPrice)
	if err != nil {
		site.log.ERROR.Printf("feed-in %s: %v", month, err)
		return
	}

	settings.SetString(keys.FeedInFinalized, month)
	site.publish(keys.FeedInFinalized, month)

	site.log.INFO.Printf("feed-in %s finalized at %.5f/kWh: %d rate slots and %d charging sessions recalculated, %d sessions without stored rates left as they were",
		month, res.price, res.slots, res.sessions, res.skipped)
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
