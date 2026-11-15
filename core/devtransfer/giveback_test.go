package devtransfer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDisabledGiveBack(t *testing.T) {
	t.Setenv(EnvVar, "")
	_, err := GiveBack(context.Background(), DefaultItems)
	assert.ErrorIs(t, err, ErrDisabled)
}

// taken makes the fixture a test version that has taken over
func taken(t *testing.T, f *fixture) {
	t.Helper()
	require.NoError(t, writeSince(t0))
	_, ok := Since()
	require.True(t, ok)
}

func TestGiveBack(t *testing.T) {
	f := newFixture(t)
	taken(t, f)

	// the real one has a slot of the test time already, with another value
	addSlot(t, f.live, "grid", "grid", t0, 1.5)
	exec(t, f.live, "INSERT INTO tariffs (ts, grid) VALUES (?, 0.5)", t0.Unix())
	addSession(t, f.live, "lp1", t0.Add(10*time.Minute), 4)

	// the test version recorded
	addSlot(t, f.dev, "grid", "grid", t0.Add(-slot), 99) // before the test time
	addSlot(t, f.dev, "grid", "grid", t0, 7)             // the real one has it
	addSlot(t, f.dev, "grid", "grid", t0.Add(slot), 8)
	addSlot(t, f.dev, "battery", "battery", t0.Add(slot), 2) // new entity
	exec(t, f.dev, "INSERT INTO tariffs (ts, grid) VALUES (?, 0.9)", t0.Unix())
	exec(t, f.dev, "INSERT INTO tariffs (ts, grid) VALUES (?, 0.7)", t0.Add(slot).Unix())
	exec(t, f.dev, "INSERT INTO tariffs_eeg (ts, price) VALUES (?, 0.12)", t0.Add(slot).Unix())
	addSession(t, f.dev, "lp1", t0.Add(10*time.Minute), 6) // the real one has it
	addSession(t, f.dev, "lp1", t0.Add(2*time.Hour), 9)
	addSession(t, f.dev, "lp1", t0.Add(-time.Hour), 9) // before the test time
	exec(t, f.dev, "INSERT INTO configs (class, type, value) VALUES ('meter', 'dev-meter', '{}')")
	exec(t, f.dev, "INSERT INTO settings (key, value) VALUES (?, ?)", keys.LmPriorities, "dev")
	exec(t, f.dev, "INSERT INTO settings (key, value) VALUES (?, ?)", "title", "Dev")
	peak(t, f.dev, month("2026-11", 3500), month("2026-12", 100))

	res, err := GiveBack(context.Background(), DefaultItems)
	require.NoError(t, err)
	assert.Equal(t, Counts{Sessions: 1, Slots: 2, Prices: 2, Months: 1}, res)

	// the backup holds the state before
	assert.FileExists(t, f.livePath+BackupSuffix)
	bak, err := openFile(f.livePath + BackupSuffix)
	require.NoError(t, err)
	defer bak.Close()
	var n int
	require.NoError(t, bak.QueryRow("SELECT COUNT(*) FROM meters").Scan(&n))
	assert.Equal(t, 3, n)

	g := f.live
	assert.Equal(t, 5, count(t, g, "SELECT COUNT(*) FROM meters"), "3 before, battery and grid of t0+slot")
	assert.InDelta(t, 1.5, func() float64 {
		var v float64
		require.NoError(t, g.Raw("SELECT energy FROM meters WHERE ts = ? AND meter = (SELECT id FROM entities WHERE name = 'grid')", t0.Unix()).Scan(&v).Error)
		return v
	}(), 1e-9, "an existing slot is not overwritten")
	assert.Equal(t, 0, count(t, g, "SELECT COUNT(*) FROM meters WHERE energy = 99"), "nothing from before the test time")
	assert.Equal(t, 1, count(t, g, "SELECT COUNT(*) FROM entities WHERE name = 'battery'"))

	assert.Equal(t, 3, count(t, g, "SELECT COUNT(*) FROM sessions"), "the real two and the one of the test time")
	assert.Equal(t, 1, count(t, g, "SELECT COUNT(*) FROM sessions WHERE charged_kwh = 9"))

	assert.InDelta(t, 0.5, func() float64 {
		var v float64
		require.NoError(t, g.Raw("SELECT grid FROM tariffs WHERE ts = ?", t0.Unix()).Scan(&v).Error)
		return v
	}(), 1e-9, "an existing price is not overwritten")
	assert.Equal(t, 2, count(t, g, "SELECT COUNT(*) FROM tariffs"))
	assert.Equal(t, 1, count(t, g, "SELECT COUNT(*) FROM tariffs_eeg"))

	// the higher peak of the month, a later month added
	assert.Equal(t, map[string]float64{"2026-12": 100, "2026-11": 4000, "2026-10": 3000}, months(t, g))

	// devices and settings stay, the record is stored
	assert.Equal(t, 1, count(t, g, "SELECT COUNT(*) FROM configs WHERE type = 'live-meter'"))
	assert.Equal(t, 0, count(t, g, "SELECT COUNT(*) FROM configs WHERE type = 'dev-meter'"))
	assert.Equal(t, "live", setting(t, g, keys.LmPriorities))
	assert.Equal(t, "Live", setting(t, g, "title"))

	var a Applied
	require.NoError(t, json.Unmarshal([]byte(setting(t, g, DevTransferApplied)), &a))
	assert.Equal(t, []string{"sessions", "energy", "peak"}, a.Items)
	assert.WithinDuration(t, time.Now(), a.At, time.Minute)
}

