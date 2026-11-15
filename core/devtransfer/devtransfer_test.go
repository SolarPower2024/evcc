package devtransfer

import (
	"context"
	"database/sql"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	_ "github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/db"
	_ "github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// the test time starts at 18:00 on the 14th, the real database stops recording there
var t0 = time.Date(2026, 11, 14, 18, 0, 0, 0, time.UTC)

// newDB creates a database with the schema of the fork and keeps its connection
// open (so its log is not checkpointed), db.FilePath is the last one made
func newDB(t *testing.T, dir, name string) (string, *gorm.DB) {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, db.NewInstance("sqlite", path))
	g := db.Instance

	require.NoError(t, g.Exec(`CREATE TABLE IF NOT EXISTS tariffs_eeg (ts integer, price real)`).Error)
	require.NoError(t, g.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS tariffs_eeg_ts ON tariffs_eeg(ts)`).Error)
	require.NoError(t, g.AutoMigrate(new(session.Session)))

	t.Cleanup(func() {
		if d, err := g.DB(); err == nil {
			d.Close()
		}
	})

	return path, g
}

func exec(t *testing.T, g *gorm.DB, q string, args ...any) {
	t.Helper()
	require.NoError(t, g.Exec(q, args...).Error)
}

func count(t *testing.T, g *gorm.DB, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, g.Raw(q, args...).Scan(&n).Error)
	return n
}

func setting(t *testing.T, g *gorm.DB, key string) string {
	t.Helper()
	var v sql.NullString
	require.NoError(t, g.Raw("SELECT value FROM settings WHERE key = ?", key).Scan(&v).Error)
	return v.String
}

func addSession(t *testing.T, g *gorm.DB, lp string, created time.Time, kwh float64) {
	t.Helper()
	exec(t, g, "INSERT INTO sessions (loadpoint, created, finished, charged_kwh) VALUES (?, ?, ?, ?)", lp, created, created.Add(time.Hour), kwh)
}

func addSlot(t *testing.T, g *gorm.DB, group, name string, ts time.Time, energy float64) {
	t.Helper()
	exec(t, g, `INSERT OR IGNORE INTO entities ("group", name, title) VALUES (?, ?, ?)`, group, name, name)
	exec(t, g, `INSERT INTO meters (meter, ts, energy) SELECT id, ?, ? FROM entities WHERE "group" = ? AND name = ?`, ts.Unix(), energy, group, name)
}

func months(t *testing.T, g *gorm.DB) map[string]float64 {
	t.Helper()
	var list []map[string]any
	require.NoError(t, json.Unmarshal([]byte(setting(t, g, keys.PeakMonths)), &list))
	res := make(map[string]float64)
	for _, m := range list {
		res[m["month"].(string)] = m["peak"].(float64)
	}
	return res
}

func peak(t *testing.T, g *gorm.DB, v ...any) {
	t.Helper()
	b, _ := json.Marshal(v)
	exec(t, g, "INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", keys.PeakMonths, string(b))
}

func month(m string, p float64) map[string]any {
	return map[string]any{"month": m, "peak": p, "demand": p, "interventions": 1}
}

// fixture is a real database with a recording that stops at t0 and a test database
type fixture struct {
	livePath, devPath string
	live, dev         *gorm.DB
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()

	f := new(fixture)
	f.livePath, f.live = newDB(t, filepath.Join(dir, "live"), "evcc.db")
	f.devPath, f.dev = newDB(t, filepath.Join(dir, "dev"), "evcc.db") // own database from here on

	t.Setenv(EnvVar, filepath.Join(dir, "live", "*.db"))

	// the real one: slots and a session until t0, devices and a setting
	addSlot(t, f.live, "grid", "grid", t0.Add(-slot), 1)
	addSlot(t, f.live, "grid", "grid", t0.Add(-2*slot), 2)
	addSession(t, f.live, "lp1", t0.Add(-3*time.Hour), 5)
	exec(t, f.live, "INSERT INTO configs (class, type, value) VALUES ('meter', 'live-meter', '{}')")
	exec(t, f.live, "INSERT INTO settings (key, value) VALUES (?, ?)", keys.LmPriorities, "live")
	exec(t, f.live, "INSERT INTO settings (key, value) VALUES (?, ?)", "title", "Live")
	peak(t, f.live, month("2026-11", 4000), month("2026-10", 3000))

	return f
}

func TestDisabled(t *testing.T) {
	t.Setenv(EnvVar, "")
	assert.False(t, Enabled())
	_, err := LivePath()
	assert.ErrorIs(t, err, ErrDisabled)

	assert.ErrorIs(t, Takeover(context.Background(), nil), ErrDisabled)
	_, err = GetTakeoverInfo()
	assert.ErrorIs(t, err, ErrDisabled)
}

func TestLivePathGlob(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvVar, filepath.Join(dir, "*", "evcc.db"))

	_, err := LivePath()
	assert.ErrorIs(t, err, ErrNotFound, "no match")

	for _, d := range []string{"a_evcc_custom", "b_evcc_custom"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, d, "evcc.db"), nil, 0o600))
	}
	_, err = LivePath()
	assert.ErrorIs(t, err, ErrNotFound, "two matches")

	require.NoError(t, os.RemoveAll(filepath.Join(dir, "b_evcc_custom")))
	p, err := LivePath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "a_evcc_custom", "evcc.db"), p)
}

func TestTakeover(t *testing.T) {
	f := newFixture(t)

	// the test version recorded before the take over: old data (before t0), and new data
	addSlot(t, f.dev, "grid", "grid", t0.Add(-slot), 99) // replaced by the real one
	addSlot(t, f.dev, "grid", "grid", t0, 7)
	addSlot(t, f.dev, "grid", "grid", t0.Add(slot), 8)
	addSession(t, f.dev, "lp1", t0.Add(30*time.Minute), 3)
	addSession(t, f.dev, "lp1", t0.Add(-5*time.Hour), 11)
	exec(t, f.dev, "INSERT INTO configs (class, type, value) VALUES ('meter', 'dev-meter', '{}')")
	peak(t, f.dev, month("2026-11", 5000))

	// the real one crashed: its latest changes are only in the log of the open connection
	require.FileExists(t, f.livePath+"-wal")

	var backups int
	require.NoError(t, Takeover(context.Background(), func(context.Context) error { backups++; return nil }))
	assert.Equal(t, 1, backups)

	since, ok := Since()
	require.True(t, ok)
	assert.Equal(t, t0.Unix(), since.Unix(), "the end of the last quarter hour of the real file")

	// the database of the test version is now the real one
	raw, err := openFile(f.devPath)
	require.NoError(t, err)
	defer raw.Close()
	g := gormOn(t, raw)

	assert.Equal(t, 1, count(t, g, "SELECT COUNT(*) FROM configs WHERE type = 'live-meter'"), "devices of the real file")
	assert.Equal(t, 0, count(t, g, "SELECT COUNT(*) FROM configs WHERE type = 'dev-meter'"))
	assert.Equal(t, "Live", setting(t, g, "title"))
	assert.Equal(t, 4, count(t, g, "SELECT COUNT(*) FROM meters"), "two of the real file, two since t0 of the test version, the old one is replaced")
	assert.Equal(t, 1, count(t, g, "SELECT COUNT(*) FROM meters WHERE ts = ? AND energy = 1", t0.Add(-slot).Unix()))
	assert.Equal(t, 0, count(t, g, "SELECT COUNT(*) FROM meters WHERE energy = 99"))
	assert.Equal(t, 2, count(t, g, "SELECT COUNT(*) FROM sessions"), "the real one and the one since t0")
	assert.Equal(t, map[string]float64{"2026-11": 5000, "2026-10": 3000}, months(t, g), "the higher peak of the month")
}

// gormOn wraps an open database for the helpers
func gormOn(t *testing.T, d *sql.DB) *gorm.DB {
	t.Helper()
	g, err := gormOpen(d)
	require.NoError(t, err)
	return g
}

func TestTakeoverCopiesLog(t *testing.T) {
	f := newFixture(t)

	// the row is in the log of the open connection only
	addSlot(t, f.live, "grid", "grid", t0.Add(-3*slot), 3)
	fi, err := os.Stat(f.livePath + "-wal")
	require.NoError(t, err)
	require.NotZero(t, fi.Size())

	tmp := filepath.Join(t.TempDir(), "copy.db")
	require.NoError(t, backupFile(context.Background(), f.livePath, tmp))

	d, err := openFile(tmp)
	require.NoError(t, err)
	defer d.Close()

	var n int
	require.NoError(t, d.QueryRow("SELECT COUNT(*) FROM meters").Scan(&n))
	assert.Equal(t, 3, n)
}

func TestSinceIsNewest(t *testing.T) {
	f := newFixture(t)

	ctx := context.Background()
	d, err := openFile(f.livePath)
	require.NoError(t, err)
	defer d.Close()

	got, err := latest(ctx, d, "")
	require.NoError(t, err)
	assert.Equal(t, t0.Unix(), got.Unix(), "end of the last quarter hour")

	// a session created later than the last slot's end
	late := t0.Add(20 * time.Minute)
	addSession(t, f.live, "lp1", late, 1)
	got, err = latest(ctx, d, "")
	require.NoError(t, err)
	assert.WithinDuration(t, late, got, time.Second)
}

func TestParseItems(t *testing.T) {
	it, err := ParseItems("sessions,peak")
	require.NoError(t, err)
	assert.Equal(t, Items{Sessions: true, Peak: true}, it)
	assert.Equal(t, []string{"sessions", "peak"}, it.Names())

	_, err = ParseItems("")
	assert.Error(t, err)
	_, err = ParseItems("foo")
	assert.Error(t, err)
}

func TestRestartWay(t *testing.T) {
	t.Setenv("SUPERVISOR_TOKEN", "")
	assert.Equal(t, RestartExit, RestartWay())
	t.Setenv("SUPERVISOR_TOKEN", "x")
	assert.Equal(t, RestartSupervisor, RestartWay())
}

// TestForkKeysClassified: every key of core/keys/site_custom.go is a setting or a
// state, a new key has to be put in one of the lists in keys.go
func TestForkKeysClassified(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "../keys/site_custom.go", nil, 0)
	require.NoError(t, err)

	var consts int
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			lit, ok := spec.Values[i].(*ast.BasicLit)
			require.True(t, ok, name.Name)
			val, err := strconv.Unquote(lit.Value)
			require.NoError(t, err)

			consts++
			inSettings, inState := contains(settingKeys, val), contains(stateKeys, val)
			assert.True(t, inSettings != inState, "%s (%s) must be in exactly one of settingKeys and stateKeys of core/devtransfer/keys.go", name.Name, val)
		}
		return true
	})
	assert.Greater(t, consts, 50)
}

// TestRestartSupervisor: with a token the supervisor is asked, the process ends
// only if it refuses or there is no token
func TestRestartSupervisor(t *testing.T) {
	var auth string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		auth = r.Header.Get("Authorization")
		assert.Equal(t, http.MethodPost, r.Method)
		if r.URL.Path == "/refuse" {
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()

	exits := 0
	oldExit, oldURL := Exit, supervisorURL
	Exit = func() { exits++ }
	t.Cleanup(func() { Exit, supervisorURL = oldExit, oldURL })

	t.Setenv("SUPERVISOR_TOKEN", "")
	Restart(context.Background())
	assert.Equal(t, 1, exits, "no token: ends")
	assert.Zero(t, hits)

	t.Setenv("SUPERVISOR_TOKEN", "tok")
	supervisorURL = srv.URL + "/addons/self/restart"
	Restart(context.Background())
	assert.Equal(t, 1, exits, "the supervisor restarts")
	assert.Equal(t, 1, hits)
	assert.Equal(t, "Bearer tok", auth)

	supervisorURL = srv.URL + "/refuse"
	Restart(context.Background())
	assert.Equal(t, 2, exits, "refused: ends")
}
