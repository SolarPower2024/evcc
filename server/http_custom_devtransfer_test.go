package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/devtransfer"
	_ "github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func devTransferRouter() *mux.Router {
	r := mux.NewRouter()
	for _, rt := range devTransferRoutes() {
		r.Methods(rt.Method).Path(rt.Pattern).Handler(rt.HandlerFunc)
	}
	r.Methods(http.MethodGet).Path("/devtransfer").HandlerFunc(devTransferStatusHandler)
	return r
}

func devTransferCall(r http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

// newDevTransferDB makes a database with the schema of the fork
func newDevTransferDB(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, db.NewInstance("sqlite", path))
	require.NoError(t, db.Instance.Exec(`CREATE TABLE IF NOT EXISTS tariffs_eeg (ts integer, price real)`).Error)
	t.Cleanup(func() {
		if d, err := db.Instance.DB(); err == nil {
			d.Close()
		}
	})
}

// TestDevTransferRoutesOff: without the variable, file or take over the routes
// refuse with 409 and change nothing, the status names what is the case
func TestDevTransferRoutesOff(t *testing.T) {
	dir := t.TempDir()
	newDevTransferDB(t, filepath.Join(dir, "own", "evcc.db"))
	r := devTransferRouter()

	exits := 0
	old := devtransfer.Exit
	devtransfer.Exit = func() { exits++ }
	t.Cleanup(func() { devtransfer.Exit = old })

	// without the variable
	t.Setenv(devtransfer.EnvVar, "")
	for _, c := range []struct{ method, target string }{
		{http.MethodGet, "/devtransfer/giveback"},
		{http.MethodPost, "/devtransfer/giveback"},
		{http.MethodPost, "/devtransfer/takeover"},
	} {
		assert.Equal(t, http.StatusConflict, devTransferCall(r, c.method, c.target).Code, c.method+" "+c.target)
	}
	assert.Zero(t, exits)

	w := devTransferCall(r, http.MethodGet, "/devtransfer")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"enabled":false}`, w.Body.String())

	// the variable, no file
	t.Setenv(devtransfer.EnvVar, filepath.Join(dir, "live", "*.db"))
	for _, c := range []struct{ method, target string }{
		{http.MethodGet, "/devtransfer/giveback"},
		{http.MethodPost, "/devtransfer/giveback"},
		{http.MethodPost, "/devtransfer/takeover"},
	} {
		assert.Equal(t, http.StatusConflict, devTransferCall(r, c.method, c.target).Code, c.method+" "+c.target)
	}
	w = devTransferCall(r, http.MethodGet, "/devtransfer")
	var st devTransferStatusResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &st))
	assert.True(t, st.Enabled)
	assert.True(t, st.NotFound)
	assert.Nil(t, st.Since)

	// the file, nothing taken over
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "live"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "live", "evcc.db"), nil, 0o600))
	for _, c := range []struct{ method, target string }{
		{http.MethodGet, "/devtransfer/giveback"},
		{http.MethodPost, "/devtransfer/giveback"},
	} {
		assert.Equal(t, http.StatusConflict, devTransferCall(r, c.method, c.target).Code, c.method+" "+c.target)
	}
	w = devTransferCall(r, http.MethodGet, "/devtransfer")
	st = devTransferStatusResult{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &st))
	assert.False(t, st.NotFound)
	assert.Contains(t, st.LivePath, "evcc.db")
	assert.Nil(t, st.Since)
	assert.Equal(t, devtransfer.RestartWay(), st.Restart)

	// a bad selection
	assert.Equal(t, http.StatusBadRequest, devTransferCall(r, http.MethodGet, "/devtransfer/giveback?items=foo").Code)
	assert.Equal(t, http.StatusBadRequest, devTransferCall(r, http.MethodPost, "/devtransfer/giveback?items=").Code)
}

// TestDevTransferRoutes: the take over restarts after the restore, afterwards
// the preview counts and the give back works
func TestDevTransferRoutes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SUPERVISOR_TOKEN", "")

	// the real database, its connection stays open
	newDevTransferDB(t, filepath.Join(dir, "live", "evcc.db"))
	live := db.Instance
	t0 := time.Now().Add(-3 * time.Hour).Truncate(15 * time.Minute)
	require.NoError(t, live.Exec(`INSERT INTO entities ("group", name, title) VALUES ('grid', 'grid', 'grid')`).Error)
	require.NoError(t, live.Exec(`INSERT INTO meters (meter, ts, energy) SELECT id, ?, 1 FROM entities WHERE name = 'grid'`, t0.Add(-15*time.Minute).Unix()).Error)

	// the test version
	newDevTransferDB(t, filepath.Join(dir, "dev", "evcc.db"))
	t.Setenv(devtransfer.EnvVar, filepath.Join(dir, "live", "*.db"))

	exits := 0
	old := devtransfer.Exit
	devtransfer.Exit = func() { exits++ }
	t.Cleanup(func() { devtransfer.Exit = old })

	r := devTransferRouter()

	// the take over does not close the connection in a way the test could not
	// reopen: it ends with the restart, and the since file is written
	w := devTransferCall(r, http.MethodPost, "/devtransfer/takeover")
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, 1, exits, "the restart comes after the restore")

	// the database holds what was taken over: the restore ran before the restart
	assert.FileExists(t, filepath.Join(dir, "dev", "evcc.db.since"))

	since, ok := devtransfer.Since()
	require.True(t, ok)
	assert.Equal(t, t0.Unix(), since.Unix())

	// the process would restart now: open the database as the new process does
	newDevTransferDB(t, filepath.Join(dir, "dev", "evcc.db"))
	require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy) SELECT id, ?, 2 FROM entities WHERE name = 'grid'`, t0.Unix()).Error)
	require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy) SELECT id, ?, 3 FROM entities WHERE name = 'grid'`, t0.Add(15*time.Minute).Unix()).Error)

	w = devTransferCall(r, http.MethodGet, "/devtransfer/giveback?items=energy")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var sum devTransferSummaryResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &sum))
	assert.Equal(t, 2, sum.Slots)
	assert.Equal(t, 0, sum.Sessions)
	assert.Equal(t, devtransfer.Items{Energy: true}, sum.Items)

	w = devTransferCall(r, http.MethodGet, "/devtransfer")
	var st devTransferStatusResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &st))
	require.NotNil(t, st.Since)

	w = devTransferCall(r, http.MethodPost, "/devtransfer/giveback?items=energy")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var n int
	require.NoError(t, live.Raw("SELECT COUNT(*) FROM meters").Scan(&n).Error)
	assert.Equal(t, 3, n)
	assert.FileExists(t, filepath.Join(dir, "live", "evcc.db"+devtransfer.BackupSuffix))

	// the record is stored in the real database, which shows it as evcc custom
	var rec string
	require.NoError(t, live.Raw("SELECT value FROM settings WHERE key = ?", devtransfer.DevTransferApplied).Scan(&rec).Error)
	assert.Contains(t, rec, `"energy"`)

}
