package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api/globalconfig"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/evcc-io/evcc/util/templates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// the recorded forecast of Innsbruck for 24 to 26 January 2026: 4.4 cm of snow
// at -0.4 to +0.5 °C on the 25th ending 16:00 to 21:45 GMT, the next sunrise at 06:47 on the 26th
const snowTirolData = "testdata/open-meteo-snow-tirol.json"

var snowTirolSnowEnd = time.Date(2026, 1, 25, 21, 45, 0, 0, time.UTC)

func snowAt(day, hour, min int) time.Time {
	return time.Date(2026, 1, day, hour, min, 0, 0, time.UTC)
}

func TestSnowFall(t *testing.T) {
	now := snowAt(10, 12, 0)

	// n quarter hours of the amount per quarter hour at the temperature, the first ending at from
	run := func(from time.Time, n int, cm, temp float64) []snowSlot {
		var res []snowSlot
		for i := range n {
			res = append(res, snowSlot{from.Add(time.Duration(i) * 15 * time.Minute), cm, temp})
		}
		return res
	}

	tc := []struct {
		name  string
		slots []snowSlot
		until time.Time
		seen  time.Time
		cm    float64
		ok    bool
	}{
		{"1 cm at -2 °C", run(snowAt(10, 6, 0), 4, 0.25, -2), snowAt(11, 7, 0), time.Time{}, 1, true},
		{"1 cm at +3 °C", run(snowAt(10, 6, 0), 4, 0.25, 3), snowAt(11, 7, 0), time.Time{}, 0, false},
		{"1 cm at exactly +1 °C", run(snowAt(10, 6, 0), 4, 0.25, 1), snowAt(11, 7, 0), time.Time{}, 1, true},
		{"0.5 cm", run(snowAt(10, 6, 0), 4, 0.125, -2), snowAt(11, 7, 0), time.Time{}, 0.5, false},
		{"ten times 0.1 cm is not short by rounding", run(snowAt(10, 6, 0), 10, 0.1, -2), snowAt(11, 7, 0), time.Time{}, 1, true},
		{"rain does not count", run(snowAt(10, 6, 0), 8, 0, 5), snowAt(11, 7, 0), time.Time{}, 0, false},
		{"mixed: only the cold quarter hours add up", append(run(snowAt(10, 6, 0), 4, 0.25, 4), run(snowAt(10, 7, 0), 3, 0.25, -1)...), snowAt(11, 7, 0), time.Time{}, 0.75, false},
		{"forecast for tonight counts", run(snowAt(10, 22, 0), 4, 0.25, -2), snowAt(11, 7, 0), time.Time{}, 1, true},
		{"after the next sunrise does not count yet", run(snowAt(11, 8, 0), 4, 0.25, -2), snowAt(11, 7, 0), time.Time{}, 0, false},
		{"snow ending at the sunrise counts", run(snowAt(11, 6, 15), 4, 0.25, -2), snowAt(11, 7, 0), time.Time{}, 1, true},
		{"older than 24 h does not count", run(snowAt(9, 10, 0), 4, 0.25, -2), snowAt(11, 7, 0), time.Time{}, 0, false},
		{"just inside the 24 h counts", run(snowAt(9, 12, 15), 4, 0.25, -2), snowAt(11, 7, 0), time.Time{}, 1, true},
		{"0.6 cm now and 0.6 cm in 20 h are 1.2 cm in 24 h", append(run(snowAt(10, 8, 0), 4, 0.15, -2), run(snowAt(11, 4, 0), 4, 0.15, -2)...), snowAt(11, 7, 0), time.Time{}, 1.2, true},
		{"0.6 cm yesterday and 0.6 cm in 26 h are not 24 h", append(run(snowAt(9, 13, 0), 4, 0.15, -2), run(snowAt(10, 15, 0), 4, 0.15, -2)...), snowAt(11, 15, 0), time.Time{}, 0.6, false},
		{"already counted does not count again", run(snowAt(10, 6, 0), 4, 0.25, -2), snowAt(11, 7, 0), snowAt(10, 7, 0), 0, false},
		{"snow after the counted counts alone", append(run(snowAt(10, 6, 0), 4, 0.25, -2), run(snowAt(10, 20, 0), 3, 0.25, -2)...), snowAt(11, 7, 0), snowAt(10, 7, 0), 0.75, false},
		{"enough new snow after the counted", append(run(snowAt(10, 6, 0), 4, 0.25, -2), run(snowAt(10, 20, 0), 4, 0.25, -2)...), snowAt(11, 7, 0), snowAt(10, 7, 0), 1, true},
		{"nothing", nil, snowAt(11, 7, 0), time.Time{}, 0, false},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			cm, last, ok := snowFall(tc.slots, now, tc.until, tc.seen)
			assert.InDelta(t, tc.cm, cm, 1e-9)
			assert.Equal(t, tc.ok, ok)

			if cm > 0 {
				// the last counted quarter hour is the one to remember
				assert.True(t, last.After(tc.seen), "last %v", last)
				assert.False(t, last.After(tc.until), "last %v", last)
			} else {
				assert.True(t, last.IsZero())
			}
		})
	}

	// the last quarter hour counted, not the amount's window
	slots := append(run(snowAt(10, 6, 0), 4, 0.25, -2), run(snowAt(10, 22, 0), 2, 0.25, 5)...)
	_, last, _ := snowFall(slots, now, snowAt(11, 7, 0), time.Time{})
	assert.Equal(t, snowAt(10, 6, 45), last, "warm quarter hours are not counted")
}

