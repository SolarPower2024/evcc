package core

// Custom extension: the monthly peak from the meter of the main circuit.
//
// The grid meter's import counter is not always what the grid operator bills:
// where part of the import is not metered by it (an energy community), the
// main circuit's own meter, which sees the real power at the connection, can
// be the better source. If the main circuit has a meter of its own with an
// energy counter, the fork reads it every cycle:
//   - the peak window (peakEnergy) takes it before the grid meter's counter,
//   - the collector "grid-peak" records it per 15 minute slot in a group of its
//     own, in no balance, flow or chart, for evcc's monthly peak, which reads
//     it through gridPeakCollector once the update with it is in.
//
// The circuit's configuration is read as it is (its meter reference, resolved
// through the meters), nothing in the circuit code is touched. Without such a
// meter nothing here runs and the peak comes from the grid meter as before.

import (
	"errors"
	"sync"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
	"github.com/spf13/cast"
)

type peakCircuitState struct {
	mu        sync.Mutex
	ref       string             // meter of the main circuit the collector records, empty = none
	collector *metrics.Collector // records the counter per slot
	reading   *float64           // counter read this cycle, valid until peakEnergy takes it
	failing   bool               // a failing read was logged
	clock     clock.Clock        // for tests, nil = real time
}

func (site *Site) peakCircuit() *peakCircuitState {
	return &site.custom.peakCircuit
}

// rootCircuitMeter returns the name and the energy counter of the main circuit's
// own meter, empty and nil if the circuit has none or it has no energy
func rootCircuitMeter() (string, api.MeterEnergy) {
	root := circuit.Root()
	if root == nil {
		return "", nil
	}

	for _, dev := range config.Circuits().Devices() {
		if dev.Instance() != root {
			continue
		}

		ref := cast.ToString(dev.Config().Property("meter"))
		if ref == "" {
			return "", nil
		}

		md, err := config.Meters().ByName(ref)
		if err != nil || md.Instance() == nil {
			return "", nil
		}

		if me, ok := api.Cap[api.MeterEnergy](md.Instance()); ok {
			return ref, me
		}
		return "", nil
	}

	return "", nil
}

// updatePeakCircuit reads the main circuit meter's counter and records it. Called
// every cycle before peak shaving, does nothing without such a meter.
func (site *Site) updatePeakCircuit() {
	s := site.peakCircuit()

	ref, me := rootCircuitMeter()

	// the grid meter itself: nothing new to read, its counter is the source anyway
	if ref != "" && ref == site.Meters.GridMeterRef {
		ref, me = "", nil
	}

	s.mu.Lock()
	s.reading = nil
	prev, c := s.ref, s.collector
	s.mu.Unlock()

	if me == nil {
		if prev != "" {
			s.mu.Lock()
			s.ref, s.collector, s.failing = "", nil, false
			s.mu.Unlock()
		}
		return
	}

	// once per meter: a failing start is logged once, not every cycle
	if ref != prev {
		var err error
		if c, err = site.applyPeakCircuit(ref, prev); err != nil {
			site.log.ERROR.Printf("peak circuit: %v", err)
		}
	}

	v, err := nonZeroEnergy(me.TotalEnergy())

	s.mu.Lock()
	warn, recovered := err != nil && !errors.Is(err, api.ErrNotAvailable) && !s.failing, err == nil && s.failing
	s.failing = err != nil && !errors.Is(err, api.ErrNotAvailable)
	if err == nil {
		s.reading = &v
	}
	s.mu.Unlock()

	switch {
	case warn:
		site.log.WARN.Printf("peak circuit: meter %s: %v", ref, err)
	case recovered:
		site.log.INFO.Printf("peak circuit: meter %s readable again", ref)
	}

	if err != nil || c == nil {
		return
	}

	if err := c.SetEnergyMeterTotal(v); err != nil {
		site.log.ERROR.Printf("peak circuit: %v", err)
	}
}

// applyPeakCircuit starts the recording of the circuit meter ref. A meter other
// than the one recorded before, in this run or the one before, starts a fresh
// recording without the previous counter's reading, so the difference between
// the two is not taken as import.
func (site *Site) applyPeakCircuit(ref, prev string) (*metrics.Collector, error) {
	s := site.peakCircuit()

	stored, _ := settings.String(keys.GridPeakMeter)
	changed := (prev != "" && prev != ref) || (stored != "" && stored != ref)

	// the meter is taken note of whatever follows, so a failure does not repeat
	s.mu.Lock()
	s.ref, s.collector, s.failing = ref, nil, false
	s.mu.Unlock()

	var c *metrics.Collector

	if db.Instance != nil {
		var opt []func(*metrics.Accumulator)
		if s.clock != nil {
			opt = append(opt, metrics.WithClock(s.clock))
		}

		var err error
		if c, err = metrics.NewCollector(metrics.GridPeak, metrics.GridPeakName, metrics.GridPeakName, opt...); err != nil {
			return nil, err
		}

		// drops the reading restored from the database
		if changed {
			if err := c.SetCapabilities(false, false); err != nil {
				return nil, err
			}
		}
	}

	settings.SetString(keys.GridPeakMeter, ref)

	s.mu.Lock()
	s.ref, s.collector, s.failing = ref, c, false
	s.mu.Unlock()

	return c, nil
}

// peakCircuitEnergy returns the counter read this cycle, ok false without one
func (site *Site) peakCircuitEnergy() (float64, bool) {
	s := site.peakCircuit()

	s.mu.Lock()
	defer s.mu.Unlock()

	v := s.reading
	s.reading = nil // only valid for the cycle it was read in

	if v == nil {
		return 0, false
	}
	return *v, true
}

// gridPeakCollector is the collector evcc's monthly peak reads: the circuit
// meter's, else the grid meter's as upstream
func (site *Site) gridPeakCollector() *metrics.Collector {
	s := site.peakCircuit()

	s.mu.Lock()
	c := s.collector
	s.mu.Unlock()

	if c != nil {
		return c
	}

	return site.collectors[site.Meters.GridMeterRef]
}
