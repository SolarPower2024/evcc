package tariff

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func oemagServer(t *testing.T, body string) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv.URL
}

const oemagSample = `{
    "timestamp": "2026-09-23T11:01:07.140613",
    "oemag_marktpreis": 0.08997,
    "unit": "EUR/kWh",
    "raw_scraped_value": "8,997 ct/kWh"
}`

func TestOemag(t *testing.T) {
	tt, err := NewOemagFromConfig(map[string]any{"uri": oemagServer(t, oemagSample)})
	require.NoError(t, err)

	o := tt.(*Oemag)
	assert.Equal(t, api.TariffTypePriceStatic, o.Type())
	assert.Equal(t, 15, o.FinalizeDay(), "default finalize day")

	rates, err := o.Rates()
	require.NoError(t, err)
	require.NotEmpty(t, rates)

	r, err := rates.At(time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0.08997, r.Value)

	p, err := o.FinalPrice(time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0.08997, p)
}

func TestOemagChargesAndTax(t *testing.T) {
	tt, err := NewOemagFromConfig(map[string]any{
		"uri":         oemagServer(t, oemagSample),
		"charges":     -0.01,
		"finalizeday": 20,
	})
	require.NoError(t, err)

	o := tt.(*Oemag)
	assert.Equal(t, 20, o.FinalizeDay())

	p, err := o.FinalPrice(time.Now())
	require.NoError(t, err)
	assert.InDelta(t, 0.07997, p, 1e-9, "final price with the same charges as the running rate")
}

func TestOemagRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing price", `{"unit": "EUR/kWh"}`},
		{"zero", `{"oemag_marktpreis": 0, "unit": "EUR/kWh"}`},
		{"cent instead of euro", `{"oemag_marktpreis": 8.997, "unit": "EUR/kWh"}`},
		{"wrong unit", `{"oemag_marktpreis": 0.08997, "unit": "ct/kWh"}`},
		{"not json", `<html>rate limited</html>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewOemagFromConfig(map[string]any{"uri": oemagServer(t, tc.body)})
			assert.Error(t, err)
		})
	}

	for _, day := range []int{0, 29, 31} {
		_, err := NewOemagFromConfig(map[string]any{"uri": oemagServer(t, oemagSample), "finalizeday": day})
		assert.Error(t, err, "finalize day %d", day)
	}
}
