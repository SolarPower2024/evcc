package server

// Custom extension: the site routes added by this fork. They live here rather
// than in the route table in http.go, so that upstream changes to that table
// merge without conflicts.

import (
	"net/http"
	"strconv"

	"github.com/evcc-io/evcc/core/site"
	"github.com/gorilla/mux"
)

// namePattern is evcc's own name rule, see nameRE in cmd/setup.go. It includes
// the colon of device names like db:3.
const namePattern = "[a-zA-Z0-9_.:-]+"

func customSiteRoutes(site site.API) map[string]route {
	return map[string]route{
		// soc-based grid charging, see core/site_lm.go
		"batterysocgridcharge":      {"POST", "/batterysocgridcharge/{value:[01truefalse]+}", boolHandler(site.SetBatterySocGridCharge, site.GetBatterySocGridCharge)},
		"batterysocgridchargestart": {"POST", "/batterysocgridchargestart/{value:[0-9.]+}", floatHandler(site.SetBatterySocGridChargeStart, site.GetBatterySocGridChargeStart)},
		"batterysocgridchargestop":  {"POST", "/batterysocgridchargestop/{value:[0-9.]+}", floatHandler(site.SetBatterySocGridChargeStop, site.GetBatterySocGridChargeStop)},

		// load management shed priorities, see core/site_lm.go
		"lmpriority": {"POST", "/lmpriority/{name:" + namePattern + "}/{value:[0-9]+}", lmPriorityHandler(site)},

		// peak shaving, see core/site_peakshaving.go
		"peakshaving":                   {"POST", "/peakshaving/{value:[01truefalse]+}", boolHandler(site.SetPeakShaving, site.GetPeakShaving)},
		"peakshavinglimit":              {"POST", "/peakshavinglimit/{value:[0-9.]+}", floatHandler(site.SetPeakShavingLimit, site.GetPeakShavingLimit)},
		"peakshavingreserve":            {"POST", "/peakshavingreserve/{value:[0-9.]+}", floatHandler(site.SetPeakShavingReserve, site.GetPeakShavingReserve)},
		"peakshavingentity":             {"POST", "/peakshavingentity/{value:[a-zA-Z0-9_.]+}", stringHandler(site.SetPeakShavingEntity, site.GetPeakShavingEntity)},
		"peakshavingentitydelete":       {"DELETE", "/peakshavingentity", stringHandler(site.SetPeakShavingEntity, site.GetPeakShavingEntity)},
		"peakshavingchargeentity":       {"POST", "/peakshavingchargeentity/{value:[a-zA-Z0-9_.]+}", stringHandler(site.SetPeakShavingChargeEntity, site.GetPeakShavingChargeEntity)},
		"peakshavingchargeentitydelete": {"DELETE", "/peakshavingchargeentity", stringHandler(site.SetPeakShavingChargeEntity, site.GetPeakShavingChargeEntity)},
		"peakshavingchargepower":        {"POST", "/peakshavingchargepower/{value:[0-9.]+}", floatHandler(site.SetPeakShavingChargePower, site.GetPeakShavingChargePower)},
		"peakshavingcircuit":            {"POST", "/peakshavingcircuit/{value:" + namePattern + "}", stringHandler(site.SetPeakShavingCircuit, site.GetPeakShavingCircuit)},
		"peakshavingcircuitdelete":      {"DELETE", "/peakshavingcircuit", stringHandler(site.SetPeakShavingCircuit, site.GetPeakShavingCircuit)},
	}
}

// lmPriorityHandler sets a load's shed priority
func lmPriorityHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		prio, err := strconv.Atoi(vars["value"])
		if err == nil {
			err = site.SetLmPriority(vars["name"], prio)
		}

		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, prio)
	}
}
