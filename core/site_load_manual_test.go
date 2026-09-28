package core

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profileCsv writes a load profile: header columns and a value per column and slot
func profileCsv(sep string, cols []string, rows int, value func(col, row int) string) string {
	var b strings.Builder
	b.WriteString("# test profile\nzeit" + sep + strings.Join(cols, sep) + "\n")
	step := 96 / rows
	for r := range rows {
		slot := r * step
		fmt.Fprintf(&b, "%02d:%02d", slot/4, slot%4*15)
		for c := range cols {
			b.WriteString(sep + value(c, r))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestParseHomeLoadProfile(t *testing.T) {
	var cols []string
	for m := 1; m <= 12; m++ {
		cols = append(cols, fmt.Sprintf("%02d-werktag", m), fmt.Sprintf("%02d-wochenende", m))
	}

	// quarter hours, decimal comma
	p, err := parseHomeLoadProfile([]byte("\xef\xbb\xbf" + profileCsv(";", cols, 96, func(c, r int) string {
		return fmt.Sprintf("%d,5", c*1000+r)
	})))
	require.NoError(t, err)
	assert.Len(t, p.Months, 12)
	assert.Equal(t, 10.5, p.Watts[0][0][10])
	assert.Equal(t, 1010.5, p.Watts[0][1][10])
	assert.Equal(t, 23095.5, p.Watts[11][1][95])

	// and back
	again, err := parseHomeLoadProfile(p.csv())
	require.NoError(t, err)
	for m := range 12 {
		for dt := range 2 {
			for i := range 96 {
				assert.InDelta(t, p.Watts[m][dt][i], again.Watts[m][dt][i], 0.5)
			}
		}
	}

	// hours, comma separated, some months only, one day type
	p, err = parseHomeLoadProfile([]byte(profileCsv(",", []string{"1", "7-we", "7 wt"}, 24, func(c, r int) string {
		return fmt.Sprintf("%d", (c+1)*100+r)
	})))
	require.NoError(t, err)
	assert.Equal(t, []int{1, 7}, p.Months)
	assert.Equal(t, 105.0, p.Watts[0][0][20], "hour 5 fills its four quarter hours")
	assert.Equal(t, 105.0, p.Watts[0][1][23], "a month without day type is both")
	assert.Equal(t, 305.0, p.Watts[6][0][20])
	assert.Equal(t, 205.0, p.Watts[6][1][20])
	assert.Equal(t, p.Watts[0], p.Watts[3], "april is nearest to january on a tie")
	assert.Equal(t, p.Watts[6], p.Watts[5])
	assert.Equal(t, p.Watts[0], p.Watts[11])

	// a single day type takes the other one
	p, err = parseHomeLoadProfile([]byte(profileCsv("\t", []string{"03-werktag"}, 24, func(c, r int) string { return "500" })))
	require.NoError(t, err)
	assert.Equal(t, 500.0, p.Watts[2][1][0])

	for name, data := range map[string]string{
		"empty":        "",
		"no months":    "zeit\n00:00\n",
		"bad column":   profileCsv(";", []string{"januar"}, 24, func(c, r int) string { return "1" }),
		"month 13":     profileCsv(";", []string{"13"}, 24, func(c, r int) string { return "1" }),
		"twice":        profileCsv(";", []string{"1", "1-we"}, 24, func(c, r int) string { return "1" }),
		"rows":         profileCsv(";", []string{"1"}, 24, func(c, r int) string { return "1" }) + "00:00;1\n",
		"negative":     profileCsv(";", []string{"1"}, 24, func(c, r int) string { return "-1" }),
		"text":         profileCsv(";", []string{"1"}, 24, func(c, r int) string { return "x" }),
		"missing cell": strings.Replace(profileCsv(";", []string{"1", "2"}, 24, func(c, r int) string { return "1" }), "05:00;1;1", "05:00;1", 1),
		"time":         strings.Replace(profileCsv(";", []string{"1"}, 24, func(c, r int) string { return "1" }), "05:00", "05:30", 1),
	} {
		_, err := parseHomeLoadProfile([]byte(data))
		assert.Error(t, err, name)
	}
}

// flatProfile has the same power in every slot of a month
func flatProfile(watts func(month, dt, slot int) float64) *homeLoadProfile {
	p := &homeLoadProfile{Months: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}}
	for m := range 12 {
		for dt := range 2 {
			for i := range 96 {
				p.Watts[m][dt][i] = watts(m, dt, i)
			}
		}
	}
	return p
}

func TestHomeLoadProfileAt(t *testing.T) {
	p := flatProfile(func(m, dt, i int) float64 { return float64((m + 1) * 100) })

	// june has 30 days: its value applies at the middle, the 16th 00:00
	assert.Equal(t, 600.0, p.at(time.Date(2026, 6, 16, 0, 0, 0, 0, time.Local)))
	assert.InDelta(t, 650, p.at(time.Date(2026, 6, 30, 23, 59, 0, 0, time.Local)), 1, "halfway to july")
	assert.InDelta(t, 650, p.at(time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)), 1, "and on from july")
	assert.InDelta(t, 650, p.at(time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)), 1, "january starts halfway from december")

	// day types: 2026-06-13 is a saturday
	p = flatProfile(func(m, dt, i int) float64 { return float64(1000 + dt*500) })
	assert.Equal(t, 1500.0, p.at(time.Date(2026, 6, 13, 12, 0, 0, 0, time.Local)))
	assert.Equal(t, 1000.0, p.at(time.Date(2026, 6, 15, 12, 0, 0, 0, time.Local)))
}

