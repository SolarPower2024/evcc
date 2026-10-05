package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evcc-io/evcc/core"
	"github.com/evcc-io/evcc/core/lm/profile"
	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/util/logstash"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

// TestNamePatternMatchesUiDevices is the regression test for device names like
// db:3, which the first version of the circuit route rejected with a 404
func TestNamePatternMatchesUiDevices(t *testing.T) {
	var got string

	r := mux.NewRouter()
	r.Methods(http.MethodPost).Path("/peakshavingcircuit/{value:" + namePattern + "}").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = mux.Vars(r)["value"]
	})

	for path, want := range map[string]string{
		"/peakshavingcircuit/hausanschluss": "hausanschluss",
		"/peakshavingcircuit/db:1":          "db:1",
		"/peakshavingcircuit/db%3A12":       "db:12", // as sent by encodeURIComponent
	} {
		got = ""
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))

		assert.Equal(t, http.StatusOK, w.Code, path)
		assert.Equal(t, want, got, path)
	}
}

// TestMergeRoutesKeepsUpstream: a custom route colliding with an upstream one by
// name or by method and path is left out, upstream's stays
func TestMergeRoutesKeepsUpstream(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {}
	routes := map[string]route{
		"limit": {"POST", "/limit/{value:[0-9]+}", upstream},
		"state": {"GET", "/state", upstream},
	}

	left := mergeRoutes(routes, map[string]route{
		"limit":     {"POST", "/other/{value:[0-9]+}", nil}, // same name
		"mystate":   {"GET", "/state", nil},                 // same method and path
		"poststate": {"POST", "/state", nil},                // other method: fine
		"new":       {"POST", "/new/{value:[0-9]+}", nil},   // fine
	})

	assert.Equal(t, []string{"limit", "mystate"}, left)
	assert.Equal(t, "/limit/{value:[0-9]+}", routes["limit"].Pattern)
	assert.NotNil(t, routes["limit"].HandlerFunc)
	assert.NotContains(t, routes, "mystate")
	assert.Contains(t, routes, "poststate")
	assert.Contains(t, routes, "new")
}

// profileSite records the profile the handler saves
type profileSite struct {
	site.API
	saved int
}

func (s *profileSite) SaveLmProfile(p profile.Profile) (profile.Profile, error) {
	s.saved++
	return p, nil
}

// TestLmProfileBodyLimit: a profile is a few hundred bytes, an oversized body is
// refused before it is read into memory
func TestLmProfileBodyLimit(t *testing.T) {
	s := new(profileSite)
	h := lmProfileSaveHandler(s)

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/lmprofile", strings.NewReader(`{"name":"Sommer"}`)))
	assert.Equal(t, http.StatusOK, w.Code)

	big := `{"name":"` + strings.Repeat("x", 100<<10) + `"}`
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/lmprofile", strings.NewReader(big)))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, 1, s.saved)
}

// customRouteSamples is a request for every custom route. A route added to
// customSiteRoutes needs a sample here, TestCustomRoutesMatch checks that.
var customRouteSamples = map[string]string{
	"batterysocgridcharge":          "/batterysocgridcharge/true",
	"batterysocgridchargestart":     "/batterysocgridchargestart/25",
	"batterysocgridchargestop":      "/batterysocgridchargestop/85",
	"batterygridchargeonce":         "/batterygridchargeonce/80",
	"batterygridchargeonceuntil":    "/batterygridchargeonce/80/06:30",
	"batterygridchargeoncecancel":   "/batterygridchargeonce",
	"lmpriority":                    "/lmpriority/db%3A3/7",
	"lmshedguard":                   "/lmshedguard/10",
	"lmshedprotect":                 "/lmshedprotect/db:3/true",
	"lmenabled":                     "/lmenabled/false",
	"lmcircuit":                     "/lmcircuit/db:1",
	"lmcircuitdelete":               "/lmcircuit",
	"batteryidentuse":               "/batteryidentuse/true",
	"lmadvanced":                    "/lmadvanced/hysteresis/3",
	"lmhomeprofile":                 "/lmhomeprofile",
	"lmhomeprofiledelete":           "/lmhomeprofile",
	"lmhomeprofilecsv":              "/lmhomeprofile",
	"lmprofile":                     "/lmprofile",
	"lmprofiledelete":               "/lmprofile/abc1",
	"lmprofileapply":                "/lmprofile/abc1/apply",
	"feedineegentity":               "/feedineegentity/sensor.eeg_export",
	"feedineegentitydelete":         "/feedineegentity",
	"feedinsplit":                   "/feedinsplit?from=2026-09-01&to=2026-10-01&aggregate=day",
	"logfile":                       "/logfile",
	"logfileset":                    "/logfile",
	"snowcover":                     "/snowcover/true",
	"snowauto":                      "/snowauto/true",
	"peakshaving":                   "/peakshaving/false",
	"peakshavinglimit":              "/peakshavinglimit/7000",
	"peakfollow":                    "/peakfollow/true",
	"peakfollowbuffer":              "/peakfollowbuffer/1000",
	"peaktariff":                    "/peaktariff/price/40",
	"peakshavingreserve":            "/peakshavingreserve/40",
	"peakshavingentity":             "/peakshavingentity/input_number.peak",
	"peakshavingentitydelete":       "/peakshavingentity",
	"peakshavingchargeentity":       "/peakshavingchargeentity/input_number.charge",
	"peakshavingchargeentitydelete": "/peakshavingchargeentity",
	"peakshavingenergyentity":       "/peakshavingenergyentity/sensor.grid_import",
	"peakshavingenergyentitydelete": "/peakshavingenergyentity",
	"peakshavingchargepower":        "/peakshavingchargepower/4000",
	"peakshavingcircuit":            "/peakshavingcircuit/db:1",
	"peakshavingcircuitdelete":      "/peakshavingcircuit",
}

