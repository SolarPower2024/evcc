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
//
// This forecast never breaks the sum of the solar tariffs: a missing entity,
// `unavailable`, a wrong attribute, a forecast not updated for an hour and data
// older than twice the interval give no rates and no error, so the instance's own
// forecast goes on alone. A missing entity (after a restart of Home Assistant)
// keeps the data until they are older than twice the interval. Each cause is logged once as a warning, the return of
// the data once as info, see util.CauseLog; only the loop logs, not Rates.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/evcc-io/evcc/util/request"
)

const (
	// the forecast counts as stale when its `updated` attribute is older, a
	// forecast without that attribute (another source) is taken as current
	haForecastStale = time.Hour

	// entries are taken from this window around now
	haForecastPast   = 24 * time.Hour
	haForecastFuture = 7 * 24 * time.Hour
)

// how long the constructor waits for the first read, a variable for the tests
var haForecastWait = 10 * time.Second

// errNoForecast marks a state of the entity that gives no forecast, as opposed to
// a failed request
var errNoForecast = errors.New("no forecast")

// errEntityMissing marks a missing entity (404). After a restart of Home Assistant
// the entity is gone until its writer creates it again, so the data are kept, as
// when Home Assistant does not answer.
var errEntityMissing = errors.New("entity not found")

// HAForecast is a solar forecast from the attribute of a Home Assistant entity
type HAForecast struct {
	*homeassistant.Connection
	log       *util.Logger
	entity    string
	attribute string
	data      *util.Monitor[api.Rates]
	cause     util.CauseLog // each cause is logged once
}

var _ api.Tariff = (*HAForecast)(nil)

// HAForecastConnection creates the connection; tests replace it to reach a local server instead of Home Assistant
// HAForecastWriterID returns the id this instance writes into the export forecast
// entity, empty if it has none. The entity of that id is not read: it is this
// instance's own list. nil = no check, tests set it like HAForecastConnection.
var HAForecastWriterID func() string

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
	if cc.Interval <= 0 {
		return nil, errors.New("interval: must be positive")
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

	// the first read may stay empty: the entity may not exist yet, the instance
	// that writes it may start later. Wait for it haForecastWait at most, then
	// start empty (Rates would block until the first Set otherwise); the loop sets
	// the real data when it has them.
	go t.run(cc.Interval)

	select {
	case <-t.data.Done():
	case <-time.After(haForecastWait):
		t.data.Set(nil)
	}

	return t, nil
}

func (t *HAForecast) run(interval time.Duration) {
	for ; true; <-time.Tick(interval) {
		t.update()
	}
}

// update reads once and reports the result
func (t *HAForecast) update() {
	var data api.Rates

	err := backoff.Retry(func() error {
		var err error
		data, err = t.forecast(time.Now())

		// a state of the entity does not change by asking again
		if errors.Is(err, errNoForecast) || errors.Is(err, errEntityMissing) {
			return backoff.Permanent(err)
		}
		return backoffPermanentError(err)
	}, bo())

	switch {
	case err == nil:
		t.data.Set(data)
	case errors.Is(err, errNoForecast):
		t.data.Set(nil)
	default:
		// Home Assistant not reached or the entity missing: keep the data until
		// they are outdated, empty only while there were none
		select {
		case <-t.data.Done():
		default:
			t.data.Set(nil)
		}
	}

	t.cause.Report(t.log, t.entity, err)
}

// forecast reads the list from the entity and divides it into slots. A state of
// the entity that gives no forecast is an errNoForecast.
func (t *HAForecast) forecast(now time.Time) (api.Rates, error) {
	var res struct {
		State      string                     `json:"state"`
		Attributes map[string]json.RawMessage `json:"attributes"`
	}

	uri := fmt.Sprintf("%s/api/states/%s", t.URI(), url.PathEscape(t.entity))
	if err := t.GetJSON(uri, &res); err != nil {
		if se, ok := errors.AsType[*request.StatusError](err); ok && se.StatusCode() == http.StatusNotFound {
			return nil, errEntityMissing
		}
		return nil, err
	}

	if res.State == "unknown" || res.State == "unavailable" {
		return nil, fmt.Errorf("entity %s: %w", res.State, errNoForecast)
	}

	if own := HAForecastWriterID; own != nil {
		if id := own(); id != "" {
			// a missing attribute (another source, an old list) is a foreign entity
			var writer string
			if raw, ok := res.Attributes["writer"]; ok && json.Unmarshal(raw, &writer) == nil && writer == id {
				return nil, fmt.Errorf("written by this instance: %w", errNoForecast)
			}
		}
	}

	raw, ok := res.Attributes[t.attribute]
	if !ok {
		return nil, fmt.Errorf("missing attribute %s: %w", t.attribute, errNoForecast)
	}

	if raw, ok := res.Attributes["updated"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if updated, err := time.Parse(time.RFC3339, s); err == nil && now.Sub(updated) > haForecastStale {
				return nil, fmt.Errorf("not updated since %s: %w", updated.Local().Format("02.01. 15:04"), errNoForecast)
			}
		}
	}

	var rates api.Rates
	if err := json.Unmarshal(raw, &rates); err != nil {
		return nil, fmt.Errorf("attribute %s is not a list of start, end and value: %w", t.attribute, errors.Join(err, errNoForecast))
	}

	slots, err := haForecastSlots(rates, now)
	if err != nil {
		return nil, fmt.Errorf("attribute %s: %w", t.attribute, errors.Join(err, errNoForecast))
	}

	return slots, nil
}

// haForecastSlots divides each entry into slots of SlotDuration with the value of
// the entry. An entry starts at the slot it begins in. Only the time from a day
// ago to a week ahead is taken, before dividing.
func haForecastSlots(rates api.Rates, now time.Time) (api.Rates, error) {
	from, to := now.Add(-haForecastPast), now.Add(haForecastFuture)

	res := make(api.Rates, 0, len(rates))

	for _, r := range rates {
		if r.Start.IsZero() || !r.End.After(r.Start) {
			return nil, fmt.Errorf("invalid entry from %v to %v", r.Start, r.End)
		}

		start, end := r.Start, r.End
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if !end.After(start) {
			continue
		}

		// in the local zone like the slots of every other tariff: combined adds the
		// rates of equal start as map keys, which also compare the zone, and the entity
		// holds the times in the zone of the instance that wrote it
		for s := start.Local().Truncate(SlotDuration); s.Before(end); s = s.Add(SlotDuration) {
			res = append(res, api.Rate{
				Start: s,
				End:   s.Add(SlotDuration),
				Value: r.Value,
			})
		}
	}

	// entries that overlap leave a slot once, the first one stays
	slices.SortStableFunc(res, func(a, b api.Rate) int { return cmp.Compare(a.Start.UnixNano(), b.Start.UnixNano()) })
	res = slices.CompactFunc(res, func(a, b api.Rate) bool { return a.Start.Equal(b.Start) })

	return res, nil
}

// Rates implements the api.Tariff interface. Without a current forecast there
// are no rates and no error, see above.
func (t *HAForecast) Rates() (api.Rates, error) {
	var res api.Rates
	if err := t.data.GetFunc(func(val api.Rates) {
		res = slices.Clone(val)
	}); err != nil {
		return nil, nil
	}

	return res, nil
}

// Type implements the api.Tariff interface
func (t *HAForecast) Type() api.TariffType {
	return api.TariffTypeSolar
}
