package core

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The export forecast with its hours of zeros as entries over a full hour goes
// through evcc's way for solar forecasts: read by the tariff homeassistant-forecast,
// added to a second solar tariff as configureSolarTariff does, then turned into the
// energy per slot of the optimizer request and into the series of the forecast page.
// Every slot has the value of its step, 0 in the hours of zeros, and the result is
// the same as for a list of individual steps.

// exportDay is a run of the optimizer from now on: export in the middle of the
// day, zeros before and after, first step shortened as a run in the middle of a
// quarter hour has it
func exportDay(t *testing.T) ([]exportRate, []exportRate, []float64) {
	t.Helper()

	// the run is a little into the current quarter hour, its first step ends on the
	// boundary; the steps after it are quarter hours
	now := time.Now().Truncate(15 * time.Minute)
	first := now.Add(20 * time.Second)

	const n = 120 // 30 h
	wh := make([]float32, n)
	for i := range wh {
		// export in two periods of 4 h, 20 and 26 steps from now
		if (i >= 20 && i < 36) || (i >= 70 && i < 86) {
			wh[i] = float32(50 + 15*(i%7))
		}
	}

	details, req, res := exportCase(now, 900, wh...)
	details.Timestamps[0] = first
	req.TimeSeries.Dt[0] = 880

	merged := exportForecast(details, req, res)

	// the same plan as individual steps
	single := make([]exportRate, n)
	want := make([]float64, n)
	for i := range single {
		dt := float64(req.TimeSeries.Dt[i])
		want[i] = math.Round(float64(wh[i]) * 3600 / dt)
		start := now.Add(time.Duration(i) * 15 * time.Minute)
		single[i] = exportRate{start, start.Add(15 * time.Minute), want[i]}
	}

	require.Less(t, len(merged), len(single), "hours of zeros are merged")
	return merged, single, want
}