func TestSnowUntil(t *testing.T) {
	sunrises := []time.Time{snowAt(9, 7, 0), snowAt(10, 7, 0), snowAt(11, 7, 0)}

	assert.Equal(t, snowAt(10, 7, 0), snowUntil(sunrises, snowAt(10, 5, 0)), "this morning's sunrise is next")
	assert.Equal(t, snowAt(11, 7, 0), snowUntil(sunrises, snowAt(10, 7, 0)), "at the sunrise the next one")
	assert.Equal(t, snowAt(11, 7, 0), snowUntil(sunrises, snowAt(10, 12, 0)))
	assert.Equal(t, snowAt(11, 12, 0), snowUntil(sunrises[:2], snowAt(10, 12, 0)), "without a next sunrise 24 h")
	assert.Equal(t, snowAt(11, 12, 0), snowUntil(nil, snowAt(10, 12, 0)))
}

func readSnowTirol(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(snowTirolData)
	require.NoError(t, err)
	return b
}

// TestSnowAutoRecordedForecast: the recorded answer is read as the quarter
// hours and sunrises it holds, and rated at several times of the snow day
func TestSnowAutoRecordedForecast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(readSnowTirol(t))
	}))
	defer srv.Close()
	setSnowAutoURI(t, srv.URL)

	site := &Site{log: util.NewLogger("test")}
	w, err := site.snowFetch(47.27, 11.39)
	require.NoError(t, err)

	assert.Len(t, w.Slots, 288, "three days of quarter hours")
	assert.Len(t, w.Sunrises, 3)
	assert.Equal(t, snowAt(24, 0, 0), w.Slots[0].End)
	assert.Equal(t, snowAt(26, 6, 47), w.Sunrises[2].UTC().Truncate(time.Minute))

	rate := func(now, seen time.Time) (float64, bool) {
		cm, _, ok := snowFall(w.Slots, now, snowUntil(w.Sunrises, now), seen)
		return cm, ok
	}

	// the day before: no snow yet, the next sunrise comes before it
	cm, ok := rate(snowAt(24, 20, 0), time.Time{})
	assert.Zero(t, cm)
	assert.False(t, ok)

	// the morning of the snow day: it comes in the evening, before the next
	// sunrise, so it counts already
	cm, ok = rate(snowAt(25, 8, 0), time.Time{})
	assert.InDelta(t, 4.3, cm, 0.05)
	assert.True(t, ok)

	// while it snows, and after it: still in the 24 h
	for _, now := range []time.Time{snowAt(25, 19, 0), snowAt(26, 6, 0), snowAt(26, 15, 0)} {
		_, ok = rate(now, time.Time{})
		assert.True(t, ok, now)
	}

	// counted once, it does not count again
	_, ok = rate(snowAt(25, 19, 0), snowTirolSnowEnd)
	assert.False(t, ok)
	_, ok = rate(snowAt(25, 8, 0), snowTirolSnowEnd)
	assert.False(t, ok)

	// two days later it has left the window
	cm, ok = rate(snowAt(26, 22, 30), time.Time{})
	assert.Zero(t, cm)
	assert.False(t, ok)
}

