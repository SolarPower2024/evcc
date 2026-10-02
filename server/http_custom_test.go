package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evcc-io/evcc/core/lm/profile"
	"github.com/evcc-io/evcc/core/site"
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
