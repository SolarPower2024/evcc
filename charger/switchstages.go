package charger

// Custom extension: a heater switched in stages, e.g. a three-phase heating rod
// with one switch per phase. A single loadpoint drives all stages: evcc sets a
// current, the charger switches on as many whole stages as fit into it. Load
// management and pv surplus can so step the heater down instead of switching it
// off, and the whole heater is updated in one loadpoint cycle.
//
// Switching down is immediate. Switching up while running waits until the last
// change is stagedelay old, so a surplus hovering around a stage boundary does
// not toggle the switches every cycle. Switching on from off is not delayed,
// the loadpoint's own enable delay already applies there.
//
// In pv mode core steps the heater down only after the loadpoint's disable delay,
// see core/loadpoint_stages.go; a limit of the circuit or of load management
// applies at once.
//
// A heater with its own thermostat draws nothing while the switches stay on.
// With a power sensor, a draw up to standbypower then reports ready instead of
// heating, as upstream's switch socket does.
//
// A switch that cannot be read leaves its stage unknown: the others are still
// read and switched, nothing is written to the unknown one, and its power
// counts as on.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/api/implement"
	"github.com/evcc-io/evcc/charger/measurement"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/plugin"
	"github.com/evcc-io/evcc/util"
)

func init() {
	registry.AddCtx("switchstages", NewSwitchStagesFromConfig)
}

// switchStage is one switched element
type switchStage struct {
	enabled func() (bool, error)
	enable  func(bool) error
}

// SwitchStages charger implementation
type SwitchStages struct {
	implement.Caps
	*embed
	log   *util.Logger
	clock clock.Clock

	stages     []switchStage
	down       []atomic.Bool // per stage: its switch could not be read, logged once
	stagePower float64
	delay      time.Duration
	power      func() (float64, error) // optional measurement
	standby    float64                 // measured power up to this counts as idle
	lp         loadpoint.API

	mu      sync.Mutex
	enabled bool      // as last set by evcc or found on
	current float64   // last requested current, 0 = unknown
	level   int       // stages switched on by the last write
	changed time.Time // last change of the level or failed attempt
	lastErr string    // cause of the last failed catch-up, logged once

	// a step down failed and is left to evcc's retry: the request is not taken
	// over, so the catch-up must not step up to the old one. Until evcc writes
	// again successfully.
	lowering bool
}

// NewSwitchStagesFromConfig creates a stepped switch charger from generic config
func NewSwitchStagesFromConfig(ctx context.Context, other map[string]any) (api.Charger, error) {
	var cc struct {
		embed                   `mapstructure:",squash"`
		Stages                  []struct{ Enabled, Enable plugin.Config }
		StagePower              float64
		StageDelay              time.Duration
		Power                   *plugin.Config
		StandbyPower            float64
		Energy                  *plugin.Config
		measurement.Temperature `mapstructure:",squash"` // optional, for heating devices
	}

	if err := util.DecodeOther(other, &cc); err != nil {
		return nil, err
	}

	if len(cc.Stages) == 0 {
		return nil, errors.New("missing stages")
	}

	if cc.StagePower <= 0 {
		return nil, errors.New("missing stage power")
	}

	stages := make([]switchStage, 0, len(cc.Stages))
	for i, s := range cc.Stages {
		enabled, err := s.Enabled.BoolGetter(ctx)
		if err != nil {
			return nil, fmt.Errorf("stage %d enabled: %w", i+1, err)
		}

		enable, err := s.Enable.BoolSetter(ctx, "enable")
		if err != nil {
			return nil, fmt.Errorf("stage %d enable: %w", i+1, err)
		}

		stages = append(stages, switchStage{enabled: enabled, enable: enable})
	}

	power, err := cc.Power.FloatGetter(ctx)
	if err != nil {
		return nil, fmt.Errorf("power: %w", err)
	}

	c := NewSwitchStages(&cc.embed, stages, cc.StagePower, cc.StageDelay, power)
	c.standby = max(cc.StandbyPower, 0)

	energy, err := cc.Energy.FloatGetter(ctx)
	if err != nil {
		return nil, fmt.Errorf("energy: %w", err)
	}
	implement.May(c, implement.MeterEnergy(energy))

	// for heating devices, the soc slot holds the temperature in °C
	temp, limitTemp, err := cc.Temperature.Configure(ctx)
	if err != nil {
		return nil, err
	}
	implement.May(c, implement.Battery(temp))
	implement.May(c, implement.SocLimiter(limitTemp))

	if cc.StageDelay > 0 {
		go c.run(ctx, min(cc.StageDelay/4, 5*time.Second))
	}

	return c, nil
}

