package core

// Custom extension: the optimizer's planned grid export as a forecast in a Home
// Assistant entity. After each valid optimizer run the export per step is written
// as a list of {start, end, value} in the entity's `forecast` attribute, value
// the mean power in W. A second evcc instance reads it as solar forecast with the
// tariff type homeassistant-forecast (tariff/homeassistant_forecast.go).
//
// The entity is created by the write itself, no helper is needed. It does not
// survive a restart of Home Assistant, the next optimizer run writes it again.
// Only available in the Home Assistant add-on, like the other entities, see
// peakURI. The entity is set in the ui under Lastmanagement-Details → Erweitert.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/evcc-io/evcc/util/request"
	optimizer "github.com/evcc-io/optimizer/client"
)

const (
	exportForecastTimeout = 10 * time.Second
	exportForecastWarn    = 15 * time.Minute // at most one warning in this time
)

var exportForecastEntityRe = regexp.MustCompile(`^sensor\.[a-z0-9_]+$`)

// exportRate is one entry of the exported forecast, value is the mean power in W
type exportRate struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Value float64   `json:"value"`
}

// exportForecastState is the runtime state of the export forecast
type exportForecastState struct {
	mu       sync.Mutex
	last     []exportRate // last list written
	lastWarn time.Time
	writing  atomic.Bool
	wg       sync.WaitGroup // writes in flight, for tests

	// connect returns the Home Assistant connection, nil = site.haConnection
	connect func() (*homeassistant.Connection, error)
}

// exportFc returns the export forecast state
func (site *Site) exportFc() *exportForecastState {
	return &site.custom.export
}

// exportForecast returns the planned grid export as a forecast. Wh per step
// become the mean power in W. Steps of a full hour at 0 W are one entry over that
// hour, so a forecast of nights stays short; no entry spans more than a full hour,
// the reader can then divide it onto its own slots. Steps with export, and
// partial hours at either end of the horizon, stay as they are. A result that
// does not fit the request gives nil.
func exportForecast(details requestDetails, req optimizer.OptimizationInput, res optimizer.OptimizationResult) []exportRate {
	n := len(res.GridExport)
	if n == 0 || len(details.Timestamps) != n || len(req.TimeSeries.Dt) != n {
		return nil
	}

	steps := make([]exportRate, 0, n)
	for i, wh := range res.GridExport {
		dt := time.Duration(req.TimeSeries.Dt[i]) * time.Second
		if dt <= 0 {
			return nil
		}

		// solver noise below zero is no export
		w := math.Round(math.Max(float64(wh), 0) * 3600 / dt.Seconds())

		start := details.Timestamps[i]
		end := start.Add(dt)

		// the first step starts at the time of the run, the quarter hour it is in
		// is meant: a reader takes the value for that slot
		if q := start.Truncate(tariff.SlotDuration); end.Sub(q) <= tariff.SlotDuration {
			start = q
		}

		steps = append(steps, exportRate{Start: start, End: end, Value: w})
	}

	res2 := make([]exportRate, 0, n)
	for i := 0; i < len(steps); {
		if j := zeroHourEnd(steps, i); j > i+1 {
			res2 = append(res2, exportRate{Start: steps[i].Start, End: steps[j-1].End})
			i = j
			continue
		}

		res2 = append(res2, steps[i])
		i++
	}

	return res2
}

// zeroHourEnd returns the index after the steps from i that make up a full hour
// at 0 W, i if they are not
func zeroHourEnd(steps []exportRate, i int) int {
	start := steps[i].Start
	if start.Minute() != 0 || start.Second() != 0 || start.Nanosecond() != 0 {
		return i
	}

	end := start.Add(time.Hour)

	for j := i; j < len(steps); j++ {
		s := steps[j]
		if s.Value != 0 || j > i && !s.Start.Equal(steps[j-1].End) {
			return i
		}
		if s.End.Equal(end) {
			return j + 1
		}
		if s.End.After(end) {
			return i
		}
	}

	return i
}

// exportCurrent is the power of the step covering now, 0 if none does
func exportCurrent(rates []exportRate, now time.Time) float64 {
	for _, r := range rates {
		if !now.Before(r.Start) && now.Before(r.End) {
			return r.Value
		}
	}
	return 0
}

