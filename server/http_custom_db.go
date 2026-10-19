package server

// Custom extension: routes of the fork in the protected api/db group, see
// core/metrics/feedin_eeg_custom.go.

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/util"
)

// customDbRoutes are the fork's routes below api/db: the stored EEG prices of a
// time range can be deleted or set anew
func customDbRoutes() map[string]route {
	return map[string]route{
		"deleteeegprices": {"DELETE", "/metrics/tariffs/eeg", deleteEegPricesHandler},
		"seteegprices":    {"PUT", "/metrics/tariffs/eeg", setEegPricesHandler},
	}
}

// addCustomDbRoutes adds the fork's db routes to upstream's route table, a
// colliding route stays upstream's (see addCustomSiteRoutes)
func addCustomDbRoutes(routes map[string]route) {
	for _, name := range mergeRoutes(routes, customDbRoutes()) {
		util.NewLogger("httpd").ERROR.Printf("custom route %s collides with an evcc route, left out", name)
	}
}

// deleteEegPricesHandler removes the stored EEG prices of from/to
func deleteEegPricesHandler(w http.ResponseWriter, r *http.Request) {
	if db.Instance == nil {
		jsonError(w, http.StatusBadRequest, errors.New("database offline"))
		return
	}

	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	rows, err := metrics.DeleteEegPrices(from, to)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err)
		return
	}

	jsonWrite(w, deleteResult{rows})
}

// setEegPricesHandler sets the stored EEG prices of from/to to price (currency
// per kWh)
func setEegPricesHandler(w http.ResponseWriter, r *http.Request) {
	if db.Instance == nil {
		jsonError(w, http.StatusBadRequest, errors.New("database offline"))
		return
	}

	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	price, err := strconv.ParseFloat(r.URL.Query().Get("price"), 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, errors.New("invalid 'price' parameter"))
		return
	}

	rows, err := metrics.SetEegPrices(from, to, price)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, metrics.ErrInvalidPrice) {
			status = http.StatusBadRequest
		}
		jsonError(w, status, err)
		return
	}

	jsonWrite(w, deleteResult{rows})
}
