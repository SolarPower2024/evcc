package tariff

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/evcc-io/evcc/util/templates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

// haStates serves the state of one entity as Home Assistant does
type haStates struct {
	mu   sync.Mutex
	body string
	code int
	path string
}

func (h *haStates) set(body string, code int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.body, h.code = body, code
}

// haForecastServer makes the tariff read from a test server instead of Home
// Assistant, without the token the connection would ask for
func haForecastServer(t *testing.T, body string) *haStates {
	t.Helper()

	h := &haStates{body: body, code: http.StatusOK}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()

		h.path = r.URL.Path
		w.WriteHeader(h.code)
		fmt.Fprint(w, h.body)
	}))
	t.Cleanup(srv.Close)

	prev := HAForecastConnection
	HAForecastConnection = func(log *util.Logger, c homeassistant.Config) (*homeassistant.Connection, error) {
		c.URI = srv.URL
		conn, err := c.NewConnection(log)
		if err != nil {
			return nil, err
		}
		conn.Client.Transport = http.DefaultTransport
		return conn, nil
	}
	t.Cleanup(func() { HAForecastConnection = prev })

	return h
}

func haConfig(extra map[string]any) map[string]any {
	res := map[string]any{
		"uri":      "http://unused",
		"entity":   "sensor.evcc_einspeiseprognose",
		"interval": "1h",
	}
	for k, v := range extra {
		res[k] = v
	}
	return res
}

// at is a time tomorrow, within the window the forecast takes entries from
func at(h, m int) time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day()+1, h, m, 0, 0, time.Local)
}

func quarters(t *testing.T, rates api.Rates) []float64 {
	t.Helper()

	res := make([]float64, 0, len(rates))
	for i, r := range rates {
		assert.Equal(t, SlotDuration, r.End.Sub(r.Start), "slot %d", i)
		if i > 0 {
			assert.True(t, r.Start.Equal(rates[i-1].End), "slot %d follows slot %d", i, i-1)
		}
		res = append(res, r.Value)
	}
	return res
}

func TestHAForecast(t *testing.T) {
	h := haForecastServer(t, fmt.Sprintf(`{"entity_id":"sensor.evcc_einspeiseprognose","state":"1000","attributes":{
		"forecast":[
			{"start":%q,"end":%q,"value":1000},
			{"start":%q,"end":%q,"value":600}
		]}}`,
		at(10, 0).Format(time.RFC3339), at(10, 15).Format(time.RFC3339),
		at(10, 15).Format(time.RFC3339), at(10, 30).Format(time.RFC3339)))

	tr, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(nil))
	require.NoError(t, err)
	assert.Equal(t, api.TariffTypeSolar, tr.Type())
	assert.Equal(t, "/api/states/sensor.evcc_einspeiseprognose", h.path)

	rates, err := tr.Rates()
	require.NoError(t, err)
	assert.Equal(t, []float64{1000, 600}, quarters(t, rates))
	assert.True(t, rates[0].Start.Equal(at(10, 0)))
}

// An entry over several slots is divided into slots of its value, the first slot
// starts at the quarter hour the entry begins in.
func TestHAForecastSlots(t *testing.T) {
	rates, err := haForecastSlots(api.Rates{
		{Start: at(10, 5), End: at(10, 15), Value: 400}, // shortened first step of a run
		{Start: at(10, 15), End: at(10, 30), Value: 300},
		{Start: at(10, 30), End: at(11, 30), Value: 0}, // an hour
		{Start: at(11, 30), End: at(11, 45), Value: 50},
	}, time.Now())
	require.NoError(t, err)

	assert.Equal(t, []float64{400, 300, 0, 0, 0, 0, 50}, quarters(t, rates))
	assert.True(t, rates[0].Start.Equal(at(10, 0)))
	assert.True(t, rates[len(rates)-1].End.Equal(at(11, 45)))

	for _, bad := range []api.Rates{
		{{Start: at(10, 0), End: at(10, 0), Value: 1}},
		{{Start: at(10, 15), End: at(10, 0), Value: 1}},
		{{End: at(10, 0), Value: 1}},
	} {
		_, err := haForecastSlots(bad, time.Now())
		assert.Error(t, err)
	}

	// overlapping entries leave a slot once
	rates, err = haForecastSlots(api.Rates{
		{Start: at(10, 0), End: at(10, 30), Value: 1},
		{Start: at(10, 15), End: at(10, 45), Value: 2},
	}, time.Now())
	require.NoError(t, err)
	assert.Len(t, rates, 3)
}