// NewSwitchStages creates a stepped switch charger
func NewSwitchStages(embed *embed, stages []switchStage, stagePower float64, delay time.Duration, power func() (float64, error)) *SwitchStages {
	return &SwitchStages{
		Caps:       implement.New(),
		embed:      embed,
		log:        util.NewLogger("switchstages"),
		clock:      clock.New(),
		stages:     stages,
		down:       make([]atomic.Bool, len(stages)),
		stagePower: stagePower,
		delay:      delay,
		power:      power,
	}
}

// run catches up on a delayed step up once its delay has passed
func (c *SwitchStages) run(ctx context.Context, interval time.Duration) {
	for tick := time.Tick(interval); ; {
		select {
		case <-tick:
		case <-ctx.Done():
			return
		}

		c.logCatchUp(c.catchUp())
	}
}

// logCatchUp logs a failed catch-up once per cause, a repeat only on debug level
func (c *SwitchStages) logCatchUp(err error) {
	if err == nil {
		return
	}

	c.mu.Lock()
	repeat := err.Error() == c.lastErr
	c.lastErr = err.Error()
	c.mu.Unlock()

	if repeat {
		c.log.DEBUG.Println(err)
		return
	}
	c.log.ERROR.Println(err)
}

// catchUp applies a step up that was held back by the delay. Only a running
// heater has one: from no stage apply never holds back, so a heater that is
// off, or whose switch-on failed, is left to evcc. So is one whose step down
// failed: the old current is still the request, but evcc asked for less and
// retries, stepping up meanwhile would add load against its limit.
func (c *SwitchStages) catchUp() error {
	phases := c.phases()

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.enabled || c.lowering || c.level == 0 || c.level >= c.target(phases) || c.clock.Since(c.changed) < c.delay {
		return nil
	}

	err := c.apply(phases)
	if err == nil {
		c.lastErr = ""
	}
	return err
}

// stageState is what the heater knows of one stage's switch
type stageState int

const (
	stageOff stageState = iota
	stageOn
	stageUnknown // not readable, e.g. unavailable or deleted
)

// readStates reads all switches, a stage that cannot be read is unknown and
// carries its error
func (c *SwitchStages) readStates() ([]stageState, []error) {
	res := make([]stageState, len(c.stages))
	errs := make([]error, len(c.stages))

	for i, s := range c.stages {
		on, err := s.enabled()
		switch {
		case err != nil:
			res[i], errs[i] = stageUnknown, fmt.Errorf("stage %d: %w", i+1, err)
		case on:
			res[i] = stageOn
		}
	}

	return res, errs
}

// states reads all switches. An unreadable stage stays unknown, so one lost
// switch does not keep evcc from switching off the others; it is logged once
// per outage. Only if no stage can be read there is nothing to go on.
func (c *SwitchStages) states() ([]stageState, error) {
	res, errs := c.readStates()

	var readable int
	for i, err := range errs {
		switch {
		case err != nil:
			if c.down[i].CompareAndSwap(false, true) {
				c.log.WARN.Printf("%v, switching the others", err)
			}
		default:
			readable++
			if c.down[i].CompareAndSwap(true, false) {
				c.log.INFO.Printf("stage %d: readable again", i+1)
			}
		}
	}

	if readable == 0 {
		return nil, errs[0]
	}

	return res, nil
}

// countOn returns the stages known to be on
func countOn(states []stageState) int {
	var n int
	for _, s := range states {
		if s == stageOn {
			n++
		}
	}
	return n
}

// countRunning returns the stages that are on or may be: an unreadable stage is
// counted on, the safe side for circuits
func countRunning(states []stageState) int {
	var n int
	for _, s := range states {
		if s != stageOff {
			n++
		}
	}
	return n
}

// phases returns the phases the loadpoint converts power and current with.
// Not called with the mutex held, the loadpoint takes its own lock.
func (c *SwitchStages) phases() int {
	if c.lp != nil {
		if p := c.lp.ActivePhases(); p > 0 {
			return p
		}
	}
	return len(c.stages)
}

// target returns the stages that fit into the requested current. evcc only
// enables at or above the minimum, which is one stage, so an enabled heater
// runs at least one. The tolerance only covers floating point error from
// converting power to current and back, a partial stage is never rounded up.
func (c *SwitchStages) target(phases int) int {
	n := int(math.Floor(voltage*c.current*float64(phases)/c.stagePower + 1e-6))
	return min(max(n, 1), len(c.stages))
}