// history returns the measured slots of the days before start, W by slot time
func history(start time.Time, days int, watts func(t time.Time) float64) []metrics.MeterSlot {
	var res []metrics.MeterSlot
	for ts := start.AddDate(0, 0, -days); ts.Before(start); ts = ts.Add(tariff.SlotDuration) {
		res = append(res, metrics.MeterSlot{Start: ts, Energy: watts(ts) / 4000})
	}
	return res
}

func TestHomeManualForecast(t *testing.T) {
	// a monday noon in mid june
	start := time.Date(2026, 6, 15, 12, 0, 0, 0, time.Local)

	// peak at 18:00, the same every month
	shape := func(i int) float64 { return 300 + 1000*math.Exp(-math.Pow(float64(i-72)/6, 2)) }
	p := flatProfile(func(m, dt, i int) float64 { return shape(i) })

	t.Run("no history: the profile", func(t *testing.T) {
		res := homeManualForecast(p, nil, start, 96, 0)
		for i, v := range res {
			assert.InDelta(t, shape((48+i)%96), v, 1e-6, "slot %d", i)
		}
	})

	t.Run("history at twice the level", func(t *testing.T) {
		hist := history(start, 28, func(ts time.Time) float64 { return 2 * shape(slotOfDay(ts)) })
		res := homeManualForecast(p, hist, start, 96, 0)
		for i, v := range res {
			assert.InDelta(t, 2*shape((48+i)%96), v, 0.05*2*shape((48+i)%96), "slot %d", i)
		}
	})

	t.Run("history of another shape: the last weeks count", func(t *testing.T) {
		hist := history(start, 28, func(time.Time) float64 { return 500 })
		res := homeManualForecast(p, hist, start, 96, 0)
		// the profile's evening peak nearly gone
		assert.InDelta(t, 500, res[24], 150, "18:00")
		assert.InDelta(t, 500, res[48], 100, "midnight")
	})

	t.Run("short history counts less", func(t *testing.T) {
		hist := history(start, 2, func(time.Time) float64 { return 500 })
		res := homeManualForecast(p, hist, start, 96, 0)
		assert.Greater(t, res[24], 700.0, "the profile's peak mostly stays")
	})

	t.Run("working days and weekends apart", func(t *testing.T) {
		hist := history(start, 28, func(ts time.Time) float64 {
			if dayType(ts) == 1 {
				return 2 * shape(slotOfDay(ts))
			}
			return shape(slotOfDay(ts))
		})
		res := homeManualForecast(p, hist, start, 6*96, 0)
		monday, saturday := res[24], res[5*96+24] // 18:00
		assert.Greater(t, saturday, 1.5*monday)
	})

	t.Run("strong deviation of the last hours", func(t *testing.T) {
		hist := history(start, 28, func(ts time.Time) float64 {
			if start.Sub(ts) <= 3*time.Hour {
				return 2 * shape(slotOfDay(ts))
			}
			return shape(slotOfDay(ts))
		})
		res := homeManualForecast(p, hist, start, 96, 0)
		assert.InDelta(t, 2*shape(48), res[0], 0.1*shape(48), "now")
		assert.InDelta(t, shape(88), res[40], 0.05*shape(88), "10 hours later")
	})

	t.Run("small deviation is noise", func(t *testing.T) {
		hist := history(start, 28, func(ts time.Time) float64 {
			if start.Sub(ts) <= 3*time.Hour {
				return 1.1 * shape(slotOfDay(ts))
			}
			return shape(slotOfDay(ts))
		})
		res := homeManualForecast(p, hist, start, 1, 0)
		assert.InDelta(t, shape(48), res[0], 0.05*shape(48))
	})

	t.Run("safety margin from the last weeks", func(t *testing.T) {
		// every other day twice the power
		hist := history(start, 28, func(ts time.Time) float64 {
			if ts.YearDay()%2 == 0 {
				return 2 * shape(slotOfDay(ts))
			}
			return shape(slotOfDay(ts))
		})
		avg := homeManualForecast(p, hist, start, 96, 0)
		high := homeManualForecast(p, hist, start, 96, 0.9)
		assert.Greater(t, high[24], avg[24])
	})

	t.Run("incomplete days are left out", func(t *testing.T) {
		var hist []metrics.MeterSlot
		for _, s := range history(start, 28, func(time.Time) float64 { return 5000 }) {
			if slotOfDay(s.Start) < 36 { // until 9:00 only, nothing in the last 3 hours
				hist = append(hist, s)
			}
		}
		res := homeManualForecast(p, hist, start, 1, 0)
		assert.InDelta(t, shape(48), res[0], 1e-6)
	})
}

