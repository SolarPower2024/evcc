package metrics

// Custom extension: Konfiguration → System → Debug recalculates the feed-in of a
// time range from the energy balance of each slot, for a feed-in that was stored
// wrong (e.g. the export sensor of the grid meter held the import sensor):
//
//	feed-in = max(0, PV + battery discharge + import - home - battery charge - loadpoints)
//
// All summands are measured independent of the export sensor. Only the feed-in of
// the grid meter's existing slots changes, the EEG counter, a separate meter, stays.
// Revenue, the EEG split and the energy flow follow as they are worked out from the
// feed-in when asked for. See feedin_clear_custom.go for the range check.

import (
	"sort"
	"time"

	"github.com/evcc-io/evcc/db"
	"gorm.io/gorm"
)

// feedInEpsilon: smaller balances and remainders are 0
const feedInEpsilon = 1e-9

// balanceFeedIn is the feed-in of a slot from its balance
func balanceFeedIn(s flowSlot) float64 {
	v := s.PV + s.BatOut + s.GridIn - s.Home - s.BatIn - s.Loadpoint
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

// feedInRecalcPlan reads the slots in [from,to) and returns the changes of the grid
// rows, the feed-in and revenue before and after. Nothing is written.
func feedInRecalcPlan(from, to time.Time) (changes []feedInChange, oldEnergy, newEnergy, oldRevenue, newRevenue float64, err error) {
	slots, err := sinkSlots(from, to)
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
	for _, row := range rows {
		s := slotBy[row.Ts]
		target := 0.0
		if !seen[row.Ts] {
			// the first grid meter of a slot takes the feed-in, others 0
			seen[row.Ts] = true
			target = balanceFeedIn(s)
			newEnergy += target
			newRevenue += revenue(s, target)
		}
		oldEnergy += row.Value
		oldRevenue += revenue(s, row.Value)

		if row.Value != target && (row.Value > feedInEpsilon || target > 0) {
			changes = append(changes, feedInChange{row.Ts, row.Meter, target})
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Ts < changes[j].Ts })
	return changes, oldEnergy, newEnergy, oldRevenue, newRevenue, nil
}

// FeedInRecalcSummary returns the feed-in of the slots in [from,to) in kWh and its
// revenue before and after a recalculation. Nothing changes.
func FeedInRecalcSummary(from, to time.Time) (oldEnergy, newEnergy, oldRevenue, newRevenue float64, err error) {
	if err = feedInRange(from, to); err != nil {
		return
	}
	_, oldEnergy, newEnergy, oldRevenue, newRevenue, err = feedInRecalcPlan(from, to)
	return
}

// RecalcFeedIn sets the feed-in of the grid meter in the slots in [from,to) to the
// balance of the slot. It returns the number of changed grid rows.
func RecalcFeedIn(from, to time.Time) (int64, error) {
	if err := feedInRange(from, to); err != nil {
		return 0, err
	}

	changes, _, _, _, _, err := feedInRecalcPlan(from, to)
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
