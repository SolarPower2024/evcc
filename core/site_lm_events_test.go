package core

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLmEventsSurviveRestart: the event log is stored every 5 minutes and when
// evcc stops, and comes back after a restart; PeakEvents gives all of them
func TestLmEventsSurviveRestart(t *testing.T) {
	keepSettings(t)

	site, clk := peakWindowSite(t, 0)

	site.lmm().AddEvent(lm.Event{At: clk.Now(), Type: lm.EventShed, Load: "wallbox"})
	site.lmm().AddEvent(lm.Event{At: clk.Now(), Type: lm.EventPeakEnd, A: 1600, B: 180})

	// the first save is due at once, the next after 5 minutes
	site.saveLmEvents(false)
	var saved []lm.Event
	require.NoError(t, settings.Json(keys.LmEvents, &saved))
	assert.Len(t, saved, 2)

	clk.Add(time.Minute)
	site.lmm().AddEvent(lm.Event{At: clk.Now(), Type: lm.EventMeter, A: 1})
	site.saveLmEvents(false)
	require.NoError(t, settings.Json(keys.LmEvents, &saved))
	assert.Len(t, saved, 2, "not yet: stored every 5 minutes")

	clk.Add(5 * time.Minute)
	site.saveLmEvents(false)
	require.NoError(t, settings.Json(keys.LmEvents, &saved))
	assert.Len(t, saved, 3)

	// stopping stores at once
	site.lmm().AddEvent(lm.Event{At: clk.Now(), Type: lm.EventStorage, A: 1})
	site.saveLmEvents(true)
	require.NoError(t, settings.Json(keys.LmEvents, &saved))
	assert.Len(t, saved, 4)

	// the restart
	again, _ := peakWindowSite(t, 0)
	again.custom.peak.clock = clk
	again.restoreLmEvents()

	events := again.lmm().AllEvents(time.Time{}, nil)
	if assert.Len(t, events, 4) {
		assert.Equal(t, lm.EventStorage, events[0].Type, "newest first")
	}

	all := again.PeakEvents()
	assert.Len(t, all, 4, "the shed is part of the dialog's list")
	assert.Equal(t, lm.EventStorage, all[0].Type, "newest first")
}

// TestLmEventsStopPersists: stopping writes the log to the database itself, the
// function that persists the settings runs in parallel and may be done before
func TestLmEventsStopPersists(t *testing.T) {
	noSettingsDB(t)

	before := settings.All()
	t.Cleanup(func() {
		if db.Instance != nil {
			if sqlDB, err := db.Instance.DB(); err == nil {
				sqlDB.Close()
			}
		}
		db.Instance = nil
		for _, s := range settings.All() {
			_ = settings.Delete(s.Key)
		}
		for _, s := range before {
			settings.SetString(s.Key, s.Value)
		}
	})

	path := filepath.Join(t.TempDir(), "evcc.db")
	require.NoError(t, db.NewInstance("sqlite", path))

	site, clk := peakWindowSite(t, 0)
	site.lmm().AddEvent(lm.Event{At: clk.Now(), Type: lm.EventPeakEnd, A: 1600, B: 180})
	site.saveLmEvents(false) // stored in memory only, the database comes with the next flush
	site.lmm().AddEvent(lm.Event{At: clk.Now(), Type: lm.EventMeter, A: 1})
	site.saveLmEvents(true)

	// the restart without the flush of the settings
	sqlDB, err := db.Instance.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	require.NoError(t, db.NewInstance("sqlite", path))

	var saved []lm.Event
	require.NoError(t, settings.Json(keys.LmEvents, &saved))
	assert.Len(t, saved, 2)
}
