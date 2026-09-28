package core

// Custom extension: home consumption forecast from an uploaded load profile
// (Lastmanagement-Details → Erweitert → Verbrauchsprognose → Manuell). The
// profile holds the average home power per quarter hour for each month,
// working days and weekends apart, e.g. from last year's meter data. The
// forecast mixes it with evcc's own measurements of the last 8 weeks:
//
//  1. the profile, interpolated between the months and scaled to the level of
//     the last weeks
//  2. the last weeks' own profile per day type
//  3. both mixed: the more history and the worse the profile fits it (shape
//     or level), the more the last weeks count
//  4. a strong deviation of the last 3 hours (more than 25 %) carries over into
//     the next hours and fades out
//
// Recent days count more than older ones (half-life 4 days). The safety margin
// (upstream profilePercentile) applies to the last weeks' profile. Without a
// profile or on errors evcc's own forecast applies. Only the forecast changes.

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/tariff"
	"github.com/jinzhu/now"
)

const (
	homeManualDays     = 56   // days of history the forecast looks at
	homeManualHalfLife = 4.0  // days after which a day counts half
	homeManualFull     = 14.0 // days of history after which the history counts fully
	homeManualShort    = 12   // slots of the short-term deviation (3 hours)
	homeManualDecay    = 8.0  // slots in which the short-term deviation fades to a third
	homeManualDeviate  = 0.25 // short-term deviation that counts as strong

	homeProfileMaxSize = 1 << 20 // bytes of an uploaded file
	homeProfileMaxW    = 100000  // W, highest value accepted
)

// homeLoadProfile is an uploaded load profile: the average home power per
// quarter hour in W for each month, working days and weekends apart
type homeLoadProfile struct {
	Name     string             `json:"name"`
	Uploaded time.Time          `json:"uploaded"`
	Months   []int              `json:"months"` // months the file gave, 1..12
	Watts    [12][2][96]float64 `json:"watts"`  // month, day type (0 working day, 1 weekend), slot
}

// homeLoadProfileState is what the ui shows of the profile
type homeLoadProfileState struct {
	Name     string      `json:"name"`
	Uploaded time.Time   `json:"uploaded"`
	Months   []int       `json:"months"`
	Daily    [12]float64 `json:"daily"` // average kWh per day by month
}

// loadHomeLoadProfile returns the stored profile, nil without
func loadHomeLoadProfile() *homeLoadProfile {
	var p homeLoadProfile
	if err := settings.Json(keys.LmHomeProfile, &p); err != nil || len(p.Months) == 0 {
		return nil
	}
	return &p
}

func (site *Site) publishLmHomeProfile() {
	p := loadHomeLoadProfile()
	if p == nil {
		site.publish(keys.LmHomeProfile, nil)
		return
	}

	res := homeLoadProfileState{Name: p.Name, Uploaded: p.Uploaded, Months: p.Months}
	for m := range 12 {
		var wt, we float64
		for i := range 96 {
			wt += p.Watts[m][0][i] / 4000
			we += p.Watts[m][1][i] / 4000
		}
		res.Daily[m] = math.Round((5*wt+2*we)/7*10) / 10
	}

	site.publish(keys.LmHomeProfile, res)
}

// SetLmHomeProfile stores an uploaded load profile
func (site *Site) SetLmHomeProfile(name string, data []byte) error {
	if len(data) > homeProfileMaxSize {
		return errors.New("file too large")
	}

	p, err := parseHomeLoadProfile(data)
	if err != nil {
		return err
	}
	p.Name = name
	p.Uploaded = time.Now()

	if err := settings.SetJson(keys.LmHomeProfile, p); err != nil {
		return err
	}

	site.log.DEBUG.Printf("home load profile %q stored, months %v", name, p.Months)
	site.publishLmHomeProfile()

	if site.homeForecast() == homeForecastManual {
		site.Optimize() // the home demand forecast changed
	}

	return nil
}

// DeleteLmHomeProfile removes the uploaded load profile
func (site *Site) DeleteLmHomeProfile() error {
	if err := settings.Delete(keys.LmHomeProfile); err != nil && !errors.Is(err, settings.ErrNotFound) {
		return err
	}

	site.publishLmHomeProfile()

	if site.homeForecast() == homeForecastManual {
		site.Optimize()
	}

	return nil
}

// LmHomeProfileCsv returns the uploaded load profile as csv, nil without
func (site *Site) LmHomeProfileCsv() []byte {
	p := loadHomeLoadProfile()
	if p == nil {
		return nil
	}
	return p.csv()
}

