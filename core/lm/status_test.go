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
}
