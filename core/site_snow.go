package core

// Custom extension: snow on the pv modules. With a switch in the ui the user
// says "snow lies": the optimizer then plans without solar yield, as the
// forecast shows a yield the modules do not deliver. The switch turns itself
// off once the system produces near the forecast again; with the detection on
// it also turns itself on, see site_snow_auto.go. Without the switch nothing
// changes. See the README for the rules.

import (
	"sync"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/tariff"
	optimizer "github.com/evcc-io/optimizer/client"
)

const (
	// a completed slot counts as free when the pv energy is at least this share of the forecast
	snowClearRatio = 0.7
	// the switch turns off after this many free slots in a row
	snowClearSlots = 4
	// slots with less forecast energy (Wh) say nothing: night, dawn, overcast
	snowMinForecastWh = 100
)

// snowState is the state of snow on pv
type snowState struct {
	mu    sync.Mutex
	cover bool      // the switch
	clear int       // free slots in a row, memory only
	slot  time.Time // start of the last slot rated, memory only

	// detection from the weather, see site_snow_auto.go
	auto     bool      // the setting
	byAuto   bool      // the switch was turned on by the detection
	seen     time.Time // end of the last snow slot counted
	fetchAt  time.Time // last attempt, memory only
	fetching bool      // an attempt is running, memory only
	failed   bool      // the last attempt failed, memory only
}

// snow returns the snow on pv state
func (site *Site) snow() *snowState {
	return &site.custom.snow
}

// restoreSnowCover applies the persisted switch
func (site *Site) restoreSnowCover() {
	s := site.snow()

	s.mu.Lock()
	if v, err := settings.Bool(keys.SnowCover); err == nil {
		s.cover = v
	}
	if v, err := settings.Bool(keys.SnowCoverAuto); err == nil {
		s.byAuto = v && s.cover
	}
	if v, err := settings.Bool(keys.SnowAuto); err == nil {
		s.auto = v
	}
	if v, err := settings.Time(keys.SnowSeen); err == nil {
		s.seen = v
	}
	cover, byAuto, auto := s.cover, s.byAuto, s.auto
	s.mu.Unlock()

	site.publish(keys.SnowCover, cover)
	site.publish(keys.SnowCoverAuto, byAuto)
	site.publish(keys.SnowAuto, auto)
	site.updateSnowAvailable()
}

// GetSnowCover returns the snow on pv switch
func (site *Site) GetSnowCover() bool {
	s := site.snow()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.cover
}

// SetSnowCover switches snow on pv by hand. The optimizer runs again at once,
// as for upstream's adjusted forecast.
func (site *Site) SetSnowCover(val bool) error {
	site.setSnowCover(val, false)
	return nil
}

// setSnowCover switches snow on pv, byAuto when the detection turns it on
func (site *Site) setSnowCover(val, byAuto bool) {
	s := site.snow()

	s.mu.Lock()
	changed := s.cover != val
	if changed {
		s.cover = val
		s.byAuto = val && byAuto
	}
	s.clear = 0
	s.slot = time.Time{}
	s.mu.Unlock()

	if !changed {
		return
	}

	site.log.DEBUG.Println("set snow on pv:", val, "automatic:", val && byAuto)
	settings.SetBool(keys.SnowCover, val)
	settings.SetBool(keys.SnowCoverAuto, val && byAuto)
	site.publish(keys.SnowCover, val)
	site.publish(keys.SnowCoverAuto, val && byAuto)

	go site.optimizerUpdateAsync(0)
}

// snowClearStep rates a completed slot: the new count of free slots in a row
// and whether the switch turns off. A slot with too little forecast energy
// leaves the count as it is.
func snowClearStep(count int, pv, fcst float64) (int, bool) {
	if fcst < snowMinForecastWh {
		return count, false
	}

	if pv/fcst >= snowClearRatio {
		count++
	} else {
		count = 0
	}

	return count, count >= snowClearSlots
}

// snowSlotEnergy returns the summed energy in Wh of the last completed metrics
// slot of the collectors, false until all of them have it
func (site *Site) snowSlotEnergy(refs ...string) (float64, bool) {
	if len(refs) == 0 {
		return 0, false
	}

	var sum float64
	for _, ref := range refs {
		c, ok := site.collectors[ref]
		if !ok {
			return 0, false
		}

		v, ok := c.LastSlotEnergy()
		if !ok {
			return 0, false
		}
		sum += v
	}

	return sum * 1e3, true
}

// updateSnowCover rates every completed slot once while the switch is on and
// turns it off after enough free slots, called with the clock's time each cycle
func (site *Site) updateSnowCover(now time.Time) {
	site.updateSnowAuto(now)

	s := site.snow()

	s.mu.Lock()
	on, count, last := s.cover, s.clear, s.slot
	s.mu.Unlock()

	// the slot the collectors report as completed
	slot := now.Truncate(tariff.SlotDuration).Add(-tariff.SlotDuration)

	if !on || slot.Equal(last) {
		return
	}

	// the same energies upstream's blend of the first slots uses, the forecast
	// unscaled; until the collectors have the slot it is tried again next cycle
	pv, ok := site.snowSlotEnergy(site.Meters.PVMetersRef...)
	if !ok {
		return
	}
	fcst, ok := site.snowSlotEnergy(metrics.Forecast)
	if !ok {
		return
	}

	count, off := snowClearStep(count, pv, fcst)

	s.mu.Lock()
	if !s.cover {
		// switched off meanwhile
		s.mu.Unlock()
		return
	}
	// snow the detection counted is still to come, e.g. tonight's snow found on a
	// sunny afternoon: the free modules of now say nothing about tomorrow
	if off && s.seen.After(now) {
		off, count = false, 0
	}
	s.clear, s.slot = count, slot
	s.mu.Unlock()

	if off {
		site.log.INFO.Println("pv snow cover off: production back to 70 % of the forecast for 1 h")
		_ = site.SetSnowCover(false)
	}
}

// applySnowCover takes the solar yield out of the optimizer request while the
// switch is on, after upstream has built the series
func (site *Site) applySnowCover(req *optimizer.OptimizationInput) {
	if !site.GetSnowCover() {
		return
	}

	clear(req.TimeSeries.Ft)
}
