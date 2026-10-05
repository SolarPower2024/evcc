package server

// Custom extension: the site routes added by this fork. They live here rather
// than in the route table in http.go, so that upstream changes to that table
// merge without conflicts.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/evcc-io/evcc/core/lm/profile"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/logstash"
	"github.com/gorilla/mux"
)

// namePattern is evcc's own name rule, see nameRE in cmd/setup.go. It includes
// the colon of device names like db:3.
const namePattern = "[a-zA-Z0-9_.:-]+"

// addCustomSiteRoutes adds the fork's site routes to upstream's route table. A
// route upstream already has, by name or by method and path, stays upstream's:
// after an evcc update that added the same route, ours must not replace it
// unnoticed.
func addCustomSiteRoutes(routes map[string]route, site site.API) {
	for _, name := range mergeRoutes(routes, customSiteRoutes(site)) {
		util.NewLogger("httpd").ERROR.Printf("custom route %s collides with an evcc route, left out", name)
	}
}

// mergeRoutes adds the routes of add to dst that collide with none of dst and
// returns the names of those left out
func mergeRoutes(dst, add map[string]route) []string {
	used := make(map[string]bool, len(dst))
	for _, r := range dst {
		used[r.Method+" "+r.Pattern] = true
	}

	var res []string
	for name, r := range add {
		if _, ok := dst[name]; ok || used[r.Method+" "+r.Pattern] {
			res = append(res, name)
			continue
		}
		dst[name] = r
	}

	slices.Sort(res)
	return res
}

func customSiteRoutes(site site.API) map[string]route {
	return map[string]route{
		// soc-based grid charging, see core/site_lm.go
		"batterysocgridcharge":      {"POST", "/batterysocgridcharge/{value:[01truefalse]+}", boolHandler(site.SetBatterySocGridCharge, site.GetBatterySocGridCharge)},
		"batterysocgridchargestart": {"POST", "/batterysocgridchargestart/{value:[0-9.]+}", floatHandler(site.SetBatterySocGridChargeStart, site.GetBatterySocGridChargeStart)},
		"batterysocgridchargestop":  {"POST", "/batterysocgridchargestop/{value:[0-9.]+}", floatHandler(site.SetBatterySocGridChargeStop, site.GetBatterySocGridChargeStop)},

		// one-time grid charging, see core/site_lm_once.go
		"batterygridchargeonce":       {"POST", "/batterygridchargeonce/{soc:[0-9]+}", gridChargeOnceHandler(site)},
		"batterygridchargeonceuntil":  {"POST", "/batterygridchargeonce/{soc:[0-9]+}/{until:[0-9]{2}:[0-9]{2}}", gridChargeOnceHandler(site)},
		"batterygridchargeoncecancel": {"DELETE", "/batterygridchargeonce", gridChargeOnceCancelHandler(site)},

		// load management shed priorities, see core/site_lm.go
		"lmpriority": {"POST", "/lmpriority/{name:" + namePattern + "}/{value:[0-9]+}", lmPriorityHandler(site)},

		// load management shed guard, see core/site_lm_guard.go
		"lmshedguard":   {"POST", "/lmshedguard/{value:[0-9]+}", intHandler(site.SetLmShedGuard, site.GetLmShedGuard)},
		"lmshedprotect": {"POST", "/lmshedprotect/{name:" + namePattern + "}/{value:[01truefalse]+}", lmShedProtectHandler(site)},

		// load management switch, see core/site_lm_switch.go
		"lmenabled":       {"POST", "/lmenabled/{value:[01truefalse]+}", boolHandler(site.SetLmEnabled, site.GetLmEnabled)},
		"lmcircuit":       {"POST", "/lmcircuit/{value:" + namePattern + "}", stringHandler(site.SetLmCircuit, site.GetLmCircuit)},
		"lmcircuitdelete": {"DELETE", "/lmcircuit", stringHandler(site.SetLmCircuit, site.GetLmCircuit)},

		// battery identification, see core/site_battery_ident.go
		"batteryidentuse": {"POST", "/batteryidentuse/{value:[01truefalse]+}", boolHandler(site.SetBatteryIdentUse, site.GetBatteryIdentUse)},

		// advanced load management settings, see core/site_lm_advanced.go
		"lmadvanced": {"POST", "/lmadvanced/{name:[a-zA-Z]+}/{value:[0-9.]+}", lmAdvancedHandler(site)},

		// uploaded load profile for the home forecast, see core/site_load_manual.go
		"lmhomeprofile":       {"POST", "/lmhomeprofile", lmHomeProfileUploadHandler(site)},
		"lmhomeprofiledelete": {"DELETE", "/lmhomeprofile", lmHomeProfileDeleteHandler(site)},
		"lmhomeprofilecsv":    {"GET", "/lmhomeprofile", lmHomeProfileCsvHandler(site)},

		// battery profiles, see core/site_lm_profiles.go
		"lmprofile":       {"POST", "/lmprofile", lmProfileSaveHandler(site)},
		"lmprofiledelete": {"DELETE", "/lmprofile/{id:[a-z0-9]+}", lmProfileHandler(site.DeleteLmProfile)},
		"lmprofileapply":  {"POST", "/lmprofile/{id:[a-z0-9]+}/apply", lmProfileHandler(site.ApplyLmProfile)},

		// export under a second feed-in tariff, see core/site_feedin_eeg.go
		"feedineegentity":       {"POST", "/feedineegentity/{value:[a-zA-Z0-9_.]+}", stringHandler(site.SetFeedInEegEntity, site.GetFeedInEegEntity)},
		"feedineegentitydelete": {"DELETE", "/feedineegentity", stringHandler(site.SetFeedInEegEntity, site.GetFeedInEegEntity)},
		"feedinsplit":           {"GET", "/feedinsplit", feedInSplitHandler},

		// log in daily files, see core/site_logfile.go
		"logfile":    {"GET", "/logfile", logFileHandler(site)},
		"logfileset": {"POST", "/logfile", logFileSetHandler(site)},

		// snow on pv, see core/site_snow.go and core/site_snow_auto.go
		"snowcover": {"POST", "/snowcover/{value:[01truefalse]+}", boolHandler(site.SetSnowCover, site.GetSnowCover)},
		"snowauto":  {"POST", "/snowauto/{value:[01truefalse]+}", boolHandler(site.SetSnowAuto, site.GetSnowAuto)},

		// peak shaving, see core/site_peakshaving.go
		"peakshaving":                   {"POST", "/peakshaving/{value:[01truefalse]+}", boolHandler(site.SetPeakShaving, site.GetPeakShaving)},
		"peakshavinglimit":              {"POST", "/peakshavinglimit/{value:[0-9.]+}", floatHandler(site.SetPeakShavingLimit, site.GetPeakShavingLimit)},
		"peakfollow":                    {"POST", "/peakfollow/{value:[01truefalse]+}", boolHandler(site.SetPeakFollow, site.GetPeakFollow)},
		"peakfollowbuffer":              {"POST", "/peakfollowbuffer/{value:[0-9.]+}", floatHandler(site.SetPeakFollowBuffer, site.GetPeakFollowBuffer)},
		"peaktariff":                    {"POST", "/peaktariff/{name:[a-zA-Z]+}/{value:[0-9.]+}", peakTariffHandler(site)},
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

// logFileHandler returns the log file setting and the state of the files
func logFileHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jsonWrite(w, site.LogFile())
	}
}