func setSnowAutoURI(t *testing.T, uri string) {
	t.Helper()
	prev := snowAutoURI
	snowAutoURI = uri
	t.Cleanup(func() { snowAutoURI = prev })
}

// snowAutoDB opens a database with the tariffs of the ui: the solar
// forecasts of the templates given, in this order, the first one as the first
// reference
func snowAutoDB(t *testing.T, tariffs ...config.Properties) {
	t.Helper()

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	var refs globalconfig.TariffRefs
	for _, p := range tariffs {
		data := map[string]any{"template": p.Product, "lat": 47.27, "lon": "11.39"}
		p.Product = ""
		conf, err := config.AddConfig(templates.Tariff, data, config.WithProperties(p))
		require.NoError(t, err)
		refs.Solar = append(refs.Solar, config.NameForID(conf.ID))
	}

	require.NoError(t, settings.SetJson(keys.TariffRefs, refs))
}

func snowTariff(template string) config.Properties {
	return config.Properties{Type: "template", Product: template}
}

func TestSnowCoordinates(t *testing.T) {
	keepSettings(t)

	noSettingsDB(t)
	_, _, ok := snowCoordinates()
	assert.False(t, ok, "no database")

	snowAutoDB(t)
	_, _, ok = snowCoordinates()
	assert.False(t, ok, "no solar forecast")

	disabled := snowTariff("open-meteo")
	disabled.Disable = true

	snowAutoDB(t, snowTariff("forecast-solar"), disabled)
	_, _, ok = snowCoordinates()
	assert.False(t, ok, "other templates and disabled ones do not give the location")

	snowAutoDB(t, snowTariff("forecast-solar"), snowTariff("open-meteo"))
	lat, lon, ok := snowCoordinates()
	assert.True(t, ok)
	assert.Equal(t, 47.27, lat)
	assert.Equal(t, 11.39, lon, "also stored as text")

	// the first one
	snowAutoDB(t, disabled, snowTariff("open-meteo"), snowTariff("open-meteo"))
	_, _, ok = snowCoordinates()
	assert.True(t, ok)

	// a location out of range is none
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	conf, err := config.AddConfig(templates.Tariff, map[string]any{"template": "open-meteo", "lat": 147.0, "lon": 11.0})
	require.NoError(t, err)
	require.NoError(t, settings.SetJson(keys.TariffRefs, globalconfig.TariffRefs{Solar: []string{config.NameForID(conf.ID)}}))
	_, _, ok = snowCoordinates()
	assert.False(t, ok)
	db.Instance = nil
}

// snowAutoSite is a site with the weather served as data, the requests counted
func snowAutoSite(t *testing.T, data func() []byte, status *atomic.Int32) (*Site, chan util.Param, *atomic.Int32) {
	t.Helper()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if code := int(status.Load()); code != 0 {
			w.WriteHeader(code)
			return
		}
		_, _ = w.Write(data())
	}))
	t.Cleanup(srv.Close)
	setSnowAutoURI(t, srv.URL)

	pub := make(chan util.Param, 64)
	return &Site{log: util.NewLogger("test"), valueChan: pub}, pub, &calls
}

