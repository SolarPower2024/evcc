package core

// Custom extension: detect snow from the weather. With the setting on, the
// weather forecast of Open-Meteo is fetched every 30 minutes for the location
// of the Open-Meteo solar forecast. Enough new snow in the last 24 hours or
// before the next sunrise turns the switch of site_snow.go on, so the plan for
// the first snow day already runs without solar yield. Turning off stays with
// the measurement there. Without the setting, or without an Open-Meteo solar
// forecast, nothing happens. See the README for the rules.

import (
	"fmt"
	"sort"
	"time"

	"github.com/evcc-io/evcc/api/globalconfig"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
	"github.com/evcc-io/evcc/util/request"
	"github.com/evcc-io/evcc/util/templates"
	"github.com/spf13/cast"
)

const (
	// the weather is fetched this often, a failed attempt is repeated after it as well
	snowAutoInterval = 30 * time.Minute
	// snow in cm within snowAutoWindow turns the switch on
	snowAutoMinCm = 1.0
	// only snow at this air temperature in °C or below lies on the modules
	snowAutoMaxTemp = 1.0
	// window of the snow amount, looking back as far as it is long
	snowAutoWindow = 24 * time.Hour
	// the template of the solar forecast the location is taken from
	snowAutoTemplate = "open-meteo"
)

// snowAutoURI is the Open-Meteo forecast, a variable for the tests
var snowAutoURI = "https://api.open-meteo.com/v1/forecast"

// snowSlot is a quarter hour of the weather forecast
type snowSlot struct {
	End  time.Time // the amounts are those of the quarter hour before
	Cm   float64   // snowfall in cm
	Temp float64   // air temperature in °C
}

// snowWeather is the weather data of Open-Meteo
type snowWeather struct {
	Slots    []snowSlot
	Sunrises []time.Time
}

// snowResponse is the part of Open-Meteo's answer that is used
type snowResponse struct {
	Minutely15 struct {
		Time     []int64    `json:"time"`
		Snowfall []*float64 `json:"snowfall"`
		Temp     []*float64 `json:"temperature_2m"`
	} `json:"minutely_15"`
	Daily struct {
		Sunrise []int64 `json:"sunrise"`
	} `json:"daily"`
}

// weather converts the answer, quarter hours without a value are left out
func (r snowResponse) weather() snowWeather {
	var res snowWeather

	m := r.Minutely15
	for i, t := range m.Time {
		if i >= len(m.Snowfall) || i >= len(m.Temp) || m.Snowfall[i] == nil || m.Temp[i] == nil {
			continue
		}
		res.Slots = append(res.Slots, snowSlot{time.Unix(t, 0).UTC(), *m.Snowfall[i], *m.Temp[i]})
	}

	for _, t := range r.Daily.Sunrise {
		res.Sunrises = append(res.Sunrises, time.Unix(t, 0).UTC())
	}

	return res
}

// snowUntil returns the end of the forecast that counts: the next sunrise,
// the snow of the day after is looked at again then. Without a sunrise in the
// data it is the length of the window.
func snowUntil(sunrises []time.Time, now time.Time) time.Time {
	for _, t := range sunrises {
		if t.After(now) {
			return t
		}
	}

	return now.Add(snowAutoWindow)
}

// snowFall returns the snow in cm the slots hold in the most snowy 24 hours,
// the end of the last snowy slot and whether the amount turns the switch on.
// Counted are the slots at or below the temperature limit from the last 24 hours
// until the end, that end after seen: snow counted before does not count again.
func snowFall(slots []snowSlot, now, until, seen time.Time) (cm float64, last time.Time, ok bool) {
	var snow []snowSlot
	for _, s := range slots {
		if s.Cm > 0 && s.Temp <= snowAutoMaxTemp && s.End.After(now.Add(-snowAutoWindow)) && !s.End.After(until) && s.End.After(seen) {
			snow = append(snow, s)
		}
	}

	sort.Slice(snow, func(i, j int) bool { return snow[i].End.Before(snow[j].End) })

	for i, from := range snow {
		var sum float64
		for _, s := range snow[i:] {
			if !s.End.Before(from.End.Add(snowAutoWindow)) {
				break
			}
			sum += s.Cm
		}
		cm = max(cm, sum)
	}

	if len(snow) > 0 {
		last = snow[len(snow)-1].End
	}

	// the sum of the quarter hours is not exact
	return cm, last, cm+1e-9 >= snowAutoMinCm
}

