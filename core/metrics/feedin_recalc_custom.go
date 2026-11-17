package metrics

// Custom extension: Konfiguration → System → Debug recalculates the feed-in of a
// time range from the energy balance of each slot, for a feed-in that was stored
// wrong (e.g. the export sensor of the grid meter held the import sensor):
//
//	feed-in = max(0, min(PV + discharge, PV + discharge + import - home - charge - loadpoints))
//
// All summands are measured independent of the export sensor. Only the feed-in of
// the grid meter's existing slots changes, the EEG counter, a separate meter, stays.
// Revenue, the EEG split and the energy flow follow as they are worked out from the
// feed-in when asked for. See feedin_clear_custom.go for the range check.

import (
	"time"

	"github.com/evcc-io/evcc/db"
	"gorm.io/gorm"
)

// feedInEpsilon: smaller balances and remainders are 0
const feedInEpsilon = 1e-9

// balanceFeedIn is the feed-in of a slot from its balance. Only production and
// battery discharge can be fed in: without them (at night) the small difference
// between the metered import and the home consumption, worked out from powers,
// would add up to a feed-in that never happened.
func balanceFeedIn(s flowSlot) float64 {
	v := min(s.PV+s.BatOut, s.PV+s.BatOut+s.GridIn-s.Home-s.BatIn-s.Loadpoint)
	if v < feedInEpsilon {
		return 0
	}
	return v
}

// feedInChange is the new feed-in of one grid meter row
type feedInChange struct {
	Ts    int64
	Meter int
	Value float64
}

// FeedInRecalc is the feed-in and its revenue before and after a recalculation.
// Skipped counts the slots without a home consumption row: their balance would
// count all production as feed-in, so they stay as stored, only capped at the
// slot's production and discharge when it has a PV row.
type FeedInRecalc struct {
	OldEnergy, NewEnergy   float64 // kWh
	OldRevenue, NewRevenue float64 // currency
	Skipped                int
}