// TestSnowAutoTurnsOn: the weather turns the switch on once for snow in the
// forecast, marked as automatic; it does not come back after the switch went
// off, by hand or by the measurement, until new snow falls
func TestSnowAutoTurnsOn(t *testing.T) {
	keepSettings(t)
	snowAutoDB(t, snowTariff("open-meteo"))

	var status atomic.Int32
	data := readSnowTirol(t)
	site, pub, calls := snowAutoSite(t, func() []byte { return data }, &status)

	// the setting is off: inert, no request
	site.snowAutoRun(snowAt(25, 8, 0))
	site.updateSnowAuto(snowAt(25, 8, 0))
	assert.False(t, site.GetSnowCover())
	assert.Zero(t, calls.Load())

	require.NoError(t, site.SetSnowAuto(true))
	assert.Equal(t, util.Param{Key: keys.SnowAuto, Val: true}, <-pub)

	// the morning before the snow: it is in the forecast for tonight
	site.snowAutoRun(snowAt(25, 8, 0))
	assert.Equal(t, int32(1), calls.Load())
	assert.True(t, site.GetSnowCover())
	assert.True(t, site.snow().byAuto)
	assert.Equal(t, snowTirolSnowEnd, site.snow().seen.UTC())

	// the state is published and saved
	got := map[string]any{}
	for len(pub) > 0 {
		p := <-pub
		got[p.Key] = p.Val
	}
	assert.Equal(t, true, got[keys.SnowCover])
	assert.Equal(t, true, got[keys.SnowCoverAuto])
	assert.Equal(t, true, got[keys.SnowAutoAvailable])
	v, err := settings.Bool(keys.SnowCoverAuto)
	require.NoError(t, err)
	assert.True(t, v)
	seen, err := settings.Time(keys.SnowSeen)
	require.NoError(t, err)
	assert.True(t, snowTirolSnowEnd.Equal(seen))

	// off by hand: the same snow does not turn it on again
	require.NoError(t, site.SetSnowCover(false))
	assert.False(t, site.snow().byAuto)
	site.snowAutoRun(snowAt(25, 12, 0))
	site.snowAutoRun(snowAt(26, 8, 0))
	assert.False(t, site.GetSnowCover(), "off by hand stays off")

	// off by the measurement, after the snow, still within its 24 h
	site.setSnowCover(true, true)
	assert.True(t, site.GetSnowCover())
	site.setSnowCover(false, false)
	site.snowAutoRun(snowAt(26, 15, 0))
	assert.False(t, site.GetSnowCover())

	// new snow on the next day turns it on again: 1 cm at -3 °C at 08:00 to 08:45 on the 27th
	slots := readSnowTirolResponse(t)
	slots.add(snowAt(27, 8, 0), 4, 0.25, -3)
	data = slots.json(t)
	site.snowAutoRun(snowAt(27, 9, 0))
	assert.True(t, site.GetSnowCover())
	assert.True(t, site.snow().byAuto)
	assert.Equal(t, snowAt(27, 8, 45), site.snow().seen.UTC())

	// switched on by hand while it is on, nothing changes: the mark stays
	require.NoError(t, site.SetSnowCover(true))
	assert.True(t, site.snow().byAuto)

	// the setting off in between: the answer is not used
	require.NoError(t, site.SetSnowCover(false))
	slots.add(snowAt(28, 8, 0), 4, 0.25, -3)
	data = slots.json(t)
	require.NoError(t, site.SetSnowAuto(false))
	site.snowAutoRun(snowAt(28, 9, 0))
	assert.False(t, site.GetSnowCover())
}

