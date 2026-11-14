package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/util/auth"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAddCustomDbRoutesInPlace: the hook sits in the same block as ensureDbAuth,
// so the routes are behind the protected group
func TestAddCustomDbRoutesInPlace(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "http.go", nil, 0)
	require.NoError(t, err)

	calls := func(b *ast.BlockStmt, name string) bool {
		var found bool
		for _, st := range b.List {
			ast.Inspect(st, func(n ast.Node) bool {
				if _, ok := n.(*ast.BlockStmt); ok {
					return false
				}
				if id, ok := n.(*ast.Ident); ok && id.Name == name {
					found = true
				}
				return true
			})
		}
		return found
	}

	var guarded, hooked int
	ast.Inspect(f, func(n ast.Node) bool {
		if b, ok := n.(*ast.BlockStmt); ok && calls(b, "addCustomDbRoutes") {
			hooked++
			if calls(b, "ensureDbAuth") {
				guarded++
			}
		}
		return true
	})

	assert.Equal(t, 1, hooked, "addCustomDbRoutes must be called once")
	assert.Equal(t, 1, guarded, "addCustomDbRoutes must sit in the block of ensureDbAuth")
}

// TestCustomDbRoutesNoCollision: the routes are added, none is left out
func TestCustomDbRoutesNoCollision(t *testing.T) {
	routes := map[string]route{
		"deletetariffs": {"DELETE", "/metrics/tariffs", deleteTariffsHandler},
	}
	addCustomDbRoutes(routes)

	for name := range customDbRoutes() {
		assert.Contains(t, routes, name)
	}
	assert.Len(t, routes, 1+len(customDbRoutes()))
}

func TestEegPriceRoutes(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	slot := func(n int) time.Time { return base.Add(time.Duration(n) * 15 * time.Minute) }

	for n := range 4 {
		require.NoError(t, metrics.PersistEegPrice(slot(n), 0))
	}

	r := mux.NewRouter()
	for _, rt := range customDbRoutes() {
		r.Methods(rt.Method).Path(rt.Pattern).Handler(rt.HandlerFunc)
	}

	call := func(method string, q url.Values) (int, string) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/metrics/tariffs/eeg?"+q.Encode(), nil))
		return w.Code, w.Body.String()
	}
	rng := func(from, to int) url.Values {
		return url.Values{"from": {slot(from).Format(time.RFC3339)}, "to": {slot(to).Format(time.RFC3339)}}
	}
	prices := func() map[int64]float64 {
		var rows []struct {
			Ts    int64
			Price float64
		}
		require.NoError(t, db.Instance.Table("tariffs_eeg").Find(&rows).Error)
		res := make(map[int64]float64)
		for _, r := range rows {
			res[r.Ts] = r.Price
		}
		return res
	}

	// set slots 1 and 2 only ([from,to))
	q := rng(1, 3)
	q.Set("price", "0.082")
	code, _ := call(http.MethodPut, q)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[int64]float64{slot(0).Unix(): 0, slot(1).Unix(): 0.082, slot(2).Unix(): 0.082, slot(3).Unix(): 0}, prices())

	// missing bound, no or negative or not finite price
	for _, q := range []url.Values{
		{"to": {slot(3).Format(time.RFC3339)}, "price": {"0.1"}},
		{"from": {slot(1).Format(time.RFC3339)}, "price": {"0.1"}},
		func() url.Values { q := rng(0, 4); return q }(),
		func() url.Values { q := rng(0, 4); q.Set("price", "-0.01"); return q }(),
		func() url.Values { q := rng(0, 4); q.Set("price", "Inf"); return q }(),
		func() url.Values { q := rng(0, 4); q.Set("price", "NaN"); return q }(),
	} {
		code, _ := call(http.MethodPut, q)
		assert.Equal(t, http.StatusBadRequest, code, q.Encode())
	}
	assert.Equal(t, 0.082, prices()[slot(1).Unix()], "rejected requests change nothing")

	code, _ = call(http.MethodDelete, url.Values{"from": {slot(0).Format(time.RFC3339)}})
	assert.Equal(t, http.StatusBadRequest, code)

	// delete slot 0 and 1
	code, body := call(http.MethodDelete, rng(0, 2))
	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"deleted":2}`, body)
	assert.Equal(t, map[int64]float64{slot(2).Unix(): 0.082, slot(3).Unix(): 0}, prices())
}

// TestFeedInRoutes: the summary shows what the deletion removes without changing
// it, the deletion sets the feed-in of the range to 0, and both need the admin
// password
func TestFeedInRoutes(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	_, err := metrics.NewCollector(metrics.Grid, "grid", "grid")
	require.NoError(t, err)

	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	slot := func(n int) time.Time { return base.Add(time.Duration(n) * 15 * time.Minute) }
	require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy, return_energy)
		SELECT id, ?, 2, 3 FROM entities WHERE "group" = 'grid'`, slot(0).Unix()).Error)
	require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy, return_energy)
		SELECT id, ?, 2, 3 FROM entities WHERE "group" = 'grid'`, slot(1).Unix()).Error)

	const pw = "secret"
	authed := fakeAuth{mode: auth.Enabled, password: pw}

	r := mux.NewRouter()
	sub := r.PathPrefix("/db").Subrouter()
	sub.Use(ensureDbAuth(authed))
	for _, rt := range customDbRoutes() {
		sub.Methods(rt.Method).Path(rt.Pattern).Handler(rt.HandlerFunc)
	}

	call := func(method, password string, from, to int) (int, string) {
		q := url.Values{"from": {slot(from).Format(time.RFC3339)}, "to": {slot(to).Format(time.RFC3339)}}
		req := httptest.NewRequest(method, "/db/metrics/feedin?"+q.Encode(), nil)
		if password != "" {
			req.Header.Set("X-Admin-Password", password)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	// without the password nothing happens
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		code, _ := call(method, "", 0, 2)
		assert.Equal(t, http.StatusUnauthorized, code, method)
		code, _ = call(method, "wrong", 0, 2)
		assert.Equal(t, http.StatusUnauthorized, code, method)
	}

	code, body := call(http.MethodGet, pw, 0, 2)
	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"energy":6,"revenue":0}`, body)

	// the second slot only
	code, body = call(http.MethodDelete, pw, 1, 2)
	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"deleted":1}`, body)

	_, body = call(http.MethodGet, pw, 0, 2)
	assert.JSONEq(t, `{"energy":3,"revenue":0}`, body)

	// the draw is as it was
	var energy float64
	require.NoError(t, db.Instance.Raw(`SELECT SUM(energy) FROM meters`).Scan(&energy).Error)
	assert.Equal(t, 4.0, energy)

	// a range without sense
	code, _ = call(http.MethodDelete, pw, 2, 0)
	assert.Equal(t, http.StatusBadRequest, code)
}