// feedInRecalcPlan reads the slots in [from,to) and returns the changes of the grid
// rows and the feed-in and revenue before and after. Nothing is written.
func feedInRecalcPlan(from, to time.Time) (changes []feedInChange, res FeedInRecalc, err error) {
	slots, err := sinkSlots(from, to)
	if err != nil {
		return
	}

	// slots with a stored row of a group
	slotsWith := func(group string) (map[int64]bool, error) {
		var tss []int64
		if err := db.Instance.Raw(`
			SELECT DISTINCT m.ts FROM meters m JOIN entities e ON e.id = m.meter AND e."group" = ?
			WHERE m.ts >= ? AND m.ts < ?`,
			group, from.Unix(), to.Unix()).Scan(&tss).Error; err != nil {
			return nil, err
		}
		res := make(map[int64]bool, len(tss))
		for _, ts := range tss {
			res[ts] = true
		}
		return res, nil
	}

	hasHome, err := slotsWith(Home)
	if err != nil {
		return
	}
	hasPV, err := slotsWith(PV)
	if err != nil {
		return
	}

	// loadpoints add up to one sink
	type tsSum struct {
		Ts  int64
		Sum float64
	}
	var lps []tsSum
	if err = db.Instance.Raw(`
		SELECT m.ts AS ts, SUM(m.energy) AS sum
		FROM meters m JOIN entities e ON e.id = m.meter AND e."group" = ?
		WHERE m.ts >= ? AND m.ts < ? GROUP BY m.ts`,
		Loadpoint, from.Unix(), to.Unix()).Scan(&lps).Error; err != nil {
		return
	}
	loadpoints := make(map[int64]float64, len(lps))
	for _, l := range lps {
		loadpoints[l.Ts] = l.Sum
	}

	// EEG counter and EEG price per slot: the revenue split as in QueryFeedInSplit
	type eegSlot struct {
		Ts    int64
		Eeg   *float64
		Price *float64
	}
	var eegs []eegSlot
	if err = db.Instance.Raw(`
		SELECT g.ts AS ts, e.energy AS eeg, p.price AS price
		FROM meters g
		JOIN entities ge ON ge.id = g.meter AND ge."group" = ?
		LEFT JOIN entities ee ON ee."group" = ? AND ee.name = ?
		LEFT JOIN meters e ON e.meter = ee.id AND e.ts = g.ts
		LEFT JOIN tariffs_eeg p ON p.ts = g.ts
		WHERE g.ts >= ? AND g.ts < ?`,
		Grid, Meter, FeedInEeg, from.Unix(), to.Unix()).Scan(&eegs).Error; err != nil {
		return
	}
	eegBy := make(map[int64]eegSlot, len(eegs))
	for _, e := range eegs {
		eegBy[e.Ts] = e
	}

	// existing grid rows
	var rows []feedInChange
	if err = db.Instance.Raw(`
		SELECT m.ts AS ts, m.meter AS meter, m.return_energy AS value
		FROM meters m JOIN entities e ON e.id = m.meter AND e."group" = ?
		WHERE m.ts >= ? AND m.ts < ? ORDER BY m.ts, m.meter`,
		Grid, from.Unix(), to.Unix()).Scan(&rows).Error; err != nil {
		return
	}

	slotBy := make(map[int64]flowSlot, len(slots))
	for _, s := range slots {
		s.Loadpoint = loadpoints[s.Ts]
		slotBy[s.Ts] = s
	}

	revenue := func(s flowSlot, export float64) (r float64) {
		eeg, standard := splitSlot(export, eegBy[s.Ts].Eeg)
		if p := eegBy[s.Ts].Price; p != nil {
			r += eeg * *p
		}
		if s.FeedIn != nil {
			r += standard * *s.FeedIn
		}
		return r
	}

	seen := make(map[int64]bool)
	capLeft := make(map[int64]float64) // production left for the grid rows of a slot without home
	for _, row := range rows {
		s := slotBy[row.Ts]
		res.OldEnergy += row.Value
		res.OldRevenue += revenue(s, row.Value)

		if !hasHome[row.Ts] {
			// no balance without the home consumption: the slot stays, only capped at
			// production and discharge (e.g. a counter jump after a sensor change)
			if !seen[row.Ts] {
				seen[row.Ts] = true
				res.Skipped++
				capLeft[row.Ts] = s.PV + s.BatOut
			}
			target := row.Value
			if hasPV[row.Ts] {
				target = max(0, min(row.Value, capLeft[row.Ts]))
				capLeft[row.Ts] -= target
				if target < feedInEpsilon {
					target = 0
				}
			}
			res.NewEnergy += target
			res.NewRevenue += revenue(s, target)
			if row.Value-target > feedInEpsilon {
				changes = append(changes, feedInChange{row.Ts, row.Meter, target})
			}
			continue
		}

		target := 0.0
		if !seen[row.Ts] {
			// the first grid meter of a slot takes the feed-in, others 0
			seen[row.Ts] = true
			target = balanceFeedIn(s)
			res.NewEnergy += target
			res.NewRevenue += revenue(s, target)
		}

		if row.Value != target && (row.Value > feedInEpsilon || target > 0) {
			changes = append(changes, feedInChange{row.Ts, row.Meter, target})
		}
	}

	return changes, res, nil
}

// FeedInRecalcSummary returns the feed-in of the slots in [from,to) in kWh and its
// revenue before and after a recalculation. Nothing changes.
func FeedInRecalcSummary(from, to time.Time) (FeedInRecalc, error) {
	if err := feedInRange(from, to); err != nil {
		return FeedInRecalc{}, err
	}
	_, res, err := feedInRecalcPlan(from, to)
	return res, err
}

// RecalcFeedIn sets the feed-in of the grid meter in the slots in [from,to) to the
// balance of the slot. It returns the number of changed grid rows.
func RecalcFeedIn(from, to time.Time) (int64, error) {
	if err := feedInRange(from, to); err != nil {
		return 0, err
	}

	changes, _, err := feedInRecalcPlan(from, to)
	if err != nil {
		return 0, err
	}

	err = db.Instance.Transaction(func(tx *gorm.DB) error {
		for _, c := range changes {
			if err := tx.Exec(`UPDATE meters SET return_energy = ? WHERE meter = ? AND ts = ?`,
				c.Value, c.Meter, c.Ts).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	return int64(len(changes)), nil
}