// logFileSetHandler saves the log file setting sent as json and answers like logFileHandler
func logFileSetHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var cfg logstash.FileConfig
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&cfg)
		if err == nil {
			err = site.SetLogFile(cfg)
		}

		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, site.LogFile())
	}
}

// lmProfileSaveHandler creates or updates a battery profile sent as json
func lmProfileSaveHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p profile.Profile
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&p)
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

// peakTariffHandler sets one value of the capacity tariff
func peakTariffHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		value, err := strconv.ParseFloat(vars["value"], 64)
		if err == nil {
			err = site.SetPeakTariff(vars["name"], value)
		}

		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, value)
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

// lmHomeProfileUploadHandler stores the load profile sent as csv in the body
func lmHomeProfileUploadHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			name = "lastprofil.csv"
		}
		if r := []rune(name); len(r) > 100 {
			name = string(r[:100])
		}

		if err := site.SetLmHomeProfile(name, data); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, name)
	}
}

// lmHomeProfileDeleteHandler removes the load profile
func lmHomeProfileDeleteHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := site.DeleteLmHomeProfile(); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, true)
	}
}

// lmHomeProfileCsvHandler returns the load profile as csv
func lmHomeProfileCsvHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := site.LmHomeProfileCsv()
		if data == nil {
			jsonError(w, http.StatusNotFound, errors.New("no load profile"))
			return
		}

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="lastprofil.csv"`)
		_, _ = w.Write(data)
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

// gridChargeOnceHandler starts one-time grid charging up to a soc, right away
// or at the cheapest slots before the next occurrence of a time of day (HH:MM)
func gridChargeOnceHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		soc, err := strconv.ParseFloat(vars["soc"], 64)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		var until time.Time
		if s := vars["until"]; s != "" {
			t, err := time.ParseInLocation("15:04", s, time.Local)
			if err != nil {
				jsonError(w, http.StatusBadRequest, err)
				return
			}
			now := time.Now()
			until = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
			if !until.After(now) {
				until = until.AddDate(0, 0, 1)
			}
		}

		if err := site.SetBatteryGridChargeOnce(soc, until); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, soc)
	}
}

// gridChargeOnceCancelHandler stops one-time grid charging
func gridChargeOnceCancelHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := site.CancelBatteryGridChargeOnce(); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		jsonWrite(w, true)
	}
}

// feedInSplitHandler returns the export split by feed-in tariff in the
// buckets of the energy history
func feedInSplitHandler(w http.ResponseWriter, r *http.Request) {
	from, to, ok := historyRange(w, r)
	if !ok {
		return
	}

	if to.IsZero() {
		to = time.Now().AddDate(0, 0, 1)
	}

	aggregate := r.URL.Query().Get("aggregate")
	if aggregate == "" {
		aggregate = "15m"
	}

	res, err := metrics.QueryFeedInSplit(from, to, aggregate)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	jsonWrite(w, res)
}