// Every state of the entity that gives no forecast: the tariff is created and gives
// no rates and no error, so it does not break the sum of the solar tariffs.
func TestHAForecastNoForecast(t *testing.T) {
	list := func(updated string) string {
		return fmt.Sprintf(`{"state":"1","attributes":{"updated":%q,"forecast":[{"start":%q,"end":%q,"value":5}]}}`,
			updated, at(10, 0).Format(time.RFC3339), at(10, 15).Format(time.RFC3339))
	}

	for name, body := range map[string]string{
		"attribute missing": `{"state":"1000","attributes":{}}`,
		"unavailable":       `{"state":"unavailable","attributes":{"forecast":[]}}`,
		"unknown":           `{"state":"unknown","attributes":{"forecast":[]}}`,
		"not a list":        `{"state":"1","attributes":{"forecast":"abc"}}`,
		"list of numbers":   `{"state":"1","attributes":{"forecast":[1,2]}}`,
		"invalid entry":     `{"state":"1","attributes":{"forecast":[{"start":"2026-10-08T10:00:00Z","end":"2026-10-08T09:00:00Z","value":1}]}}`,
		"stale":             list(time.Now().Add(-90 * time.Minute).Format(time.RFC3339)),
	} {
		t.Run(name, func(t *testing.T) {
			haForecastServer(t, body)

			tr, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(nil))
			require.NoError(t, err)

			rates, err := tr.Rates()
			assert.NoError(t, err)
			assert.Empty(t, rates)
		})
	}

	t.Run("entity missing", func(t *testing.T) {
		h := haForecastServer(t, `{"message":"Entity not found."}`)
		h.set(`{"message":"Entity not found."}`, http.StatusNotFound)

		tr, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(nil))
		require.NoError(t, err)

		rates, err := tr.Rates()
		assert.NoError(t, err)
		assert.Empty(t, rates)
	})

	// a forecast updated within the hour, or without that attribute, is taken
	for name, body := range map[string]string{
		"fresh":      list(time.Now().Add(-50 * time.Minute).Format(time.RFC3339)),
		"no updated": fmt.Sprintf(`{"state":"1","attributes":{"forecast":[{"start":%q,"end":%q,"value":5}]}}`, at(10, 0).Format(time.RFC3339), at(10, 15).Format(time.RFC3339)),
	} {
		t.Run(name, func(t *testing.T) {
			haForecastServer(t, body)

			tr, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(nil))
			require.NoError(t, err)

			rates, err := tr.Rates()
			require.NoError(t, err)
			assert.Equal(t, []float64{5}, quarters(t, rates))
		})
	}

	t.Run("config", func(t *testing.T) {
		haForecastServer(t, `{}`)

		_, err := NewFromConfig(t.Context(), "homeassistant-forecast", map[string]any{"uri": "http://unused"})
		assert.ErrorContains(t, err, "missing entity")

		_, err = NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(map[string]any{"attribute": ""}))
		assert.ErrorContains(t, err, "missing attribute")
	})
}

