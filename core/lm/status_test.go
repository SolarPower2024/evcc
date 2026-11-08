package lm_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/lm"
	"github.com/stretchr/testify/assert"
)

func TestRecordThrottled(t *testing.T) {
	m := lm.New()

	wallbox := &testLoad{title: "wallbox"}
	now := time.Now()

	assert.False(t, m.Record(wallbox, 11000, 11000, true, now), "full power")
	assert.True(t, m.Record(wallbox, 11000, 7400, true, now), "throttled: reported once")
	assert.False(t, m.Record(wallbox, 11000, 7000, true, now), "still throttled")
	assert.False(t, m.Record(wallbox, 11000, 10980, true, now), "a few watts are rounding")
	assert.True(t, m.Record(wallbox, 11000, 7400, true, now), "throttled again")
	assert.False(t, m.Record(wallbox, 3000, 0, false, now), "not running, waiting is no throttling")

	d, ok := m.LastDecision(wallbox)
	assert.True(t, ok)
	assert.Equal(t, lm.Decision{Requested: 3000, Allowed: 0, At: now}, d)
}

func TestEventLog(t *testing.T) {
	m := lm.New()

	for i := range 25 {
		m.AddEvent(lm.Event{Type: lm.EventShed, Load: fmt.Sprint(i)})
	}

	ev := m.Events()
	assert.Len(t, ev, 20, "the oldest are dropped")
	assert.Equal(t, "24", ev[0].Load, "newest first")
	assert.Equal(t, "5", ev[19].Load)

	assert.Len(t, m.AllEvents(time.Time{}, nil), 20, "the others keep 20")
}

func TestEventLogKeeps(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	t.Run("number", func(t *testing.T) {
		m := lm.New()
		for i := range 350 {
			m.AddEvent(lm.Event{At: now.Add(time.Duration(i) * time.Second), Type: lm.EventPeak, Load: fmt.Sprint(i)})
		}

		all := m.AllEvents(now, nil)
		assert.Len(t, all, 300, "at most 300")
		assert.Equal(t, "349", all[0].Load)
		assert.Equal(t, "50", all[299].Load)
	})

	t.Run("age", func(t *testing.T) {
		m := lm.New()
		m.AddEvent(lm.Event{At: now.Add(-8 * 24 * time.Hour), Type: lm.EventPeak, Load: "old"})
		m.AddEvent(lm.Event{At: now.Add(-6 * 24 * time.Hour), Type: lm.EventPeak, Load: "week"})
		m.AddEvent(lm.Event{At: now, Type: lm.EventPeak, Load: "new"})

		all := m.AllEvents(now, nil)
		assert.Len(t, all, 2, "older than 7 days is dropped")
		assert.Equal(t, "week", all[1].Load)
	})

	t.Run("age when read", func(t *testing.T) {
		m := lm.New()
		m.AddEvent(lm.Event{At: now, Type: lm.EventPeak, Load: "a"})
		m.AddEvent(lm.Event{At: now.Add(time.Hour), Type: lm.EventPeak, Load: "b"})

		assert.Len(t, m.AllEvents(now.Add(7*24*time.Hour+30*time.Minute), nil), 1, "the first is a week old by then")
	})

	t.Run("peak events are not pushed out by the others", func(t *testing.T) {
		m := lm.New()
		for i := range 100 {
			m.AddEvent(lm.Event{At: now.Add(time.Duration(i) * time.Second), Type: lm.EventPeakEnd})
		}
		for i := range 100 {
			m.AddEvent(lm.Event{At: now.Add(time.Duration(100+i) * time.Second), Type: lm.EventShed})
		}

		all := m.AllEvents(now, nil)
		assert.Len(t, all, 120, "100 peak events and 20 others")
		assert.Len(t, m.AllEvents(now, func(e lm.Event) bool { return lm.PeakEvent(e.Type) }), 100)
	})
}

func TestEventFilterAndStore(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	m := lm.New()

	m.AddEvent(lm.Event{At: now, Type: lm.EventShed, Load: "wallbox"})
	m.AddEvent(lm.Event{At: now.Add(time.Second), Type: lm.EventReserveBelow, A: 24, B: 25})
	m.AddEvent(lm.Event{At: now.Add(2 * time.Second), Type: lm.EventPeakEnd, A: 1600, B: 180})

	peak := m.AllEvents(now, func(e lm.Event) bool { return lm.PeakEvent(e.Type) })
	if assert.Len(t, peak, 2, "the shed is not a peak event") {
		assert.Equal(t, lm.EventPeakEnd, peak[0].Type, "newest first")
	}

	snap, changed := m.Snapshot()
	assert.True(t, changed)
	assert.Len(t, snap, 3)
	assert.Equal(t, lm.EventShed, snap[0].Type, "oldest first")

	_, changed = m.Snapshot()
	assert.False(t, changed, "unchanged since the last snapshot")

	// a restart: the stored log comes back, what was logged since stays on top
	m2 := lm.New()
	m2.AddEvent(lm.Event{At: now.Add(time.Minute), Type: lm.EventMeter, A: 1})
	m2.Restore(snap, now.Add(time.Minute))

	all := m2.AllEvents(now, nil)
	if assert.Len(t, all, 4) {
		assert.Equal(t, lm.EventMeter, all[0].Type)
		assert.Equal(t, lm.EventShed, all[3].Type)
	}

	// stored events older than the age drop on restore
	m3 := lm.New()
	m3.Restore(snap, now.Add(8*24*time.Hour))
	assert.Empty(t, m3.AllEvents(now.Add(8*24*time.Hour), nil))

	// a failed save marks the log as unsaved again
	_, changed = m.Snapshot()
	assert.False(t, changed)
	m.Unsaved()
	_, changed = m.Snapshot()
	assert.True(t, changed)
}
