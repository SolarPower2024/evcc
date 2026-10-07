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
	"bytes"
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
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/evcc-io/evcc/util/request"
	optimizer "github.com/evcc-io/optimizer/client"
)

const (
	exportForecastTimeout   = 10 * time.Second
	exportForecastRefresh   = 10 * time.Minute // an unchanged list is written again after this time
	exportForecastWarnEvery = 15 * time.Minute // at most one warning in this time
)

var exportForecastEntityRe = regexp.MustCompile(`^sensor\.[a-z0-9_]+$`)

// exportRate is one entry of the exported forecast, value is the mean power in W
type exportRate struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Value float64   `json:"value"`
}

// exportWrite is a list for an entity
type exportWrite struct {
	entity string
	rates  []exportRate
}

// same reports whether both are the same list for the same entity
func (w *exportWrite) same(o *exportWrite) bool {
	return w != nil && o != nil && w.entity == o.entity && slices.EqualFunc(w.rates, o.rates, func(a, b exportRate) bool {
		return a.Start.Equal(b.Start) && a.End.Equal(b.End) && a.Value == b.Value
	})
}

// exportForecastState is the runtime state of the export forecast
type exportForecastState struct {
	mu        sync.Mutex
	last      *exportWrite // last list written, with its entity
	lastWrite time.Time
	inflight  *exportWrite // list being written
	pending   *exportWrite // newest list that came in while writing
	writing   bool
	lastWarn  time.Time
	conn      *homeassistant.Connection // kept until a write fails or the entity changes
	wg        sync.WaitGroup            // writer running, for tests

	// connect returns the Home Assistant connection, nil = the supervisor
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

	if usableResult(res) {
		site.publishExportForecast(details, *req, *res)
	}
}

// publishExportForecast writes the forecast to the entity set in the ui, in the
// background so the optimizer does not wait for Home Assistant. A list that did
// not change is written again after exportForecastRefresh, so `updated` stays
// current for the reader. While a write runs, the newest list waits for its end.
func (site *Site) publishExportForecast(details requestDetails, req optimizer.OptimizationInput, res optimizer.OptimizationResult) {
	entity := site.exportForecastEntity()
	if entity == "" {
		return
	}

	rates := exportForecast(details, req, res)
	if rates == nil {
		return
	}

	w := &exportWrite{entity, rates}
	s := site.exportFc()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.inflight.same(w) || s.pending == nil && s.last.same(w) && time.Since(s.lastWrite) < exportForecastRefresh {
		return
	}

	s.pending = w

	if s.writing {
		return
	}

	s.writing = true
	s.wg.Add(1)
	go site.exportForecastWriter()
}

// exportForecastWriter writes the pending list, then the one that arrived
// meanwhile, until none is left
func (site *Site) exportForecastWriter() {
	s := site.exportFc()
	defer s.wg.Done()

	for {
		s.mu.Lock()
		w := s.pending
		s.pending = nil
		s.inflight = w
		if w == nil {
			s.writing = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		// the entity was changed or turned off meanwhile
		var err error
		if w.entity == site.exportForecastEntity() {
			err = site.writeExportForecast(w)
		}

		s.mu.Lock()
		s.inflight = nil
		if err == nil {
			s.last, s.lastWrite = w, time.Now()
		}
		s.mu.Unlock()

		if err != nil {
			site.exportForecastWarn(err)
		}
	}
}

// writeExportForecast writes one list. The connection is kept, a failed write
// builds a new one the next time.
func (site *Site) writeExportForecast(w *exportWrite) error {
	s := site.exportFc()

	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()

	if conn == nil {
		var err error
		if conn, err = site.exportForecastConnection(); err != nil {
			return err
		}

		s.mu.Lock()
		s.conn = conn
		s.mu.Unlock()
	}

	now := time.Now()
	attrs := map[string]any{
		"unit_of_measurement": "W",
		"device_class":        "power",
		"friendly_name":       "evcc Einspeiseprognose",
		"forecast":            w.rates,
		"updated":             now.Format(time.RFC3339),
	}

	size, err := writeHAState(conn, w.entity, exportCurrent(w.rates, now), attrs)
	if err != nil {
		s.mu.Lock()
		s.conn = nil
		s.mu.Unlock()
		return err
	}

	// Home Assistant's recorder keeps the attributes of an entity only up to
	// 16 KB, the entity is to be excluded from it, so the size is only logged
	site.log.DEBUG.Printf("export forecast: %s, %d entries, %d bytes", w.entity, len(w.rates), size)

	return nil
}

// exportForecastConnection builds the Home Assistant connection
func (site *Site) exportForecastConnection() (*homeassistant.Connection, error) {
	if s := site.exportFc(); s.connect != nil {
		return s.connect()
	}

	uri, err := site.peakURI()
	if err != nil {
		return nil, err
	}

	return homeassistant.NewConnection(util.NewLogger("exportforecast"), uri, "", false)
}

// exportForecastWarn logs a failed write, at most once in exportForecastWarnEvery
func (site *Site) exportForecastWarn(err error) {
	s := site.exportFc()

	s.mu.Lock()
	warn := time.Since(s.lastWarn) >= exportForecastWarnEvery
	if warn {
		s.lastWarn = time.Now()
	}
	s.mu.Unlock()

	if warn {
		site.log.WARN.Printf("export forecast: %v", err)
	}
}

// writeHAState sets the state of an entity, creating it if needed. It returns
// the size of the request body.
func writeHAState(conn *homeassistant.Connection, entity string, state float64, attrs map[string]any) (int, error) {
	uri := fmt.Sprintf("%s/api/states/%s", conn.URI(), url.PathEscape(entity))

	body, err := json.Marshal(map[string]any{
		"state":      strconv.FormatFloat(state, 'f', -1, 64),
		"attributes": attrs,
	})
	if err != nil {
		return 0, err
	}

	req, err := request.New(http.MethodPost, uri, bytes.NewReader(body), request.JSONEncoding)
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), exportForecastTimeout)
	defer cancel()

	_, err = conn.DoBody(req.WithContext(ctx))
	return len(body), err
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
			return errors.New("entity: sensor. followed by a–z, 0–9 or _")
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

	// a changed entity gets the forecast on the next run, with a new connection
	f := site.exportFc()
	f.mu.Lock()
	f.last, f.lastWrite, f.conn = nil, time.Time{}, nil
	f.mu.Unlock()

	if err := settings.SetJson(keys.LmAdvanced, adv); err != nil {
		return err
	}

	site.publishLmAdvanced()

	return nil
}