// The entity is gone or the data outdated after it was read: no rates, no error.
func TestHAForecastGoesAway(t *testing.T) {
	body := fmt.Sprintf(`{"state":"1","attributes":{"forecast":[{"start":%q,"end":%q,"value":5}]}}`,
		at(10, 0).Format(time.RFC3339), at(10, 15).Format(time.RFC3339))

	t.Run("entity vanishes", func(t *testing.T) {
		h := haForecastServer(t, body)

		tr, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(map[string]any{"interval": "100ms"}))
		require.NoError(t, err)

		rates, err := tr.Rates()
		require.NoError(t, err)
		assert.Len(t, rates, 1)

		h.set(`{"message":"Entity not found."}`, http.StatusNotFound)

		assert.Eventually(t, func() bool {
			rates, err := tr.Rates()
			return err == nil && len(rates) == 0
		}, 5*time.Second, 20*time.Millisecond)
	})

	t.Run("home assistant not answering", func(t *testing.T) {
		h := haForecastServer(t, body)

		tr, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(map[string]any{"interval": "100ms"}))
		require.NoError(t, err)

		h.set(``, http.StatusInternalServerError)

		// the data is kept until it is outdated after twice the interval
		assert.Eventually(t, func() bool {
			rates, err := tr.Rates()
			return err == nil && len(rates) == 0
		}, 5*time.Second, 20*time.Millisecond)
	})
}

// The forecast is one of several solar tariffs: while the entity is missing the
// sum is the other tariff's forecast.
func TestHAForecastCombined(t *testing.T) {
	h := haForecastServer(t, `{"message":"Entity not found."}`)
	h.set(`{"message":"Entity not found."}`, http.StatusNotFound)

	ha, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(nil))
	require.NoError(t, err)

	own := &tariff{rates: api.Rates{
		{Start: at(10, 0), End: at(10, 15), Value: 700},
		{Start: at(10, 15), End: at(10, 30), Value: 900},
	}}

	got, err := NewCombined([]api.Tariff{ha, own}).Rates()
	require.NoError(t, err)
	assert.Equal(t, own.rates, got, "the own forecast goes on alone")

	got, err = NewCombined([]api.Tariff{own, ha}).Rates()
	require.NoError(t, err)
	assert.Equal(t, own.rates, got)

	// with the entity there it is added
	h.set(fmt.Sprintf(`{"state":"1","attributes":{"forecast":[{"start":%q,"end":%q,"value":100}]}}`,
		at(10, 0).Format(time.RFC3339), at(10, 30).Format(time.RFC3339)), http.StatusOK)

	ha2, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(nil))
	require.NoError(t, err)

	got, err = NewCombined([]api.Tariff{ha2, own}).Rates()
	require.NoError(t, err)
	assert.Equal(t, []float64{800, 1000}, quarters(t, got))
}

// Entries are taken from a day ago to a week ahead, and an entry reaching out of
// it only as far as that.
func TestHAForecastWindow(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour

	rates, err := haForecastSlots(api.Rates{
		{Start: now.Add(-3 * day), End: now.Add(-2 * day), Value: 1},                 // before the window
		{Start: now.Add(-day - time.Hour), End: now.Add(-day + time.Hour), Value: 2}, // reaches in
		{Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Value: 3},
		{Start: now.Add(7*day - time.Hour), End: now.Add(7*day + time.Hour), Value: 4}, // reaches out
		{Start: now.Add(8 * day), End: now.Add(9 * day), Value: 5},                     // after the window
		{Start: now.Add(-day), End: now.Add(400 * day), Value: 6},                      // a year: only the window
	}, now)
	require.NoError(t, err)

	assert.Less(t, len(rates), 8*96+10, "no more slots than the window holds")
	for _, r := range rates {
		assert.False(t, r.Start.Before(now.Add(-day-SlotDuration)), "%v", r.Start)
		assert.False(t, r.End.After(now.Add(7*day+SlotDuration)), "%v", r.End)
		assert.NotEqual(t, 1.0, r.Value)
		assert.NotEqual(t, 5.0, r.Value)
	}
	assert.Equal(t, 2.0, rates[0].Value)
}