// snowCoordinates returns the location of the first Open-Meteo solar forecast
// of the tariffs set up in the ui
func snowCoordinates() (lat, lon float64, ok bool) {
	if db.Instance == nil {
		return 0, 0, false
	}

	var refs globalconfig.TariffRefs
	if err := settings.Json(keys.TariffRefs, &refs); err != nil {
		return 0, 0, false
	}

	for _, ref := range refs.Solar {
		id, err := config.IDForName(ref)
		if err != nil {
			continue
		}

		conf, err := config.ConfigByID(id)
		if err != nil || conf.Class != templates.Tariff || conf.Disable || conf.Data["template"] != snowAutoTemplate {
			continue
		}

		lat, errLat := cast.ToFloat64E(conf.Data["lat"])
		lon, errLon := cast.ToFloat64E(conf.Data["lon"])
		if errLat != nil || errLon != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			continue
		}

		return lat, lon, true
	}

	return 0, 0, false
}

// snowFetch gets the weather of the location
func (site *Site) snowFetch(lat, lon float64) (snowWeather, error) {
	uri := fmt.Sprintf("%s?latitude=%v&longitude=%v&minutely_15=snowfall,temperature_2m&daily=sunrise&past_days=1&forecast_days=2&timezone=GMT&timeformat=unixtime", snowAutoURI, lat, lon)

	var res snowResponse
	if err := request.NewHelper(site.log).GetJSON(uri, &res); err != nil {
		return snowWeather{}, err
	}

	w := res.weather()
	if len(w.Slots) == 0 {
		return snowWeather{}, fmt.Errorf("no weather data for %v, %v", lat, lon)
	}

	return w, nil
}

// updateSnowAvailable looks up the location and publishes whether an
// Open-Meteo solar forecast gives one
func (site *Site) updateSnowAvailable() (lat, lon float64, ok bool) {
	lat, lon, ok = snowCoordinates()
	site.publish(keys.SnowAutoAvailable, ok)

	return lat, lon, ok
}

// GetSnowAuto returns whether snow is detected from the weather
func (site *Site) GetSnowAuto() bool {
	s := site.snow()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.auto
}

// SetSnowAuto sets whether snow is detected from the weather
func (site *Site) SetSnowAuto(val bool) error {
	s := site.snow()

	s.mu.Lock()
	changed := s.auto != val
	s.auto = val
	// the weather is fetched at the next cycle
	s.fetchAt = time.Time{}
	s.mu.Unlock()

	if !changed {
		return nil
	}

	site.log.DEBUG.Println("set snow detection:", val)
	settings.SetBool(keys.SnowAuto, val)
	site.publish(keys.SnowAuto, val)

	return nil
}

// updateSnowAuto fetches the weather every 30 minutes while the detection is
// on, in the background as the request can take its time
func (site *Site) updateSnowAuto(now time.Time) {
	s := site.snow()

	s.mu.Lock()
	due := s.auto && !s.fetching && (s.fetchAt.IsZero() || now.Sub(s.fetchAt) >= snowAutoInterval)
	if due {
		s.fetching = true
		s.fetchAt = now
	}
	s.mu.Unlock()

	if !due {
		return
	}

	go func() {
		defer func() {
			s.mu.Lock()
			s.fetching = false
			s.mu.Unlock()
		}()

		site.snowAutoRun(now)
	}()
}

// snowAutoRun fetches the weather and turns the switch on for enough new snow
func (site *Site) snowAutoRun(now time.Time) {
	if !site.GetSnowAuto() {
		return
	}

	s := site.snow()

	lat, lon, ok := site.updateSnowAvailable()
	if !ok {
		return
	}

	w, err := site.snowFetch(lat, lon)

	s.mu.Lock()
	failed := s.failed
	s.failed = err != nil
	auto, seen := s.auto, s.seen
	s.mu.Unlock()

	if err != nil {
		// once per series of failures, the next attempts come every 30 minutes
		if !failed {
			site.log.WARN.Printf("snow detection: weather data not available: %v", err)
		} else {
			site.log.DEBUG.Printf("snow detection: weather data not available: %v", err)
		}
		return
	}
	if failed {
		site.log.INFO.Println("snow detection: weather data available again")
	}

	if !auto {
		// switched off meanwhile
		return
	}

	cm, last, ok := snowFall(w.Slots, now, snowUntil(w.Sunrises, now), seen)
	if !ok {
		site.log.TRACE.Printf("snow detection: %.2f cm of snow, below %.0f cm", cm, snowAutoMinCm)
		return
	}

	// the snow is counted: it does not turn the switch on again, also not after
	// the switch went off by the measurement or by hand
	s.mu.Lock()
	s.seen = last
	on := s.cover
	s.mu.Unlock()
	settings.SetTime(keys.SnowSeen, last)

	if on {
		return
	}

	site.log.INFO.Printf("pv snow cover on: %.1f cm of snow at up to %.0f °C until %s", cm, snowAutoMaxTemp, last.Local().Format("2006-01-02 15:04"))
	site.setSnowCover(true, true)
}