// apply switches the stages to the target, holding back a step up while the
// delay runs. A failure restarts the delay, so the catch-up tries again only
// after it, and leaves the level at the stages switched on so far. Called with
// the mutex held.
func (c *SwitchStages) apply(phases int) error {
	err := c.switchTo(phases)
	if err != nil {
		c.changed = c.clock.Now()
	}
	return err
}

// switchTo switches the stages to the target, see apply
func (c *SwitchStages) switchTo(phases int) error {
	states, err := c.states()
	if err != nil {
		return err
	}

	on := countOn(states)

	var level int
	if c.enabled {
		level = c.target(phases)

		// step up from a running heater only once the last change is old enough
		if level > on && on > 0 && c.clock.Since(c.changed) < c.delay {
			level = on
		}
	}

	// lowest stages first on, highest first off, so a partial failure never
	// leaves more switched on than wanted. An unknown stage is left alone,
	// nothing is written blind to a switch that cannot be read.
	for i := len(states) - 1; i >= level; i-- {
		if states[i] == stageOn {
			if err := c.stages[i].enable(false); err != nil {
				c.level = countOn(states)
				return fmt.Errorf("stage %d off: %w", i+1, err)
			}
			states[i] = stageOff
		}
	}

	for i := range level {
		if states[i] == stageOff {
			if err := c.stages[i].enable(true); err != nil {
				c.level = countOn(states)
				return fmt.Errorf("stage %d on: %w", i+1, err)
			}
			states[i] = stageOn
		}
	}

	// an unknown stage in the way keeps the target out of reach, the next
	// attempt waits for the delay
	reached := countOn(states)
	if reached != on || reached != c.level || reached < level {
		c.log.DEBUG.Printf("stages: %d of %d", reached, len(c.stages))
		c.changed = c.clock.Now()
	}
	c.level = reached

	return nil
}

// StageStates returns each stage's switch, lowest first, for the config page.
// A stage that cannot be read is an error there.
func (c *SwitchStages) StageStates() ([]bool, error) {
	states, errs := c.readStates()

	res := make([]bool, len(states))
	for i, s := range states {
		if errs[i] != nil {
			return nil, errs[i]
		}
		res[i] = s == stageOn
	}

	return res, nil
}

// PowerEstimated reports that without a power sensor the power is the stages
// switched on times the stage power, an assumption: the heater's own thermostat
// may have cut out. Core then keeps it out of the home consumption.
func (c *SwitchStages) PowerEstimated() bool {
	return c.power == nil
}

// Stages returns the number of stages, core shows the heater as switched by it
func (c *SwitchStages) Stages() int {
	return len(c.stages)
}

// StagePhaseCurrent returns the current one stage draws on its phase, if the
// heater has one stage per phase. The loadpoint's phases must match the wiring.
func (c *SwitchStages) StagePhaseCurrent() (float64, bool) {
	if len(c.stages) != c.phases() {
		return 0, false
	}

	return c.stagePower / voltage, true
}

// RunningCurrent returns the current the stages switched on by the last write
// stand for on the given phases, without reading the switches. Core holds the
// heater on them while the pv surplus falls short, see core/loadpoint_stages.go.
func (c *SwitchStages) RunningCurrent(phases int) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return float64(c.level) * c.stagePower / (voltage * float64(phases))
}

// Status implements the api.Charger interface
func (c *SwitchStages) Status() (api.ChargeStatus, error) {
	if c.lp != nil && c.lp.GetMode() == api.ModeOff {
		return api.StatusA, nil
	}

	states, err := c.states()
	if err != nil {
		return api.StatusNone, err
	}

	if countOn(states) == 0 {
		return api.StatusB, nil
	}

	// switched on, but the thermostat may have cut the heater
	if c.power != nil {
		p, err := c.CurrentPower()
		if err != nil {
			return api.StatusNone, err
		}
		if p == 0 {
			return api.StatusB, nil
		}
	}

	return api.StatusC, nil
}

// Enabled implements the api.Charger interface
func (c *SwitchStages) Enabled() (bool, error) {
	states, err := c.states()
	if err != nil {
		return false, err
	}

	return countOn(states) > 0, nil
}

// Enable implements the api.Charger interface
func (c *SwitchStages) Enable(enable bool) error {
	phases := c.phases()

	c.mu.Lock()
	defer c.mu.Unlock()

	// a failed switch leaves the setting as it was, else the catch-up would
	// carry it out later without evcc asking. The stages still on show as
	// enabled, so evcc retries a failed switch off; meanwhile no step up.
	prev := c.enabled
	c.enabled = enable
	if err := c.apply(phases); err != nil {
		c.enabled = prev
		c.lowering = c.lowering || !enable
		return err
	}

	c.lowering = false

	return nil
}