func TestHAForecastAttribute(t *testing.T) {
	haForecastServer(t, fmt.Sprintf(`{"state":"1","attributes":{
		"forecast":[{"start":%q,"end":%q,"value":1}],
		"export":[{"start":%q,"end":%q,"value":7}]}}`,
		at(10, 0).Format(time.RFC3339), at(10, 15).Format(time.RFC3339),
		at(10, 0).Format(time.RFC3339), at(10, 15).Format(time.RFC3339)))

	tr, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(map[string]any{"attribute": "export"}))
	require.NoError(t, err)

	rates, err := tr.Rates()
	require.NoError(t, err)
	assert.Equal(t, []float64{7}, quarters(t, rates))
}

func TestHAForecastTemplate(t *testing.T) {
	tmpl, err := templates.ByName(templates.Tariff, "homeassistant-forecast")
	require.NoError(t, err)

	b, _, err := tmpl.RenderResult(templates.Tariff, templates.RenderModeInstance, map[string]any{
		"uri":    "http://homeassistant.local:8123",
		"entity": "sensor.evcc_einspeiseprognose",
	})
	require.NoError(t, err)

	var res map[string]any
	require.NoError(t, yaml.Unmarshal(b, &res), string(b))

	assert.Equal(t, "homeassistant-forecast", res["type"])
	assert.Equal(t, "http://homeassistant.local:8123", res["uri"])
	assert.Equal(t, "sensor.evcc_einspeiseprognose", res["entity"])
	assert.Equal(t, "forecast", res["attribute"])
	assert.Equal(t, "15m", res["interval"])

	b, _, err = tmpl.RenderResult(templates.Tariff, templates.RenderModeInstance, map[string]any{
		"uri":       "http://homeassistant.local:8123",
		"entity":    "sensor.x",
		"attribute": "export",
		"interval":  "5m",
	})
	require.NoError(t, err)
	res = nil
	require.NoError(t, yaml.Unmarshal(b, &res), string(b))
	assert.Equal(t, "export", res["attribute"])
	assert.Equal(t, "5m", res["interval"])
}

// combined adds rates of equal start as map keys, which also compare the zone. The
// entity holds the times in the zone of the instance that wrote it (Z, +02:00 and
// +01:00 around the end of summer time in Vienna), the slots must be in the local
// zone as those of every other tariff. None of these zones is Local, whatever the
// zone of this process is, so this holds for every zone; time.Local is not changed,
// as goroutines of other tests read it.
func TestHAForecastZones(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

	// 34 hours from 20:00 UTC over the end of summer time in Vienna (25.10. 01:00 UTC)
	start := time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)
	zones := []*time.Location{time.UTC, time.FixedZone("", 2*3600), time.FixedZone("", 3600)}

	var list []string
	for h := range 34 {
		s := start.Add(time.Duration(h) * time.Hour)
		list = append(list, fmt.Sprintf(`{"start":%q,"end":%q,"value":%d}`,
			s.In(zones[h%3]).Format(time.RFC3339), s.Add(time.Hour).In(zones[(h+1)%3]).Format(time.RFC3339), 100+h))
	}

	var rates api.Rates
	require.NoError(t, json.Unmarshal([]byte("["+strings.Join(list, ",")+"]"), &rates))

	slots, err := haForecastSlots(rates, now)
	require.NoError(t, err)
	require.Len(t, slots, 34*4)

	// an other tariff: its slots from time.Now-like Local times
	var other api.Rates
	for i := range 34 * 4 {
		s := start.Local().Add(time.Duration(i) * SlotDuration)
		other = append(other, api.Rate{Start: s, End: s.Add(SlotDuration), Value: 1000})
	}

	got, err := NewCombined([]api.Tariff{&tariff{rates: slots}, &tariff{rates: other}}).Rates()
	require.NoError(t, err)
	require.Len(t, got, 34*4, "one rate per slot")

	for i, r := range got {
		assert.Equal(t, float64(1000+100+i/4), r.Value, "slot %d at %v", i, r.Start)
	}
}