// csv writes the profile in the upload format, all months and day types
func (p *homeLoadProfile) csv() []byte {
	var b bytes.Buffer

	b.WriteString("zeit")
	for m := range 12 {
		fmt.Fprintf(&b, ";%02d-werktag;%02d-wochenende", m+1, m+1)
	}
	b.WriteString("\n")

	for i := range 96 {
		fmt.Fprintf(&b, "%02d:%02d", i/4, i%4*15)
		for m := range 12 {
			fmt.Fprintf(&b, ";%.0f;%.0f", p.Watts[m][0][i], p.Watts[m][1][i])
		}
		b.WriteString("\n")
	}

	return b.Bytes()
}

// homeProfileColumn is a column header: the month, optionally with a day type
var homeProfileColumn = regexp.MustCompile(`(?i)^(\d{1,2})(?:\s*[-_ /]\s*(wt|werktage?|weekdays?|we|wochenende|weekends?|alle|all))?$`)

// parseHomeLoadProfile reads a load profile from csv: a time column and a
// column per month (1-12), optionally per day type (e.g. 01-werktag,
// 01-wochenende), with the average power in W for 96 quarter hours or 24
// hours. Separator ; , or tab, decimal comma with ; or tab. Lines starting
// with # are comments. Missing day types take the other one, missing months
// the nearest given month.
func parseHomeLoadProfile(data []byte) (homeLoadProfile, error) {
	var res homeLoadProfile

	var lines []string
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\xef\xbb\xbf"), "\r", "")
	for l := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) < 2 {
		return res, errors.New("no data")
	}

	sep := ","
	switch {
	case strings.Contains(lines[0], ";"):
		sep = ";"
	case strings.Contains(lines[0], "\t"):
		sep = "\t"
	}

	type column struct {
		month int
		types []int
	}

	header := trimEmpty(strings.Split(lines[0], sep))
	if len(header) < 2 {
		return res, errors.New("header: expected a time column and at least one month column")
	}

	var given [12][2]bool
	cols := make([]column, 0, len(header)-1)

	for _, h := range header[1:] {
		h = strings.Trim(strings.TrimSpace(h), `"`)
		m := homeProfileColumn.FindStringSubmatch(h)
		if m == nil {
			return res, fmt.Errorf("column %q: expected a month 1-12, optionally with -werktag or -wochenende", h)
		}

		month, _ := strconv.Atoi(m[1])
		if month < 1 || month > 12 {
			return res, fmt.Errorf("column %q: month must be 1-12", h)
		}

		types := []int{0, 1}
		switch strings.ToLower(m[2]) {
		case "wt", "werktag", "werktage", "weekday", "weekdays":
			types = []int{0}
		case "we", "wochenende", "weekend", "weekends":
			types = []int{1}
		}

		for _, t := range types {
			if given[month-1][t] {
				return res, fmt.Errorf("column %q: month given twice", h)
			}
			given[month-1][t] = true
		}

		cols = append(cols, column{month - 1, types})
	}

	rows := lines[1:]
	step := 0
	switch len(rows) {
	case 96:
		step = 1
	case 24:
		step = 4
	default:
		return res, fmt.Errorf("expected 96 rows (quarter hours) or 24 rows (hours), got %d", len(rows))
	}

	for r, line := range rows {
		cells := strings.Split(line, sep)
		if len(cells) > len(header) {
			// empty cells after the last column, as spreadsheets may write them
			if extra := trimEmpty(cells[len(header):]); len(extra) == 0 {
				cells = cells[:len(header)]
			}
		}
		if len(cells) != len(header) {
			return res, fmt.Errorf("row %d: %d values, expected %d", r+2, len(cells), len(header))
		}

		slot := r * step
		want := fmt.Sprintf("%02d:%02d", slot/4, slot%4*15)
		if got := normalizeProfileTime(cells[0]); got != want {
			return res, fmt.Errorf("row %d: time %q, expected %s", r+2, strings.TrimSpace(cells[0]), want)
		}

		for c, cell := range cells[1:] {
			cell = strings.Trim(strings.TrimSpace(cell), `"`)
			if sep != "," {
				cell = strings.ReplaceAll(cell, ",", ".")
			}

			v, err := strconv.ParseFloat(cell, 64)
			if err != nil || v < 0 || v > homeProfileMaxW || math.IsNaN(v) {
				return res, fmt.Errorf("row %d, column %q: expected W from 0 to %d, got %q", r+2, strings.TrimSpace(header[c+1]), homeProfileMaxW, cell)
			}

			for _, t := range cols[c].types {
				for k := range step {
					res.Watts[cols[c].month][t][slot+k] = v
				}
			}
		}
	}

	// a missing day type takes the other one
	for m := range 12 {
		switch {
		case given[m][0] && !given[m][1]:
			res.Watts[m][1] = res.Watts[m][0]
		case given[m][1] && !given[m][0]:
			res.Watts[m][0] = res.Watts[m][1]
		}
		if given[m][0] || given[m][1] {
			res.Months = append(res.Months, m+1)
		}
	}

	// a missing month takes the nearest given one, the earlier on a tie
	for m := range 12 {
		if slices.Contains(res.Months, m+1) {
			continue
		}
		for d := 1; d < 12; d++ {
			if prev := (m - d + 12) % 12; slices.Contains(res.Months, prev+1) {
				res.Watts[m] = res.Watts[prev]
				break
			}
			if next := (m + d) % 12; slices.Contains(res.Months, next+1) {
				res.Watts[m] = res.Watts[next]
				break
			}
		}
	}

	return res, nil
}