// TestCustomRoutesMatch sends a request to every custom route through a real
// site: each one must be matched by its own route and answered by its handler. Errors of the site
// without devices are fine here; the values that need none are checked as set.
func TestCustomRoutesMatch(t *testing.T) {
	site := core.NewSite()
	routes := customSiteRoutes(site)

	r := mux.NewRouter()
	for _, rt := range routes {
		r.Methods(rt.Methods()...).Path(rt.Pattern).Handler(rt.HandlerFunc)
	}

	for name, rt := range routes {
		sample, ok := customRouteSamples[name]
		if !assert.True(t, ok, "no sample for route %s", name) {
			continue
		}

		var body io.Reader
		switch name {
		case "lmprofile":
			body = strings.NewReader(`{"name":"Sommer","gridCharge":true}`)
		case "lmhomeprofile":
			body = strings.NewReader("zeit;01\n00:00;500\n")
		}

		req := httptest.NewRequest(rt.Method, sample, body)

		// the router finds this route, not another one
		var m mux.RouteMatch
		if assert.True(t, r.Match(req, &m), "%s %s", rt.Method, sample) {
			assert.Equal(t, rt.Pattern, must(m.Route.GetPathTemplate()), "%s %s", rt.Method, sample)
		}

		// and its handler answers
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	// parsed and applied
	assert.Equal(t, 10, site.GetLmShedGuard())
	assert.Equal(t, 7000.0, site.GetPeakShavingLimit())
	assert.Equal(t, 40.0, site.GetPeakShavingReserve())
	assert.Equal(t, 1000.0, site.GetPeakFollowBuffer())
	assert.Equal(t, 4000.0, site.GetPeakShavingChargePower())
	assert.False(t, site.GetLmEnabled())
	assert.True(t, site.GetSnowCover())
	assert.True(t, site.GetSnowAuto())
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestLogFileRoutes: GET and POST answer the state, an invalid level or retention is a 400
func TestLogFileRoutes(t *testing.T) {
	r := mux.NewRouter()
	for _, rt := range customSiteRoutes(core.NewSite()) {
		r.Methods(rt.Methods()...).Path(rt.Pattern).Handler(rt.HandlerFunc)
	}

	do := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/logfile", strings.NewReader(body)))
		return w
	}

	t.Cleanup(func() { _, _ = logstash.SetFile(logstash.DefaultFileConfig) })

	w := do(http.MethodGet, "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"enabled":false`)

	// disabled: stored, nothing written
	w = do(http.MethodPost, `{"enabled":false,"level":"WARN","days":7,"dir":"/ignored"}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"level":"warn"`)
	assert.Contains(t, w.Body.String(), `"days":7`)
	assert.NotContains(t, w.Body.String(), "ignored")

	for _, body := range []string{
		`{"enabled":true,"level":"loud","days":7}`,
		`{"enabled":true,"level":"info","days":0}`,
		`{"enabled":true,"level":"info","days":91}`,
		`{"enabled":true,"level":"info"}`,
		`not json`,
	} {
		assert.Equal(t, http.StatusBadRequest, do(http.MethodPost, body).Code, body)
	}
}
