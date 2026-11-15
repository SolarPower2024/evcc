package server

// Custom extension: routes of the test version's data exchange with evcc custom,
// see core/devtransfer. Without the environment variable of the test version the
// routes refuse with 409, only the status answers (it names the last give back).

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/evcc-io/evcc/core/devtransfer"
	"github.com/evcc-io/evcc/db"
)

// devTransferRoutes are the routes of the exchange below api/db; the status is in
// customSiteRoutes, so the Debug area reads it with the login alone
func devTransferRoutes() map[string]route {
	return map[string]route{
		"devtransfergiveback": {"GET", "/devtransfer/giveback", devTransferSummaryHandler},
		"devtransfertakeover": {"POST", "/devtransfer/takeover", devTransferTakeoverHandler},
		"devtransferreturn":   {"POST", "/devtransfer/giveback", devTransferGiveBackHandler},
	}
}

// devTransferStatusResult is what the Debug area shows of the exchange
type devTransferStatusResult struct {
	Enabled     bool                 `json:"enabled"`
	LivePath    string               `json:"livePath,omitempty"`
	LiveChanged *time.Time           `json:"liveChanged,omitempty"`
	NotFound    bool                 `json:"notFound,omitempty"` // the real database was not found
	Since       *time.Time           `json:"since,omitempty"`    // start of the test time, set once data was taken over
	Restart     string               `json:"restart,omitempty"`  // supervisor or exit
	Applied     *devtransfer.Applied `json:"applied,omitempty"`
}

func devTransferStatusHandler(w http.ResponseWriter, r *http.Request) {
	res := devTransferStatusResult{Enabled: devtransfer.Enabled()}

	if db.Instance != nil {
		if a, ok := devtransfer.LastApplied(); ok {
			res.Applied = &a
		}
	}

	if res.Enabled {
		res.Restart = devtransfer.RestartWay()

		if info, err := devtransfer.GetTakeoverInfo(); err != nil {
			res.NotFound = true
		} else {
			res.LivePath = info.Path
			res.LiveChanged = &info.Changed
		}

		if since, ok := devtransfer.Since(); ok {
			res.Since = &since
		}
	}

	jsonWrite(w, res)
}

// devTransferStatus is 409 for what the state does not allow, 400 for a bad
// request, 500 for a failing database
func devTransferStatus(err error) int {
	switch {
	case errors.Is(err, devtransfer.ErrDisabled), errors.Is(err, devtransfer.ErrNotFound),
		errors.Is(err, devtransfer.ErrNotTaken), errors.Is(err, devtransfer.ErrLocked):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// devTransferItems reads the selection, the give back starts with the default
func devTransferItems(r *http.Request) (devtransfer.Items, error) {
	s := r.URL.Query().Get("items")
	if !r.URL.Query().Has("items") {
		return devtransfer.DefaultItems, nil
	}
	return devtransfer.ParseItems(s)
}

// devTransferSummaryResult is the preview of a give back
type devTransferSummaryResult struct {
	devtransfer.Summary
	Items devtransfer.Items `json:"items"`
}

func devTransferSummaryHandler(w http.ResponseWriter, r *http.Request) {
	if db.Instance == nil {
		jsonError(w, http.StatusBadRequest, errors.New("database offline"))
		return
	}

	items, err := devTransferItems(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	if _, err := devtransfer.LivePath(); err != nil {
		jsonError(w, devTransferStatus(err), err)
		return
	}

	res, err := devtransfer.GetSummary(r.Context())
	if err != nil {
		jsonError(w, devTransferStatus(err), err)
		return
	}

	jsonWrite(w, devTransferSummaryResult{res, items})
}

func devTransferGiveBackHandler(w http.ResponseWriter, r *http.Request) {
	if db.Instance == nil {
		jsonError(w, http.StatusBadRequest, errors.New("database offline"))
		return
	}

	items, err := devTransferItems(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	res, err := devtransfer.GiveBack(r.Context(), items)
	if err != nil {
		jsonError(w, devTransferStatus(err), err)
		return
	}

	jsonWrite(w, res)
}

// devTransferTakeoverHandler replaces the database by a copy of evcc custom's
// and restarts like the restore of a database does
func devTransferTakeoverHandler(w http.ResponseWriter, r *http.Request) {
	if db.Instance == nil {
		jsonError(w, http.StatusBadRequest, errors.New("database offline"))
		return
	}

	if err := devtransfer.Takeover(r.Context(), createLocalDatabaseBackup); err != nil {
		jsonError(w, devTransferStatus(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	// flush before the restart, the process may end before the handler returns
	_ = http.NewResponseController(w).Flush()
	devtransfer.Restart(context.WithoutCancel(r.Context()))
}