// trimEmpty drops empty cells at the end
func trimEmpty(cells []string) []string {
	for len(cells) > 0 && strings.Trim(strings.TrimSpace(cells[len(cells)-1]), `"`) == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// normalizeProfileTime returns a time cell as hh:mm, "" if it is none
func normalizeProfileTime(s string) string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(s), `"`), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return ""
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}

// homeProfileManual is the home forecast in Wh for minLen slots starting now
// from the uploaded load profile, false without a profile or on errors
func (site *Site) homeProfileManual(col *metrics.Collector, minLen int) ([]float64, bool) {
	p := loadHomeLoadProfile()
	if p == nil {
		site.log.DEBUG.Println("home forecast: no load profile uploaded, using evcc's forecast")
		return nil, false
	}

	start := time.Now().Truncate(tariff.SlotDuration)
	hist, err := col.Slots(now.With(start).BeginningOfDay().AddDate(0, 0, -homeManualDays))
	if err != nil {
		site.log.ERROR.Printf("home forecast: %v, using evcc's forecast", err)
		return nil, false
	}

	var percentile float64
	if v := site.GetProfilePercentile(); v != nil {
		percentile = *v / 100
	}

	res := homeManualForecast(p, hist, start, minLen, percentile)
	for i := range res {
		res[i] /= 4 // W over a quarter hour to Wh
	}

	return res, true
}

// homeDay is a past day of measured home power in W per slot
type homeDay struct {
	date  time.Time
	sum   [96]float64 // W, summed over the slots of the same time
	count [96]int     // two on the day the clocks go back
	watts [96]float64 // the day's power, see complete
}

// complete averages the slots and fills single missing ones (a restart, the
// day the clocks go forward) from their neighbours. False if more than 6 are
// missing.
func (d *homeDay) complete() bool {
	var given []int
	for i := range 96 {
		if d.count[i] > 0 {
			d.watts[i] = d.sum[i] / float64(d.count[i])
			given = append(given, i)
		}
	}
	if len(given) < 90 {
		return false
	}

	for i := range 96 {
		if d.count[i] > 0 {
			continue
		}
		// nearest given slots before and after, around midnight
		prev, next := -1, -1
		for k := 1; k < 96 && (prev < 0 || next < 0); k++ {
			if prev < 0 && d.count[(i-k+96)%96] > 0 {
				prev = k
			}
			if next < 0 && d.count[(i+k)%96] > 0 {
				next = k
			}
		}
		a, b := d.watts[(i-prev+96)%96], d.watts[(i+next)%96]
		d.watts[i] = a + (b-a)*float64(prev)/float64(prev+next)
	}

	return true
}

