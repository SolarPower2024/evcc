package core

// Custom extension: a Home Assistant that does not answer must not stretch the
// site cycle.
//
// Peak shaving, grid charging and the EEG counter call Home Assistant inside the
// cycle, one after the other, each call limited by the client timeout only (10 s).
// A Home Assistant that takes the connection but does not answer (start of the
// core, an overloaded host) would cost one timeout per call: the energy sensor, the
// setpoint, the charge power, the entities of the battery type Marstek, the EEG
// counter. 40 s and more per cycle, and load management and wallboxes would be
// regulated that much later.
//
// haGuard sits in the transport of the shared connection (see haConnection). After
// the first call timing out in a cycle it refuses the further calls with errHAHung
// until the cycle ends (haCycleEnd); the first call of the next cycle asks again.
// So a cycle costs one timeout at most. Once evcc is stopping the guard refuses
// nothing (stop): the hand back of the battery has to try every call, also when a
// call timed out earlier in the cycle still running beside the shutdown function.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
)

// errHAHung refuses a call after a call in the same cycle timed out
var errHAHung = errors.New("home assistant did not answer earlier in this cycle")

// haGuard is the transport of the connection to Home Assistant
type haGuard struct {
	base http.RoundTripper
	log  *util.Logger

	mu       sync.Mutex
	hung     bool // a call timed out in this cycle
	stopping bool // evcc is stopping, nothing is refused any more
}

// guardHA puts the guard in the transport of the connection
func guardHA(conn *homeassistant.Connection, log *util.Logger) {
	conn.Client.Transport = &haGuard{base: conn.Client.Transport, log: log}
}

// RoundTrip implements http.RoundTripper
func (g *haGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	g.mu.Lock()
	hung := g.hung && !g.stopping
	g.mu.Unlock()

	if hung {
		// a round tripper closes the body also when it does not send it
		if req.Body != nil {
			_ = req.Body.Close()
		}

		return nil, errHAHung
	}

	resp, err := g.base.RoundTrip(req)

	if err != nil && isTimeout(err) {
		g.mu.Lock()
		first := !g.hung && !g.stopping
		g.hung = true
		g.mu.Unlock()

		if first {
			g.log.DEBUG.Println("home assistant: no answer, no further calls in this cycle")
		}
	}

	return resp, err
}

// endCycle lets the next call through again
func (g *haGuard) endCycle() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.hung = false
}

// stop ends the refusals for good: evcc is stopping
func (g *haGuard) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.stopping = true
	g.hung = false
}

// isTimeout reports whether the call timed out, rather than failed
func isTimeout(err error) bool {
	var ne net.Error

	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// haCycleEnd ends the cycle for the guard of the connection: the next call to
// Home Assistant is made again even if one timed out. Called at the end of the
// fork's part of the cycle, see updateBatteryModePeakAware.
func (site *Site) haCycleEnd() {
	s := site.peak()

	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()

	if conn == nil {
		return
	}

	if g, ok := conn.Client.Transport.(*haGuard); ok {
		g.endCycle()
	}
}

// haStop ends the refusals of the guard for good, see haGuard.stop: called when
// evcc stops, before the hand back. The connection is built if it does not exist
// yet, so the guard is there to be told; without Home Assistant nothing is held
// through it.
func (site *Site) haStop() {
	conn, err := site.haConnection()
	if err != nil {
		return
	}

	if g, ok := conn.Client.Transport.(*haGuard); ok {
		g.stop()
	}
}
