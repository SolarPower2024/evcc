package tariff

// Custom extension: a solar forecast read from the attribute of a Home Assistant
// entity, a list of {start, end, value} with value in W. It reads what another
// evcc instance of this fork writes as export forecast, see
// core/site_lm_export_forecast.go, and any other entity with a list in this form.
//
// An entry longer than a slot is divided into slots of equal value. evcc takes a
// solar value as the power at its start and shapes a longer slot as a ramp towards
// the next one (see shapeSolar); a night written as one entry would rise from its
// first slot on instead of staying at 0. Several solar tariffs are added per
// slot by evcc (see combined), where one ends the others go on alone.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
)

// HAForecast is a solar forecast from the attribute of a Home Assistant entity
type HAForecast struct {
	*homeassistant.Connection
	log       *util.Logger
	entity    string
	attribute string
	data      *util.Monitor[api.Rates]
}

var _ api.Tariff = (*HAForecast)(nil)

// HAForecastConnection creates the connection; tests replace it to reach a local server instead of Home Assistant
var HAForecastConnection = func(log *util.Logger, c homeassistant.Config) (*homeassistant.Connection, error) {
	return c.NewConnection(log)
}

func init() {
	registry.Add("homeassistant-forecast", NewHAForecastFromConfig)
}

func NewHAForecastFromConfig(other map[string]any) (api.Tariff, error) {
	cc := struct {
		homeassistant.Config `mapstructure:",squash"`
		Entity               string
		Attribute            string
		Interval             time.Duration
	}{
		Attribute: "forecast",
		Interval:  15 * time.Minute,
	}

	if err := util.DecodeOther(other, &cc); err != nil {
		return nil, err
	}

	if cc.Entity == "" {
		return nil, errors.New("missing entity")
	}
	if cc.Attribute == "" {
		return nil, errors.New("missing attribute")
	}

	log := util.NewLogger("ha-forecast")

	conn, err := HAForecastConnection(log, cc.Config)
	if err != nil {
		return nil, err
	}

	t := &HAForecast{
		Connection: conn,
		log:        log,
		entity:     cc.Entity,
		attribute:  cc.Attribute,
		data:       util.NewMonitor[api.Rates](2 * cc.Interval),
	}

	done := make(chan error)
	go t.run(cc.Interval, done)

	if err := <-done; err != nil {
		return nil, err
	}

	return t, nil
}

func (t *HAForecast) run(interval time.Duration, done chan error) {
	var once sync.Once

	for ; true; <-time.Tick(interval) {
		var data api.Rates

		if err := backoff.Retry(func() error {
			var err error
			data, err = t.forecast()
			return backoffPermanentError(err)
		}, bo()); err != nil {
			if reportError(&once, done, err) {
				return
			}
			t.log.ERROR.Println(err)
			continue
		}

		t.data.Set(data)
		once.Do(func() { close(done) })
	}
}

// forecast reads the list from the entity and divides it into slots
func (t *HAForecast) forecast() (api.Rates, error) {
	var res struct {
		State      string                     `json:"state"`
		Attributes map[string]json.RawMessage `json:"attributes"`
	}

	uri := fmt.Sprintf("%s/api/states/%s", t.URI(), url.PathEscape(t.entity))
	if err := t.GetJSON(uri, &res); err != nil {
		return nil, err
	}

	if res.State == "unknown" || res.State == "unavailable" {
		return nil, backoff.Permanent(fmt.Errorf("%s: %w", t.entity, api.ErrNotAvailable))
	}

	raw, ok := res.Attributes[t.attribute]
	if !ok {
		return nil, backoff.Permanent(fmt.Errorf("%s: missing attribute %s", t.entity, t.attribute))
	}

	var rates api.Rates
	if err := json.Unmarshal(raw, &rates); err != nil {
		return nil, backoff.Permanent(fmt.Errorf("%s: attribute %s is not a list of start, end and value: %w", t.entity, t.attribute, err))
	}

	slots, err := haForecastSlots(rates)
	if err != nil {
		return nil, backoff.Permanent(fmt.Errorf("%s: %w", t.entity, err))
	}

	return slots, nil
}

// haForecastSlots divides each entry into slots of SlotDuration with the value of
// the entry. An entry starts at the slot it begins in.
func haForecastSlots(rates api.Rates) (api.Rates, error) {
	res := make(api.Rates, 0, len(rates))

	for _, r := range rates {
		if r.Start.IsZero() || !r.End.After(r.Start) {
			return nil, fmt.Errorf("invalid entry from %v to %v", r.Start, r.End)
		}

		for start := r.Start.Truncate(SlotDuration); start.Before(r.End); start = start.Add(SlotDuration) {
			res = append(res, api.Rate{
				Start: start,
				End:   start.Add(SlotDuration),
				Value: r.Value,
			})
		}
	}

	// entries that overlap leave a slot once, the first one stays
	slices.SortStableFunc(res, func(a, b api.Rate) int { return cmp.Compare(a.Start.UnixNano(), b.Start.UnixNano()) })
	res = slices.CompactFunc(res, func(a, b api.Rate) bool { return a.Start.Equal(b.Start) })

	return res, nil
}

// Rates implements the api.Tariff interface
func (t *HAForecast) Rates() (api.Rates, error) {
	var res api.Rates
	err := t.data.GetFunc(func(val api.Rates) {
		res = slices.Clone(val)
	})
	return res, err
}

// Type implements the api.Tariff interface
func (t *HAForecast) Type() api.TariffType {
	return api.TariffTypeSolar
}
