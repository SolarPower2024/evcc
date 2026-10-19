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
	assert.Len(t, routes, 3)
}

func TestEegPriceRoutes(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	utc := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = utc })

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
