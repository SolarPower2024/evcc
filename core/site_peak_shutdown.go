package core

// Custom extension: hand the battery back when evcc stops.
//
// evcc only hands back its own battery mode on stopping. What the fork holds
// through Home Assistant would stay until the next cycle after the next start:
// the grid charge power and the manual control of a Marstek (it would charge from
// the grid up to full, with no peak gate and no price check), the limit of
// Omnibattery's peak shaving at the last value of the window (up to twice the
// limit), the setpoint and the charge power of a BYD. The shutdown function hands
// it back before evcc exits, see releasePeak.
//
// It is best effort: the whole hand back may take peakShutdownBudget, then evcc
// stops anyway, and with a Home Assistant that is stopping itself (the supervisor
// refuses calls with 400 or 503 "not ready" once the host shuts down) nothing
// lands. A crash or a kill is not covered either. In both cases what is stored
// (peakShavingOwned) makes the first cycle after the next start hand back.

import (
	"time"

	"github.com/evcc-io/evcc/cmd/shutdown"
)

// peakShutdownBudget is how long the shutdown function may take. The supervisor
// stops the add-on after 30 s (timeout in its manifest), evcc waits for the
// shutdown functions for max(interval, 5 s) and stops the database after them.
var peakShutdownBudget = 8 * time.Second

// registerShutdown is evcc's registration of a shutdown function, replaced in tests
var registerShutdown = shutdown.Register

// registerPeakShutdown registers the hand back with evcc's shutdown functions,
// once
func (site *Site) registerPeakShutdown() {
	s := site.peak()

	s.shutdownOnce.Do(func() { registerShutdown(site.releasePeakOnShutdown) })
}

// peakStopped reports whether evcc is stopping: the cycle writes nothing any more
func (site *Site) peakStopped() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.stopped
}

// releasePeakOnShutdown stops the cycle's writes and hands back what the fork
// holds, within peakShutdownBudget
func (site *Site) releasePeakOnShutdown() {
	s := site.peak()

	// the shutdown functions run beside a cycle that may still be running
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()

	// a call that timed out in that cycle would have every call of the hand back
	// refused until the cycle ends, see site_ha_guard.go
	site.haStop()

	done := make(chan struct{})

	go func() {
		defer close(done)
		site.releasePeak()
	}()

	select {
	case <-done:
	case <-time.After(peakShutdownBudget):
		site.log.WARN.Printf("peak shaving: handing back took longer than %s, stopping anyway", peakShutdownBudget)
	}
}

// releasePeak hands back, safety first: the grid charge power and the manual
// control of a Marstek, the limit of Omnibattery's peak shaving (it stays on, with
// the peak limit instead of the allowed power of the window), then the charge power
// and the setpoint of a BYD. What does not land stays marked as held.
func (site *Site) releasePeak() {
	s := site.peak()

	// a running cycle finishes its call first
	s.out.Lock()
	defer s.out.Unlock()

	s.mu.Lock()
	typ, limit, limitEntity, protOwned := s.batteryType, s.limit, s.protLimit, s.protOwned
	chargeSet, charging, set := s.chargeSet, s.chargeSetpoint > 0, s.set
	s.mu.Unlock()

	if typ == batteryTypeMarstek {
		if !site.releaseOmni() {
			site.log.INFO.Println("peak shaving: the manual control stays on, released with the next start")
		}

		site.restoreOmniThreshold()

		if protOwned && limitEntity != "" {
			if conn, err := site.haConnection(); err == nil {
				site.omniWriteLimit(conn, limitEntity, limit, omniLimitBasis(false, limit))
			}
		}

		return
	}

	if charging && chargeSet != nil {
		site.releaseOutput("grid charge power", chargeSet, 0)
	}

	if site.peakOwned() && set != nil && site.releaseOutput("peak shaving", set, site.peakFreeValue()) {
		site.setPeakOwned(false)
	}
}