// TestSnowAutoWhileOn: snow counted while the switch is on is remembered, so it
// does not turn the switch on again once the measurement turned it off
func TestSnowAutoWhileOn(t *testing.T) {
	keepSettings(t)
	snowAutoDB(t, snowTariff("open-meteo"))

	var status atomic.Int32
	data := readSnowTirol(t)
	site, _, _ := snowAutoSite(t, func() []byte { return data }, &status)
	require.NoError(t, site.SetSnowAuto(true))

	// on by hand before the detection looked
	require.NoError(t, site.SetSnowCover(true))
	site.snowAutoRun(snowAt(25, 20, 0))
	assert.True(t, site.GetSnowCover())
	assert.False(t, site.snow().byAuto, "by hand stays by hand")
	assert.Equal(t, snowTirolSnowEnd, site.snow().seen.UTC())

	// the measurement turns it off: the snow of the last 24 h does not count again
	require.NoError(t, site.SetSnowCover(false))
	site.snowAutoRun(snowAt(26, 14, 0))
	assert.False(t, site.GetSnowCover())
}

// TestSnowAutoWithoutLocation: without an Open-Meteo solar forecast nothing is
// fetched and the detection is not offered
func TestSnowAutoWithoutLocation(t *testing.T) {
	keepSettings(t)
	snowAutoDB(t, snowTariff("forecast-solar"))

	var status atomic.Int32
	site, pub, calls := snowAutoSite(t, func() []byte { return nil }, &status)
	require.NoError(t, site.SetSnowAuto(true))
	<-pub

	site.snowAutoRun(snowAt(25, 8, 0))

	assert.Zero(t, calls.Load())
	assert.False(t, site.GetSnowCover())
	assert.Equal(t, util.Param{Key: keys.SnowAutoAvailable, Val: false}, <-pub)

	// restored with the setting: offered as soon as the forecast is there
	snowAutoDB(t, snowTariff("open-meteo"))
	site.restoreSnowCover()
	var last util.Param
	for range 4 { // switch, by detection, setting, available
		last = <-pub
	}
	assert.Equal(t, util.Param{Key: keys.SnowAutoAvailable, Val: true}, last)
}

// TestSnowAutoFailure: an unavailable weather service leaves the switch as it
// is, and is warned about once per series
func TestSnowAutoFailure(t *testing.T) {
	keepSettings(t)
	snowAutoDB(t, snowTariff("open-meteo"))

	var status atomic.Int32
	data := readSnowTirol(t)
	site, _, calls := snowAutoSite(t, func() []byte { return data }, &status)
	require.NoError(t, site.SetSnowAuto(true))

	for _, code := range []int32{500, 500} {
		status.Store(code)
		site.snowAutoRun(snowAt(25, 8, 0))
		assert.False(t, site.GetSnowCover())
	}
	assert.Equal(t, int32(2), calls.Load())
	assert.True(t, site.snow().failed)

	// an answer without data is a failure as well
	status.Store(0)
	data = []byte(`{"minutely_15":{"time":[],"snowfall":[],"temperature_2m":[]}}`)
	site.snowAutoRun(snowAt(25, 8, 0))
	assert.False(t, site.GetSnowCover())
	assert.True(t, site.snow().failed)

	data = []byte("not json")
	site.snowAutoRun(snowAt(25, 8, 0))
	assert.False(t, site.GetSnowCover())

	// and recovers
	data = readSnowTirol(t)
	site.snowAutoRun(snowAt(25, 8, 0))
	assert.False(t, site.snow().failed)
	assert.True(t, site.GetSnowCover())
}

// TestSnowAutoInterval: the weather is fetched at the first cycle with the
// setting on and then every 30 minutes, one request at a time
func TestSnowAutoInterval(t *testing.T) {
	keepSettings(t)
	snowAutoDB(t, snowTariff("open-meteo"))

	var status atomic.Int32
	data := readSnowTirol(t)
	site, _, calls := snowAutoSite(t, func() []byte { return data }, &status)
	require.NoError(t, site.SetSnowAuto(true))

	idle := func() bool {
		s := site.snow()
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.fetching
	}

	now := snowAt(25, 8, 0)
	site.updateSnowAuto(now)
	require.Eventually(t, func() bool { return idle() && calls.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.True(t, site.GetSnowCover())

	for _, d := range []time.Duration{time.Second, 15 * time.Minute, 29 * time.Minute} {
		site.updateSnowAuto(now.Add(d))
	}
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), calls.Load(), "not within 30 minutes")

	site.updateSnowAuto(now.Add(30 * time.Minute))
	require.Eventually(t, func() bool { return idle() && calls.Load() == 2 }, 5*time.Second, 5*time.Millisecond)

	// the setting off: no more requests
	require.NoError(t, site.SetSnowAuto(false))
	site.updateSnowAuto(now.Add(2 * time.Hour))
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(2), calls.Load())

	// on again: at once, not after the rest of the 30 minutes
	require.NoError(t, site.SetSnowAuto(true))
	site.updateSnowAuto(now.Add(2*time.Hour + time.Minute))
	require.Eventually(t, func() bool { return idle() && calls.Load() == 3 }, 5*time.Second, 5*time.Millisecond)
}

