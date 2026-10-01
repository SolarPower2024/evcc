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
// A heater with its own thermostat draws nothing while the switches stay on.
// With a power sensor, a draw up to standbypower then reports ready instead of
// heating, as upstream's switch socket does.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
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
	stagePower float64
	delay      time.Duration
	power      func() (float64, error) // optional measurement
	standby    float64                 // measured power up to this counts as idle
	lp         loadpoint.API

	mu      sync.Mutex
	enabled bool      // as last set by evcc or found on
	current float64   // last requested current, 0 = unknown
	level   int       // stages switched on by the last write
	changed time.Time // last change of the level
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

		if err := c.catchUp(); err != nil {
			c.log.ERROR.Println(err)
		}
	}
}

// catchUp applies a step up that was held back by the delay
func (c *SwitchStages) catchUp() error {
	phases := c.phases()

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.enabled || c.level >= c.target(phases) || c.clock.Since(c.changed) < c.delay {
		return nil
	}

	return c.apply(phases)
}

// states reads all switches
func (c *SwitchStages) states() ([]bool, error) {
	res := make([]bool, len(c.stages))
	for i, s := range c.stages {
		on, err := s.enabled()
		if err != nil {
			return nil, fmt.Errorf("stage %d: %w", i+1, err)
		}
		res[i] = on
	}
	return res, nil
}

func countOn(states []bool) int {
	var n int
	for _, on := range states {
		if on {
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
// delay runs. Called with the mutex held.
func (c *SwitchStages) apply(phases int) error {
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
	// leaves more switched on than wanted
	for i := len(states) - 1; i >= level; i-- {
		if states[i] {
			if err := c.stages[i].enable(false); err != nil {
				return fmt.Errorf("stage %d off: %w", i+1, err)
			}
		}
	}

	for i := range level {
		if !states[i] {
			if err := c.stages[i].enable(true); err != nil {
				return fmt.Errorf("stage %d on: %w", i+1, err)
			}
		}
	}

	if level != on || level != c.level {
		c.log.DEBUG.Printf("stages: %d of %d", level, len(c.stages))
		c.changed = c.clock.Now()
	}
	c.level = level

	return nil
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
	return countOn(states) > 0, err
}

// Enable implements the api.Charger interface
func (c *SwitchStages) Enable(enable bool) error {
	phases := c.phases()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.enabled = enable
	return c.apply(phases)
}

// MaxCurrent implements the api.Charger interface
func (c *SwitchStages) MaxCurrent(current int64) error {
	return c.MaxCurrentMillis(float64(current))
}

var _ api.ChargerEx = (*SwitchStages)(nil)

// MaxCurrentMillis implements the api.ChargerEx interface. While off it only
// stores the current, Enable switches on. A heater found running, e.g. after a
// restart, is taken as enabled.
func (c *SwitchStages) MaxCurrentMillis(current float64) error {
	if current < 0 || math.IsNaN(current) || math.IsInf(current, 0) {
		return fmt.Errorf("invalid current: %g", current)
	}

	phases := c.phases()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.current = current

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
	return float64(countOn(states)) * c.stagePower, err
}

var _ loadpoint.Controller = (*SwitchStages)(nil)

// LoadpointControl implements loadpoint.Controller
func (c *SwitchStages) LoadpointControl(lp loadpoint.API) {
	c.lp = lp
}
