package devtransfer

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
)

// BackupSuffix is the name of the backup of the real database made before a give back
const BackupSuffix = ".bak-dev"

// Summary is what a give back of the selection would hand over
type Summary struct {
	Since    time.Time `json:"since"`
	To       time.Time `json:"to"`
	Sessions int       `json:"sessions"`
	Slots    int       `json:"slots"`
	Months   int       `json:"months"`
}

// GetSummary counts the test time of this instance: sessions, quarter hours and
// months of peak values from since on
func GetSummary(ctx context.Context) (Summary, error) {
	since, ok := Since()
	if !ok {
		return Summary{}, ErrNotTaken
	}

	res := Summary{Since: since, To: time.Now()}

	d, err := db.Instance.DB()
	if err != nil {
		return res, err
	}

	times, err := sessionTimes(ctx, d, "")
	if err != nil {
		return res, err
	}
	for _, k := range times {
		if k.created >= since.UnixNano() {
			res.Sessions++
		}
	}

	if res.Slots, err = slotCount(ctx, d, "", since); err != nil {
		return res, err
	}

	if err := settings.Persist(); err != nil {
		return res, err
	}

	months, err := readMonths(ctx, d, "")
	if err != nil {
		return res, err
	}
	for _, m := range months {
		if m.month() >= sinceMonth(since) {
			res.Months++
		}
	}

	return res, nil
}

// GiveBack merges the test time of this instance into the real database: first a
// backup next to it, then everything in one transaction. An error leaves the real
// database as it was; a locked one is ErrLocked.
func GiveBack(ctx context.Context, items Items) (Counts, error) {
	path, err := LivePath()
	if err != nil {
		return Counts{}, err
	}

	since, ok := Since()
	if !ok {
		return Counts{}, ErrNotTaken
	}

	if err := settings.Persist(); err != nil {
		return Counts{}, err
	}

	// the backup first, written beside and moved in place when complete
	bak := path + BackupSuffix
	if err := backupFile(ctx, path, bak+".tmp"); err != nil {
		_ = os.Remove(bak + ".tmp")
		return Counts{}, fmt.Errorf("backup: %w", err)
	}
	if err := os.Rename(bak+".tmp", bak); err != nil {
		return Counts{}, fmt.Errorf("backup: %w", err)
	}

	return merge(ctx, path, db.FilePath(), since, items, &Applied{At: time.Now(), Items: items.Names()})
}

// LastApplied returns the record of the last give back this database holds
func LastApplied() (Applied, bool) {
	var a Applied
	if err := settings.Json(DevTransferApplied, &a); err != nil {
		return Applied{}, false
	}
	return a, true
}
