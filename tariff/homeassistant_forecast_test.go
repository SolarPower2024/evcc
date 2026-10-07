package tariff

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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

func at(h, m int) time.Time {
	return time.Date(2026, 10, 8, h, m, 0, 0, time.Local)
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
	})
	require.NoError(t, err)

	assert.Equal(t, []float64{400, 300, 0, 0, 0, 0, 50}, quarters(t, rates))
	assert.True(t, rates[0].Start.Equal(at(10, 0)))
	assert.True(t, rates[len(rates)-1].End.Equal(at(11, 45)))

	for _, bad := range []api.Rates{
		{{Start: at(10, 0), End: at(10, 0), Value: 1}},
		{{Start: at(10, 15), End: at(10, 0), Value: 1}},
		{{End: at(10, 0), Value: 1}},
	} {
		_, err := haForecastSlots(bad)
		assert.Error(t, err)
	}

	// overlapping entries leave a slot once
	rates, err = haForecastSlots(api.Rates{
		{Start: at(10, 0), End: at(10, 30), Value: 1},
		{Start: at(10, 15), End: at(10, 45), Value: 2},
	})
	require.NoError(t, err)
	assert.Len(t, rates, 3)
}

func TestHAForecastErrors(t *testing.T) {
	for name, body := range map[string]string{
		"attribute missing": `{"state":"1000","attributes":{}}`,
		"unavailable":       `{"state":"unavailable","attributes":{"forecast":[]}}`,
		"unknown":           `{"state":"unknown","attributes":{"forecast":[]}}`,
		"not a list":        `{"state":"1","attributes":{"forecast":"abc"}}`,
		"list of numbers":   `{"state":"1","attributes":{"forecast":[1,2]}}`,
		"invalid entry":     `{"state":"1","attributes":{"forecast":[{"start":"2026-10-08T10:00:00Z","end":"2026-10-08T09:00:00Z","value":1}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			haForecastServer(t, body)

			_, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(nil))
			assert.Error(t, err)
		})
	}

	t.Run("entity missing", func(t *testing.T) {
		h := haForecastServer(t, `{"message":"Entity not found."}`)
		h.set(`{"message":"Entity not found."}`, http.StatusNotFound)

		_, err := NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(nil))
		assert.Error(t, err)
	})

	t.Run("config", func(t *testing.T) {
		haForecastServer(t, `{}`)

		_, err := NewFromConfig(t.Context(), "homeassistant-forecast", map[string]any{"uri": "http://unused"})
		assert.ErrorContains(t, err, "missing entity")

		_, err = NewFromConfig(t.Context(), "homeassistant-forecast", haConfig(map[string]any{"attribute": ""}))
		assert.ErrorContains(t, err, "missing attribute")
	})
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
