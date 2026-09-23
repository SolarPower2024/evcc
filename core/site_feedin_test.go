package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeedInDue(t *testing.T) {
	loc := time.Local
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, loc) }

	for _, tc := range []struct {
		name  string
		now   time.Time
		day   int
		done  string
		month string
		due   bool
	}{
		{"before the finalize day", at(2026, 9, 14), 15, "", "2026-08", false},
		{"on the finalize day", at(2026, 9, 15), 15, "", "2026-08", true},
		{"after it, catching up", at(2026, 9, 28), 15, "", "2026-08", true},
		{"already done", at(2026, 9, 20), 15, "2026-08", "2026-08", false},
		{"next month, older one done", at(2026, 10, 15), 15, "2026-08", "2026-09", true},
		{"january finalizes december", at(2027, 1, 15), 15, "2026-11", "2026-12", true},
		{"custom day", at(2026, 9, 3), 3, "", "2026-08", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to, month, due := feedInDue(tc.now, tc.day, tc.done)
			assert.Equal(t, tc.month, month)
			assert.Equal(t, tc.due, due)
			assert.Equal(t, 1, from.Day())
			assert.Equal(t, from.AddDate(0, 1, 0), to)
		})
	}
}

// TestFinalizeFeedIn: August was valued at a provisional 7 ct, the final OeMAG
// price is 8.997 ct
func TestFinalizeFeedIn(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.Table("sessions").AutoMigrate(new(session.Session)))

	loc := time.Local
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, loc)
	sep := aug.AddDate(0, 1, 0)

	provisional, final := 0.07, 0.08997
	grid := 0.30

	// provisional feed-in rates for two august afternoons and the first of september
	persist := func(from time.Time, hours int) {
		for ts := from; ts.Before(from.Add(time.Duration(hours) * time.Hour)); ts = ts.Add(tariff.SlotDuration) {
			require.NoError(t, metrics.PersistTariffs(ts, &grid, &provisional, nil, nil))
		}
	}
	persist(time.Date(2026, 8, 10, 12, 0, 0, 0, loc), 2)
	persist(time.Date(2026, 8, 20, 12, 0, 0, 0, loc), 2)
	persist(time.Date(2026, 9, 1, 12, 0, 0, 0, loc), 2)

	f := func(v float64) *float64 { return &v }

	// 10 kWh, 60 % solar: 6 kWh at feed-in, 4 kWh at grid price
	solarSession := session.Session{
		Created: time.Date(2026, 8, 10, 12, 0, 0, 0, loc), Finished: time.Date(2026, 8, 10, 13, 0, 0, 0, loc),
		ChargedEnergy: 10, SolarPercentage: f(60),
		Price: f(6*provisional + 4*grid), PricePerKWh: f((6*provisional + 4*grid) / 10),
	}
	gridSession := session.Session{
		Created: time.Date(2026, 8, 20, 12, 0, 0, 0, loc), Finished: time.Date(2026, 8, 20, 13, 0, 0, 0, loc),
		ChargedEnergy: 5, SolarPercentage: f(0), Price: f(5 * grid), PricePerKWh: f(grid),
	}
	unknownSession := session.Session{ // evcc was not recording rates then
		Created: time.Date(2026, 8, 25, 12, 0, 0, 0, loc), Finished: time.Date(2026, 8, 25, 13, 0, 0, 0, loc),
		ChargedEnergy: 8, SolarPercentage: f(100), Price: f(8 * provisional), PricePerKWh: f(provisional),
	}
	septemberSession := session.Session{
		Created: time.Date(2026, 9, 1, 12, 0, 0, 0, loc), Finished: time.Date(2026, 9, 1, 13, 0, 0, 0, loc),
		ChargedEnergy: 10, SolarPercentage: f(100), Price: f(10 * provisional), PricePerKWh: f(provisional),
	}

	for _, s := range []*session.Session{&solarSession, &gridSession, &unknownSession, &septemberSession} {
		require.NoError(t, db.Instance.Table("sessions").Create(s).Error)
	}

	rate := func(time.Time) (float64, error) { return final, nil }

	res, err := finalizeFeedIn(db.Instance, aug, sep, rate)
	require.NoError(t, err)

	assert.Equal(t, final, res.price)
	assert.Equal(t, 16, res.slots, "two august afternoons, 8 slots each")
	assert.Equal(t, 1, res.sessions, "only the solar session changes")
	assert.Equal(t, 1, res.skipped, "the session without stored rates")

	load := func(id uint) session.Session {
		var s session.Session
		require.NoError(t, db.Instance.Table("sessions").First(&s, id).Error)
		return s
	}

	// solar share revalued: 6 kWh x (8.997 - 7) ct = +0.11982
	s := load(solarSession.ID)
	assert.InDelta(t, 6*final+4*grid, *s.Price, 1e-9)
	assert.InDelta(t, (6*final+4*grid)/10, *s.PricePerKWh, 1e-9)

	assert.InDelta(t, 5*grid, *load(gridSession.ID).Price, 1e-9, "no solar share, unchanged")
	assert.InDelta(t, 8*provisional, *load(unknownSession.ID).Price, 1e-9, "old rate unknown, unchanged")
	assert.InDelta(t, 10*provisional, *load(septemberSession.ID).Price, 1e-9, "other month, unchanged")

	// the rates of august are final now, september keeps its provisional ones
	avg, ok, err := metrics.FeedInAverage(db.Instance, aug, sep)
	require.NoError(t, err)
	require.True(t, ok)
	assert.InDelta(t, final, avg, 1e-9)

	avg, ok, err = metrics.FeedInAverage(db.Instance, sep, sep.AddDate(0, 1, 0))
	require.NoError(t, err)
	require.True(t, ok)
	assert.InDelta(t, provisional, avg, 1e-9)

	// running it again changes nothing: the sessions are now valued at the rate
	// the slots hold
	_, err = finalizeFeedIn(db.Instance, aug, sep, rate)
	require.NoError(t, err)
	assert.InDelta(t, 6*final+4*grid, *load(solarSession.ID).Price, 1e-9)
}