// TestSnowAutoRestore: the setting, the mark of an automatic switch and the
// counted snow survive a restart
func TestSnowAutoRestore(t *testing.T) {
	keepSettings(t)
	noSettingsDB(t)

	a := &Site{log: util.NewLogger("test")}
	require.NoError(t, a.SetSnowAuto(true))
	a.setSnowCover(true, true)
	a.snow().seen = snowTirolSnowEnd
	settings.SetTime(keys.SnowSeen, snowTirolSnowEnd)

	b := &Site{log: util.NewLogger("test")}
	b.restoreSnowCover()
	assert.True(t, b.GetSnowAuto())
	assert.True(t, b.GetSnowCover())
	assert.True(t, b.snow().byAuto)
	assert.True(t, snowTirolSnowEnd.Equal(b.snow().seen))

	// by hand: not marked
	require.NoError(t, a.SetSnowCover(false))
	require.NoError(t, a.SetSnowCover(true))
	c := &Site{log: util.NewLogger("test")}
	c.restoreSnowCover()
	assert.True(t, c.GetSnowCover())
	assert.False(t, c.snow().byAuto)

	// the mark without the switch is dropped
	settings.SetBool(keys.SnowCover, false)
	settings.SetBool(keys.SnowCoverAuto, true)
	d := &Site{log: util.NewLogger("test")}
	d.restoreSnowCover()
	assert.False(t, d.snow().byAuto)
}

// snowTestData is the recorded answer to change: further days of snow added
type snowTestData struct {
	Time     []int64
	Snowfall []float64
	Temp     []float64
	Sunrise  []int64
}

func readSnowTirolResponse(t *testing.T) *snowTestData {
	t.Helper()

	var r snowResponse
	require.NoError(t, json.Unmarshal(readSnowTirol(t), &r))

	d := &snowTestData{Time: r.Minutely15.Time, Sunrise: r.Daily.Sunrise}
	for i := range d.Time {
		d.Snowfall = append(d.Snowfall, *r.Minutely15.Snowfall[i])
		d.Temp = append(d.Temp, *r.Minutely15.Temp[i])
	}

	return d
}

// add lets it snow n quarter hours, the first ending at from, after the data
// that is there; quarter hours in between are dry and mild
func (d *snowTestData) add(from time.Time, n int, cm, temp float64) {
	for t := time.Unix(d.Time[len(d.Time)-1], 0).Add(15 * time.Minute); t.Before(from.Add(time.Duration(n) * 15 * time.Minute)); t = t.Add(15 * time.Minute) {
		snow, tmp := 0.0, 5.0
		if !t.Before(from) {
			snow, tmp = cm, temp
		}
		d.Time = append(d.Time, t.Unix())
		d.Snowfall = append(d.Snowfall, snow)
		d.Temp = append(d.Temp, tmp)
	}
}

func (d *snowTestData) json(t *testing.T) []byte {
	t.Helper()

	b, err := json.Marshal(map[string]any{
		"minutely_15": map[string]any{"time": d.Time, "snowfall": d.Snowfall, "temperature_2m": d.Temp},
		"daily":       map[string]any{"sunrise": d.Sunrise},
	})
	require.NoError(t, err)

	return b
}
