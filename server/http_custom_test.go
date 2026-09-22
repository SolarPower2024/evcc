package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