// homeManualForecast is the home power forecast in W for n slots from start,
// see the top of this file. hist are the measured slots before start.
func homeManualForecast(p *homeLoadProfile, hist []metrics.MeterSlot, start time.Time, n int, percentile float64) []float64 {
	today := now.With(start).BeginningOfDay()

	byDate := make(map[time.Time]*homeDay)
	var short []metrics.MeterSlot

	for _, s := range hist {
		if !s.Start.Before(start) {
			continue
		}
		if start.Sub(s.Start) <= homeManualShort*tariff.SlotDuration {
			short = append(short, s)
		}

		date := now.With(s.Start).BeginningOfDay()
		if !date.Before(today) {
			continue
		}

		d, ok := byDate[date]
		if !ok {
			d = &homeDay{date: date}
			byDate[date] = d
		}
		i := slotOfDay(s.Start)
		d.sum[i] += s.Energy * 4000 // kWh per quarter hour to W
		d.count[i]++
	}

	// complete days only, a restart may leave a few slots out
	var days []*homeDay
	for _, d := range byDate {
		if d.complete() {
			days = append(days, d)
		}
	}

	// level, shapes and the last weeks' profile, recent days weigh more
	var (
		sumW, sumA, sumP float64
		histAll, prior   [96]float64
		histType         [2][96]float64
		weightType       [2]float64
		values           [2][96][]weightedValue
	)

	for _, d := range days {
		age := today.Sub(d.date).Hours() / 24
		w := math.Pow(0.5, age/homeManualHalfLife)
		dt := dayType(d.date)
		smooth := smoothSlots(d.watts)

		for i := range 96 {
			pv := p.at(d.date.Add(time.Duration(i) * tariff.SlotDuration))
			sumA += w * d.watts[i]
			sumP += w * pv
			prior[i] += w * pv
			histAll[i] += w * smooth[i]
			histType[dt][i] += w * smooth[i]
			if percentile > 0 {
				values[dt][i] = append(values[dt][i], weightedValue{smooth[i], w})
			}
		}

		sumW += w
		weightType[dt] += w
	}

	level := 1.0
	if sumP > 0 && sumA > 0 {
		level = min(max(sumA/sumP, 0.1), 10)
	}

	// share of the last weeks: 0.5 while the profile fits them, up to 0.9 if
	// its shape or level does not, less while the history is short
	var mix float64
	if len(days) > 0 {
		fit := min(max((pearson(histAll, prior)-0.3)/0.4, 0), 1)
		fit *= max(0, 1-math.Abs(math.Log2(level))/2)
		mix = min(1, float64(len(days))/homeManualFull) * (0.9 - 0.4*fit)
	}

	recent := func(dt, i int) float64 {
		if weightType[dt] < 1 {
			// hardly any day of this type: all days
			var all []weightedValue
			if percentile > 0 {
				all = append(slices.Clone(values[0][i]), values[1][i]...)
				return weightedQuantile(all, percentile)
			}
			return histAll[i] / sumW
		}
		if percentile > 0 {
			return weightedQuantile(values[dt][i], percentile)
		}
		return histType[dt][i] / weightType[dt]
	}

	model := func(t time.Time) float64 {
		v := level * p.at(t)
		if mix > 0 {
			v = mix*recent(dayType(t), slotOfDay(t)) + (1-mix)*v
		}
		return max(0, v)
	}

	// strong deviation of the last hours, e.g. a cold spell or guests
	ratio := 1.0
	if len(short) >= homeManualShort/2 {
		var actual, expected float64
		for _, s := range short {
			actual += s.Energy * 4000
			expected += model(s.Start)
		}
		if expected > 0 {
			if r := min(max(actual/expected, 0.5), 2); math.Abs(r-1) > homeManualDeviate {
				ratio = r
			}
		}
	}

	res := make([]float64, n)
	for i := range res {
		v := model(start.Add(time.Duration(i) * tariff.SlotDuration))
		if ratio != 1 {
			v *= 1 + (ratio-1)*math.Exp(-float64(i)/homeManualDecay)
		}
		res[i] = v
	}

	return res
}

// at is the profile's power in W at t, interpolated between the months: the
// month's value applies at its middle
func (p *homeLoadProfile) at(t time.Time) float64 {
	dt, i := dayType(t), slotOfDay(t)
	m := int(t.Month()) - 1

	days := float64(time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day())
	d := (float64(t.Day()-1)+float64(i)/96)/days - 0.5

	other := (m + 1) % 12
	if d < 0 {
		other = (m + 11) % 12
		d = -d
	}

	return (1-d)*p.Watts[m][dt][i] + d*p.Watts[other][dt][i]
}

// dayType is 1 on weekends, else 0
func dayType(t time.Time) int {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return 1
	}
	return 0
}

func slotOfDay(t time.Time) int {
	return t.Hour()*4 + t.Minute()/15
}

// smoothSlots evens out single slots: a quarter of each neighbour
func smoothSlots(v [96]float64) [96]float64 {
	var res [96]float64
	for i := range 96 {
		res[i] = 0.25*v[(i+95)%96] + 0.5*v[i] + 0.25*v[(i+1)%96]
	}
	return res
}

// pearson is the correlation of two day shapes: 1 if both are flat, 0 if only one is
func pearson(a, b [96]float64) float64 {
	var ma, mb float64
	for i := range 96 {
		ma += a[i] / 96
		mb += b[i] / 96
	}

	var cov, va, vb float64
	for i := range 96 {
		cov += (a[i] - ma) * (b[i] - mb)
		va += (a[i] - ma) * (a[i] - ma)
		vb += (b[i] - mb) * (b[i] - mb)
	}

	const flat = 1e-6
	switch {
	case va < flat && vb < flat:
		return 1
	case va < flat || vb < flat:
		return 0
	}

	return cov / math.Sqrt(va*vb)
}

type weightedValue struct {
	value, weight float64
}

// weightedQuantile is the value below which the share q of the weight lies
func weightedQuantile(v []weightedValue, q float64) float64 {
	if len(v) == 0 {
		return 0
	}

	v = slices.Clone(v)
	slices.SortFunc(v, func(a, b weightedValue) int {
		switch {
		case a.value < b.value:
			return -1
		case a.value > b.value:
			return 1
		}
		return 0
	})

	var total float64
	for _, x := range v {
		total += x.weight
	}

	var sum float64
	for _, x := range v {
		sum += x.weight
		if sum >= q*total {
			return x.value
		}
	}

	return v[len(v)-1].value
}