// haServing makes a tariff of this type read the list from a test server, its
// times written in the given zone
func haServing(t *testing.T, list []exportRate, zone *time.Location) api.Tariff {
	t.Helper()

	inZone := make([]exportRate, len(list))
	for i, r := range list {
		inZone[i] = exportRate{r.Start.In(zone), r.End.In(zone), r.Value}
	}

	b, err := json.Marshal(inZone)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"entity_id":"sensor.evcc_einspeiseprognose","state":"0","attributes":{"forecast":%s}}`, b)
	}))
	t.Cleanup(srv.Close)

	prev := tariff.HAForecastConnection
	tariff.HAForecastConnection = func(log *util.Logger, c homeassistant.Config) (*homeassistant.Connection, error) {
		c.URI = srv.URL
		conn, err := c.NewConnection(log)
		if err != nil {
			return nil, err
		}
		conn.Client.Transport = http.DefaultTransport
		return conn, nil
	}
	t.Cleanup(func() { tariff.HAForecastConnection = prev })

	tr, err := tariff.NewFromConfig(t.Context(), "homeassistant-forecast", map[string]any{
		"uri": "http://unused", "entity": "sensor.evcc_einspeiseprognose", "interval": "1h",
	})
	require.NoError(t, err)
	return tr
}

// hourlySolar is a solar tariff of hourly values as most forecast services give them,
// divided into slots as evcc does for every tariff not delivering quarter hours
type hourlySolar struct{ rates api.Rates }

func (t hourlySolar) Rates() (api.Rates, error) { return t.rates, nil }
func (t hourlySolar) Type() api.TariffType      { return api.TariffTypeSolar }

func secondSolar(hours int) api.Tariff {
	start := time.Now().Truncate(time.Hour).Add(-time.Hour)

	var rates api.Rates
	for i := range hours {
		rates = append(rates, api.Rate{
			Start: start.Add(time.Duration(i) * time.Hour),
			End:   start.Add(time.Duration(i+1) * time.Hour),
			Value: 300 + float64(100*(i%5)),
		})
	}

	return &tariff.SlotWrapper{Tariff: hourlySolar{rates}}
}

// The entity holds the times as the instance that wrote them formats them: with
// the offset of its zone, or Z. Read back, none of them is in the Local zone the
// other tariffs use, and combined adds rates by their start as map key, which also
// compares the zone: the tariff has to put its slots into the Local zone, or the
// second tariff drops out of the sum. Without changing time.Local, which goroutines
// of other tests read.
func TestExportForecastThroughSolarPath(t *testing.T) {
	for name, zone := range map[string]*time.Location{
		"local":  time.Local,
		"utc":    time.UTC,
		"offset": time.FixedZone("", 330*60), // not on a full hour
	} {
		t.Run(name, func(t *testing.T) { exportThroughSolarPath(t, zone) })
	}
}

func exportThroughSolarPath(t *testing.T, zone *time.Location) {
	merged, single, want := exportDay(t)

	mixed := haServing(t, merged, zone)
	ref := haServing(t, single, time.Local)

	second := secondSolar(60) // longer than the export forecast

	// the slots of the tariff: the value of each step, 0 in the hours of zeros
	rates, err := mixed.Rates()
	require.NoError(t, err)
	refRates, err := ref.Rates()
	require.NoError(t, err)

	require.Len(t, rates, len(want))
	assert.Equal(t, refRates, rates, "an entry over an hour is the same as its four steps")

	for i, r := range rates {
		assert.Equal(t, want[i], r.Value, "slot %d at %v", i, r.Start.Format("15:04"))
		assert.Equal(t, 15*time.Minute, r.End.Sub(r.Start))
	}

	// alone and added to the second tariff
	for name, tariffs := range map[string][]api.Tariff{
		"alone":    {mixed},
		"combined": {mixed, second},
	} {
		refTariffs := append([]api.Tariff{ref}, tariffs[1:]...)

		t.Run(name, func(t *testing.T) {
			got, err := tariff.NewCombined(tariffs).Rates()
			require.NoError(t, err)
			exp, err := tariff.NewCombined(refTariffs).Rates()
			require.NoError(t, err)
			assert.Equal(t, exp, got, "the sum")

			if name == "combined" {
				sec, err := second.Rates()
				require.NoError(t, err)

				for i, r := range rates {
					var other float64
					for _, s := range sec {
						if s.Start.Equal(r.Start) {
							other = s.Value
						}
					}
					g := got[slotAt(got, r.Start)]
					assert.Equal(t, want[i]+other, g.Value, "slot %d at %v: export + second tariff", i, r.Start.Format("15:04"))
				}

				// where the export forecast ends the second tariff goes on alone
				last := rates[len(rates)-1]
				after := got[slotAt(got, last.End)]
				assert.True(t, after.Start.Equal(last.End))
				sec2 := sec[slotAt(sec, last.End)]
				assert.Equal(t, sec2.Value, after.Value, "after the end of the export forecast")
				assert.Greater(t, len(got), len(rates))
			}

			// the optimizer request: energy per slot, then one value per slot
			solar := currentRates(tariff.NewCombined(tariffs))
			refSolar := currentRates(tariff.NewCombined(refTariffs))
			require.NotEmpty(t, solar)

			energy, err := solarRatesToEnergy(solar)
			require.NoError(t, err)
			refEnergy, err := solarRatesToEnergy(refSolar)
			require.NoError(t, err)

			assert.Equal(t, scaleAndPrune(refEnergy, 1, 192), scaleAndPrune(energy, 1, 192), "optimizer input per slot")

			// the series of the forecast page
			assert.Equal(t, solarTimeseries(refSolar), solarTimeseries(solar), "forecast page")
		})
	}
}

// slotAt is the index of the rate starting at ts
func slotAt(rr api.Rates, ts time.Time) int {
	for i, r := range rr {
		if r.Start.Equal(ts) {
			return i
		}
	}
	return -1
}

// evcc's optimizer input takes one value per slot by position, and a solar value
// is the power at the start of its slot. An entry over an hour that reached it
// undivided would be one slot instead of four and shift everything after it; a
// divided slot with a ramp towards the next one (see shapeSolar) would not stay 0
// in the zero hours. That is why the tariff divides the entries itself.
func TestExportForecastMixedLengthNeedsSplit(t *testing.T) {
	merged, single, want := exportDay(t)

	// the entries as the entity holds them, read without dividing them
	raw := make(api.Rates, len(merged))
	for i, r := range merged {
		raw[i] = api.Rate{Start: r.Start.Truncate(tariff.SlotDuration), End: r.End, Value: r.Value}
	}

	// a quarter hour as first entry: evcc takes the tariff as it is
	energy, err := solarRatesToEnergy(raw)
	require.NoError(t, err)

	refRates := make(api.Rates, len(single))
	for i, r := range single {
		refRates[i] = api.Rate{Start: r.Start, End: r.End, Value: r.Value}
	}
	refEnergy, err := solarRatesToEnergy(refRates)
	require.NoError(t, err)

	assert.NotEqual(t, scaleAndPrune(refEnergy, 1, 192), scaleAndPrune(energy, 1, len(raw)), "the entries must not reach evcc undivided")
	assert.Len(t, want, len(single))
}
