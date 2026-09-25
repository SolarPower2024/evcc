package server

// Custom extension: the site routes added by this fork. They live here rather
// than in the route table in http.go, so that upstream changes to that table
// merge without conflicts.

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/evcc-io/evcc/core/lm/profile"
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

		// load management shed guard, see core/site_lm_guard.go
		"lmshedguard":   {"POST", "/lmshedguard/{value:[0-9]+}", intHandler(site.SetLmShedGuard, site.GetLmShedGuard)},
		"lmshedprotect": {"POST", "/lmshedprotect/{name:" + namePattern + "}/{value:[01truefalse]+}", lmShedProtectHandler(site)},

		// advanced load management settings, see core/site_lm_advanced.go
		"lmadvanced": {"POST", "/lmadvanced/{name:[a-zA-Z]+}/{value:[0-9.]+}", lmAdvancedHandler(site)},

		// battery profiles, see core/site_lm_profiles.go
		"lmprofile":       {"POST", "/lmprofile", lmProfileSaveHandler(site)},
		"lmprofiledelete": {"DELETE", "/lmprofile/{id:[a-z0-9]+}", lmProfileHandler(site.DeleteLmProfile)},
		"lmprofileapply":  {"POST", "/lmprofile/{id:[a-z0-9]+}/apply", lmProfileHandler(site.ApplyLmProfile)},

		// feed-in price published after the fact, see core/site_feedin.go
		"feedinfinalize": {"POST", "/feedinfinalize/{month:[0-9]{4}-[0-9]{2}}/{value:[0-9.]+}", feedInFinalizeHandler(site)},

		// peak shaving, see core/site_peakshaving.go
		"peakshaving":                   {"POST", "/peakshaving/{value:[01truefalse]+}", boolHandler(site.SetPeakShaving, site.GetPeakShaving)},
		"peakshavinglimit":              {"POST", "/peakshavinglimit/{value:[0-9.]+}", floatHandler(site.SetPeakShavingLimit, site.GetPeakShavingLimit)},
		"peakshavingreserve":            {"POST", "/peakshavingreserve/{value:[0-9.]+}", floatHandler(site.SetPeakShavingReserve, site.GetPeakShavingReserve)},
		"peakshavingentity":             {"POST", "/peakshavingentity/{value:[a-zA-Z0-9_.]+}", stringHandler(site.SetPeakShavingEntity, site.GetPeakShavingEntity)},
		"peakshavingentitydelete":       {"DELETE", "/peakshavingentity", stringHandler(site.SetPeakShavingEntity, site.GetPeakShavingEntity)},
		"peakshavingchargeentity":       {"POST", "/peakshavingchargeentity/{value:[a-zA-Z0-9_.]+}", stringHandler(site.SetPeakShavingChargeEntity, site.GetPeakShavingChargeEntity)},
		"peakshavingchargeentitydelete": {"DELETE", "/peakshavingchargeentity", stringHandler(site.SetPeakShavingChargeEntity, site.GetPeakShavingChargeEntity)},
		"peakshavingenergyentity":       {"POST", "/peakshavingenergyentity/{value:[a-zA-Z0-9_.]+}", stringHandler(site.SetPeakShavingEnergyEntity, site.GetPeakShavingEnergyEntity)},
		"peakshavingenergyentitydelete": {"DELETE", "/peakshavingenergyentity", stringHandler(site.SetPeakShavingEnergyEntity, site.GetPeakShavingEnergyEntity)},
		"peakshavingchargepower":        {"POST", "/peakshavingchargepower/{value:[0-9.]+}", floatHandler(site.SetPeakShavingChargePower, site.GetPeakShavingChargePower)},
		"peakshavingcircuit":            {"POST", "/peakshavingcircuit/{value:" + namePattern + "}", stringHandler(site.SetPeakShavingCircuit, site.GetPeakShavingCircuit)},
		"peakshavingcircuitdelete":      {"DELETE", "/peakshavingcircuit", stringHandler(site.SetPeakShavingCircuit, site.GetPeakShavingCircuit)},
	}
}

// feedInFinalizeHandler recalculates a past month at the given market price
func feedInFinalizeHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		market, err := strconv.ParseFloat(vars["value"], 64)
		if err == nil {
			err = site.FinalizeFeedIn(vars["month"], market)
		}

		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, market)
	}
}

// lmProfileSaveHandler creates or updates a battery profile sent as json
func lmProfileSaveHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p profile.Profile
		err := json.NewDecoder(r.Body).Decode(&p)
		if err == nil {
			p, err = site.SaveLmProfile(p)
		}

		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, p)
	}
}

// lmProfileHandler runs an action on a battery profile
func lmProfileHandler(action func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]

		if err := action(id); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, id)
	}
}

// lmAdvancedHandler sets one of the advanced load management settings
func lmAdvancedHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		value, err := strconv.ParseFloat(vars["value"], 64)
		if err == nil {
			err = site.SetLmAdvanced(vars["name"], value)
		}

		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, value)
	}
}

// lmShedProtectHandler adds a loadpoint to the shed guard or removes it
func lmShedProtectHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		protected, err := strconv.ParseBool(vars["value"])
		if err == nil {
			err = site.SetLmShedProtected(vars["name"], protected)
		}

		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, protected)
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