func TestGiveBackDevicesAndSettings(t *testing.T) {
	f := newFixture(t)
	taken(t, f)

	exec(t, f.dev, "INSERT INTO configs (class, type, value) VALUES ('meter', 'dev-meter', '{}')")
	exec(t, f.dev, "INSERT INTO settings (key, value) VALUES (?, ?)", keys.LmPriorities, "dev")
	exec(t, f.dev, "INSERT INTO settings (key, value) VALUES (?, ?)", "title", "Dev")
	exec(t, f.dev, "INSERT INTO settings (key, value) VALUES (?, ?)", keys.LmEvents, "dev-events")
	exec(t, f.live, "INSERT INTO settings (key, value) VALUES (?, ?)", keys.LmEvents, "live-events")
	exec(t, f.live, "INSERT INTO settings (key, value) VALUES (?, ?)", "gone", "1")

	// devices only
	_, err := GiveBack(context.Background(), Items{Devices: true})
	require.NoError(t, err)
	assert.Equal(t, 1, count(t, f.live, "SELECT COUNT(*) FROM configs WHERE type = 'dev-meter'"))
	assert.Equal(t, 0, count(t, f.live, "SELECT COUNT(*) FROM configs WHERE type = 'live-meter'"), "replaced")
	assert.Equal(t, "live", setting(t, f.live, keys.LmPriorities))

	// settings: replaced, the state keys stay
	_, err = GiveBack(context.Background(), Items{Settings: true})
	require.NoError(t, err)
	assert.Equal(t, "dev", setting(t, f.live, keys.LmPriorities))
	assert.Equal(t, "Dev", setting(t, f.live, "title"))
	assert.Equal(t, "", setting(t, f.live, "gone"))
	assert.Equal(t, "live-events", setting(t, f.live, keys.LmEvents), "state stays")
	assert.NotEmpty(t, setting(t, f.live, DevTransferApplied), "the record stays")
	assert.Len(t, months(t, f.live), 2, "the statistics stay")
}

func TestGiveBackNotTaken(t *testing.T) {
	newFixture(t)

	_, err := GiveBack(context.Background(), DefaultItems)
	assert.ErrorIs(t, err, ErrNotTaken)
	_, err = GetSummary(context.Background())
	assert.ErrorIs(t, err, ErrNotTaken)
}