// lmOptimizerResult runs the passes after the solve and then hands a usable
// result on to the export forecast
func (site *Site) lmOptimizerResult(client *optimizer.ClientWithResponses, req *optimizer.OptimizationInput, details requestDetails, res *optimizer.OptimizationResult) {
	site.lmOptimizerPasses(client, req, details, res)

	if res != nil && (res.Status == optimizer.Optimal || res.Status == optimizer.Feasible) {
		site.publishExportForecast(details, *req, *res)
	}
}

// publishExportForecast writes the forecast to the entity set in the ui, in the
// background so the optimizer does not wait for Home Assistant. A list that did
// not change is not written again; a write still running skips this one.
func (site *Site) publishExportForecast(details requestDetails, req optimizer.OptimizationInput, res optimizer.OptimizationResult) {
	entity := site.exportForecastEntity()
	if entity == "" {
		return
	}

	rates := exportForecast(details, req, res)
	if rates == nil {
		return
	}

	s := site.exportFc()

	s.mu.Lock()
	unchanged := slices.EqualFunc(rates, s.last, func(a, b exportRate) bool {
		return a.Start.Equal(b.Start) && a.End.Equal(b.End) && a.Value == b.Value
	})
	s.mu.Unlock()

	if unchanged || !s.writing.CompareAndSwap(false, true) {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.writing.Store(false)

		now := time.Now()
		attrs := map[string]any{
			"unit_of_measurement": "W",
			"device_class":        "power",
			"friendly_name":       "evcc Einspeiseprognose",
			"forecast":            rates,
			"updated":             now.Format(time.RFC3339),
		}

		connect := site.haConnection
		if s.connect != nil {
			connect = s.connect
		}

		// Home Assistant's recorder keeps the attributes of an entity only up to
		// 16 KB, the entity is to be excluded from it, so the size is only logged
		if b, err := json.Marshal(attrs); err == nil {
			site.log.DEBUG.Printf("export forecast: %d entries, %d bytes", len(rates), len(b))
		}

		conn, err := connect()
		if err == nil {
			err = writeHAState(conn, entity, exportCurrent(rates, now), attrs)
		}

		if err != nil {
			site.exportForecastWarn(err)
			return
		}

		s.mu.Lock()
		s.last = rates
		s.mu.Unlock()
	}()
}

// exportForecastWarn logs a failed write, at most once in exportForecastWarn
func (site *Site) exportForecastWarn(err error) {
	s := site.exportFc()

	s.mu.Lock()
	warn := time.Since(s.lastWarn) >= exportForecastWarn
	if warn {
		s.lastWarn = time.Now()
	}
	s.mu.Unlock()

	if warn {
		site.log.WARN.Printf("export forecast: %v", err)
	}
}

// writeHAState sets the state of an entity, creating it if needed
func writeHAState(conn *homeassistant.Connection, entity string, state float64, attrs map[string]any) error {
	uri := fmt.Sprintf("%s/api/states/%s", conn.URI(), url.PathEscape(entity))

	body := map[string]any{
		"state":      strconv.FormatFloat(state, 'f', -1, 64),
		"attributes": attrs,
	}

	req, err := request.New(http.MethodPost, uri, request.MarshalJSON(body), request.JSONEncoding)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), exportForecastTimeout)
	defer cancel()

	_, err = conn.DoBody(req.WithContext(ctx))
	return err
}

// exportForecastEntity is the entity set in the ui, empty = off
func (site *Site) exportForecastEntity() string {
	return site.advanced().ExportForecastEntity
}

// GetLmExportForecast returns the entity the export forecast is written to
func (site *Site) GetLmExportForecast() string {
	return site.exportForecastEntity()
}

// SetLmExportForecast sets the entity the export forecast is written to, empty
// turns it off
func (site *Site) SetLmExportForecast(entity string) error {
	if entity != "" {
		if !exportForecastEntityRe.MatchString(entity) {
			return errors.New("entity must start with sensor.")
		}
		if _, err := site.peakURI(); err != nil {
			return err
		}
	}

	s := site.lms()

	s.advMu.Lock()
	s.adv.ExportForecastEntity = entity
	adv := s.adv
	s.advMu.Unlock()

	site.log.DEBUG.Printf("set export forecast entity: %s", entity)

	// a changed entity gets the forecast on the next run
	f := site.exportFc()
	f.mu.Lock()
	f.last = nil
	f.mu.Unlock()

	if err := settings.SetJson(keys.LmAdvanced, adv); err != nil {
		return err
	}

	site.publishLmAdvanced()

	return nil
}
