package core

// Custom extension: the event log of the load management (package lm) survives a
// restart. It is stored as the setting lmEvents every lmEventsSave and when evcc
// stops, and loaded at the start. The peak status dialog in the battery page asks
// for the peak related events through the api, see PeakEvents.

import (
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/db/settings"
)

// lmEventsSave is how often the event log is stored at most
const lmEventsSave = 5 * time.Minute

// restoreLmEvents loads the stored event log
func (site *Site) restoreLmEvents() {
	var events []lm.Event
	if !restoreJson(site.log, keys.LmEvents, &events) {
		return
	}

	site.lmm().Restore(events, site.peak().clock.Now())
}

// saveLmEvents stores the event log if it changed and lmEventsSave has passed
// since the last time, or at once with force (evcc stops)
func (site *Site) saveLmEvents(force bool) {
	s := site.lms()
	now := site.peak().clock.Now()

	s.mu.Lock()
	due := force || now.Sub(s.eventsSaved) >= lmEventsSave
	if due {
		s.eventsSaved = now
	}
	s.mu.Unlock()

	if !due {
		return
	}

	events, changed := site.lmm().Snapshot()
	if changed {
		if err := settings.SetJson(keys.LmEvents, events); err != nil {
			site.log.ERROR.Printf("load management events: %v", err)
			site.lmm().Unsaved() // tried again with the next save
			return
		}
	}

	// stopping: the settings are written to the database by a function that
	// stops in parallel and may be done before this one, so write them now
	if force {
		if err := settings.Persist(); err != nil {
			site.log.ERROR.Printf("load management events: %v", err)
		}
	}
}

// PeakEvents returns the events the peak status shows, newest first
func (site *Site) PeakEvents() []lm.Event {
	return site.lmm().AllEvents(site.peak().clock.Now(), func(e lm.Event) bool { return lm.PeakEvent(e.Type) })
}