// a feed-in tariff behind the wrapper of a late created tariff is still found
func TestFeedInFinalizerUnwraps(t *testing.T) {
	var direct api.Tariff = &finalTariff{}
	_, ok := feedInFinalizer(direct)
	assert.True(t, ok)

	_, ok = feedInFinalizer(&wrappedTariff{direct})
	assert.True(t, ok)

	// daily rates are split into slots by evcc, possibly behind a late start
	_, ok = feedInFinalizer(&tariff.SlotWrapper{Tariff: direct})
	assert.True(t, ok, "slot wrapper")

	_, ok = feedInFinalizer(&wrappedTariff{&tariff.SlotWrapper{Tariff: direct}})
	assert.True(t, ok, "both wrappers")

	_, ok = feedInFinalizer(&wrappedTariff{nil})
	assert.False(t, ok, "not created yet")

	_, ok = feedInFinalizer(nil)
	assert.False(t, ok)
}

type finalTariff struct{ api.Tariff }

func (finalTariff) FinalizeDay() int                      { return 15 }
func (finalTariff) FinalPrice(time.Time) (float64, error) { return 0.09, nil }

type wrappedTariff struct{ t api.Tariff }

func (w *wrappedTariff) Rates() (api.Rates, error) { return nil, nil }
func (w *wrappedTariff) Type() api.TariffType      { return api.TariffTypePriceStatic }
func (w *wrappedTariff) Unwrap() api.Tariff        { return w.t }

// without loadpoints there is no session table, the rates are finalized anyway
func TestFinalizeFeedInWithoutSessions(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))

	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)
	provisional := 0.07
	require.NoError(t, metrics.PersistTariffs(aug.AddDate(0, 0, 9), nil, &provisional, nil, nil))

	res, err := finalizeFeedIn(db.Instance, aug, aug.AddDate(0, 1, 0), func(time.Time) (float64, error) { return 0.09, nil })
	require.NoError(t, err)
	assert.Equal(t, 1, res.slots)
	assert.Equal(t, 0, res.sessions)
}
