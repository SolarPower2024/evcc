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
// time range can be deleted or set anew, the feed-in of a time range can be read
// and set to 0 (Debug, see core/metrics/feedin_clear_custom.go)
func customDbRoutes() map[string]route {
	return map[string]route{
		"deleteeegprices": {"DELETE", "/metrics/tariffs/eeg", deleteEegPricesHandler},
		"seteegprices":    {"PUT", "/metrics/tariffs/eeg", setEegPricesHandler},
		"feedinsummary":   {"GET", "/metrics/feedin", feedInSummaryHandler},
		"deletefeedin":    {"DELETE", "/metrics/feedin", deleteFeedInHandler},
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

// feedInSummaryResult is what a deletion of the feed-in would remove
type feedInSummaryResult struct {
	Energy  float64 `json:"energy"`  // kWh
	Revenue float64 `json:"revenue"` // currency
}

// feedInSummaryHandler answers the feed-in of from/to without changing it: the
// preview of deleteFeedInHandler
func feedInSummaryHandler(w http.ResponseWriter, r *http.Request) {
	if db.Instance == nil {
		jsonError(w, http.StatusBadRequest, errors.New("database offline"))
		return
	}

	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	energy, revenue, err := metrics.FeedInSummary(from, to)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	jsonWrite(w, feedInSummaryResult{energy, revenue})
}

// deleteFeedInHandler sets the feed-in of from/to to 0 kWh, draw and home
// consumption stay
func deleteFeedInHandler(w http.ResponseWriter, r *http.Request) {
	if db.Instance == nil {
		jsonError(w, http.StatusBadRequest, errors.New("database offline"))
		return
	}

	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	rows, err := metrics.ClearFeedIn(from, to)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	// invalidate cached values derived from the history, as evcc's own deletion does
	util.ResetCached()

	jsonWrite(w, deleteResult{rows})
}