func TestHomeForecastSetting(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	assert.Equal(t, homeForecastEvcc, site.homeForecast())

	require.NoError(t, site.SetLmAdvanced("homeForecast", 2))
	assert.Equal(t, homeForecastManual, site.homeForecast())
	assert.False(t, site.homeWeekday())

	require.NoError(t, site.SetLmAdvanced("homeWeekday", 1))
	assert.Equal(t, homeForecastWeekday, site.homeForecast())
	assert.True(t, site.homeWeekday())

	require.Error(t, site.SetLmAdvanced("homeForecast", 3))

	// stored before the forecast setting existed
	one := 1.0
	site.lms().adv = lmAdvanced{HomeWeekday: &one}
	assert.Equal(t, homeForecastWeekday, site.homeForecast())
}

func TestHomeProfileManualFallback(t *testing.T) {
	t.Cleanup(func() { _ = settings.Delete(keys.LmHomeProfile) })
	col := seedHome(t, 28, func(time.Weekday) float64 { return 1 })

	site := &Site{log: util.NewLogger("test"), collectors: map[string]*metrics.Collector{metrics.Home: col}}
	require.NoError(t, site.SetLmAdvanced("homeForecast", 2))

	// without a profile: evcc's forecast
	_ = settings.Delete(keys.LmHomeProfile)
	res, err := site.homeProfile(4)
	require.NoError(t, err)
	assert.Equal(t, []float64{1000, 1000, 1000, 1000}, res)

	// with one: the profile's 500 Wh per slot follow the measured level
	require.NoError(t, site.SetLmHomeProfile("test.csv", []byte(profileCsv(";", []string{"1"}, 24, func(c, r int) string { return "2000" }))))
	assert.NotNil(t, site.LmHomeProfileCsv())

	res, err = site.homeProfile(4)
	require.NoError(t, err)
	for _, v := range res {
		assert.InDelta(t, 1000, v, 1)
	}

	require.Error(t, site.SetLmHomeProfile("bad.csv", []byte("zeit;1\n")))
	assert.NotNil(t, site.LmHomeProfileCsv(), "a bad file keeps the profile")

	require.NoError(t, site.DeleteLmHomeProfile())
	assert.Nil(t, site.LmHomeProfileCsv())
}