// MaxCurrent implements the api.Charger interface
func (c *SwitchStages) MaxCurrent(current int64) error {
	return c.MaxCurrentMillis(float64(current))
}

var _ api.ChargerEx = (*SwitchStages)(nil)

// MaxCurrentMillis implements the api.ChargerEx interface. While off it only
// stores the current, Enable switches on. A heater found running, e.g. after a
// restart, is taken as enabled and, before evcc sets a current, as running at
// the current of its stages: evcc's first request is the minimum, which would
// drop it to one stage, and a smaller request later would never be written.
func (c *SwitchStages) MaxCurrentMillis(current float64) error {
	if current < 0 || math.IsNaN(current) || math.IsInf(current, 0) {
		return fmt.Errorf("invalid current: %g", current)
	}

	phases := c.phases()

	c.mu.Lock()
	defer c.mu.Unlock()

	prev := c.current
	if prev == 0 && !c.enabled {
		if adopted, err := c.adopt(phases); adopted || err != nil {
			return err
		}
	}

	// a failed switch does not take the request over: evcc reads the current
	// back and, still seeing the old one, sets the new one again
	c.current = current

	if err := c.update(phases); err != nil {
		c.current = prev
		c.lowering = c.lowering || current < prev
		return err
	}

	c.lowering = false

	return nil
}

// adopt takes over the stages found running with the current they stand for,
// without switching. Called with the mutex held.
func (c *SwitchStages) adopt(phases int) (bool, error) {
	states, err := c.states()
	if err != nil {
		return false, err
	}

	n := countOn(states)
	if n == 0 {
		return false, nil
	}

	c.enabled, c.level = true, n
	c.current = float64(n) * c.stagePower / (voltage * float64(phases))

	return true, nil
}

// update switches the stages to the current. Called with the mutex held.
func (c *SwitchStages) update(phases int) error {
	if !c.enabled {
		states, err := c.states()
		if err != nil {
			return err
		}
		if c.enabled = countOn(states) > 0; !c.enabled {
			return nil
		}
	}

	return c.apply(phases)
}

var _ api.CurrentGetter = (*SwitchStages)(nil)

// GetMaxCurrent returns the requested current rather than the current of the switched stages
func (c *SwitchStages) GetMaxCurrent() (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.current == 0 {
		return 0, api.ErrNotAvailable
	}

	return c.current, nil
}

var _ api.PowerLimiter = (*SwitchStages)(nil)

// GetMinMaxPower implements the api.PowerLimiter interface: one stage up to all
func (c *SwitchStages) GetMinMaxPower() (float64, float64, error) {
	return c.stagePower, c.stagePower * float64(len(c.stages)), nil
}

var _ api.Meter = (*SwitchStages)(nil)

// CurrentPower implements the api.Meter interface: the measurement if configured,
// standby ignored, else the stages switched on
func (c *SwitchStages) CurrentPower() (float64, error) {
	if c.power != nil {
		p, err := c.power()
		if p <= c.standby {
			p = 0
		}
		return p, err
	}

	states, err := c.states()
	if err != nil {
		return 0, err
	}

	return float64(countRunning(states)) * c.stagePower, nil
}

var _ api.PhaseCurrents = (*SwitchStages)(nil)

// Currents implements the api.PhaseCurrents interface. A stage draws its whole
// current on one phase, not a third of it on each. With one stage per phase the
// heater so carries the current of one stage as soon as one is on, and evcc
// takes the maximum over the phases for its circuits. The current is reported on
// every phase, not only on the phases of the stages that are on: evcc would take
// the phases with a current for the active phases and then convert the power of
// the stages with a single phase. Not available otherwise.
func (c *SwitchStages) Currents() (float64, float64, float64, error) {
	current, ok := c.StagePhaseCurrent()
	if !ok {
		return 0, 0, 0, api.ErrNotAvailable
	}

	states, err := c.states()
	if err != nil {
		return 0, 0, 0, err
	}

	if countRunning(states) == 0 {
		return 0, 0, 0, nil
	}

	if len(c.stages) == 1 {
		return current, 0, 0, nil
	}

	return current, current, current, nil
}

var _ loadpoint.Controller = (*SwitchStages)(nil)

// LoadpointControl implements loadpoint.Controller
func (c *SwitchStages) LoadpointControl(lp loadpoint.API) {
	c.lp = lp
}