func TestGiveBackRollsBack(t *testing.T) {
	f := newFixture(t)
	taken(t, f)

	addSession(t, f.dev, "lp1", t0.Add(time.Hour), 9)
	addSlot(t, f.dev, "grid", "grid", t0.Add(slot), 8)
	exec(t, f.dev, "DROP TABLE tariffs_eeg") // the last step of the energy statistics fails

	before := count(t, f.live, "SELECT COUNT(*) FROM meters") + count(t, f.live, "SELECT COUNT(*) FROM sessions")

	_, err := GiveBack(context.Background(), DefaultItems)
	require.Error(t, err)

	assert.Equal(t, before, count(t, f.live, "SELECT COUNT(*) FROM meters")+count(t, f.live, "SELECT COUNT(*) FROM sessions"), "nothing changed")
	assert.Equal(t, "", setting(t, f.live, DevTransferApplied))
	assert.FileExists(t, f.livePath+BackupSuffix, "the backup was made before")
}

func TestGiveBackLocked(t *testing.T) {
	f := newFixture(t)
	taken(t, f)
	addSession(t, f.dev, "lp1", t0.Add(time.Hour), 9)

	// evcc custom holds the write lock
	d, err := openFile(f.livePath)
	require.NoError(t, err)
	defer d.Close()
	conn, err := d.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	require.NoError(t, err)

	before := count(t, f.live, "SELECT COUNT(*) FROM sessions")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = GiveBack(ctx, DefaultItems)
	assert.ErrorIs(t, err, ErrLocked)

	_, err = conn.ExecContext(context.Background(), "ROLLBACK")
	require.NoError(t, err)
	assert.Equal(t, before, count(t, f.live, "SELECT COUNT(*) FROM sessions"))
}

func TestSummary(t *testing.T) {
	f := newFixture(t)
	taken(t, f)

	addSlot(t, f.dev, "grid", "grid", t0.Add(-slot), 1)
	addSlot(t, f.dev, "grid", "grid", t0, 1)
	addSlot(t, f.dev, "grid", "grid", t0.Add(slot), 1)
	addSlot(t, f.dev, "battery", "battery", t0.Add(slot), 1)
	addSession(t, f.dev, "lp1", t0.Add(time.Hour), 1)
	addSession(t, f.dev, "lp1", t0.Add(-time.Hour), 1)
	peak(t, f.dev, month("2026-11", 100), month("2026-10", 100))

	res, err := GetSummary(context.Background())
	require.NoError(t, err)
	assert.Equal(t, t0.Unix(), res.Since.Unix())
	assert.Equal(t, 1, res.Sessions)
	assert.Equal(t, 2, res.Slots, "distinct quarter hours")
	assert.Equal(t, 1, res.Months)
}

func meterReading(t *testing.T, g *gorm.DB, name string) float64 {
	t.Helper()
	var v float64
	require.NoError(t, g.Raw("SELECT energy_meter FROM entities WHERE name = ?", name).Scan(&v).Error)
	return v
}

// TestGiveBackReadings: the last absolute readings of an entity follow only if the
// test version recorded later than the real database
func TestGiveBackReadings(t *testing.T) {
	f := newFixture(t)
	taken(t, f)

	for _, g := range []*gorm.DB{f.live, f.dev} {
		addSlot(t, g, "grid", "grid", t0, 1)
		addSlot(t, g, "pv", "pv", t0, 1)
	}
	exec(t, f.live, "UPDATE entities SET energy_meter = 100")
	exec(t, f.dev, "UPDATE entities SET energy_meter = 50")

	// the test version recorded later for pv, the real one for grid
	addSlot(t, f.dev, "pv", "pv", t0.Add(slot), 1)
	addSlot(t, f.live, "grid", "grid", t0.Add(2*slot), 1)
	addSlot(t, f.dev, "grid", "grid", t0.Add(slot), 1)

	_, err := GiveBack(context.Background(), Items{Energy: true})
	require.NoError(t, err)

	assert.InDelta(t, 50, meterReading(t, f.live, "pv"), 1e-9, "the test version recorded later")
	assert.InDelta(t, 100, meterReading(t, f.live, "grid"), 1e-9, "the real one recorded later")
}
