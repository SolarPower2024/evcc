package loadpoint

// Custom extension: phase switching settings, see core/loadpoint_phasecurrents.go.
// The 1p currents' json fields are named as in evcc PR 32505.

import (
	"maps"
	"time"
)

// PhaseSwitchConfig holds the phase switching settings of the loadpoint config
type PhaseSwitchConfig struct {
	MinCurrent1p      float64 `json:"minCurrent1p"`      // 0 = use minCurrent
	MaxCurrent1p      float64 `json:"maxCurrent1p"`      // 0 = use maxCurrent
	PhaseScale3pDelay float64 `json:"phaseScale3pDelay"` // ns, 0 = enable delay
	PhaseScale1pDelay float64 `json:"phaseScale1pDelay"` // ns, 0 = disable delay
}

// the delays are plain numbers rather than time.Duration: the ui sends a
// cleared field as "", which reads as 0 for a number but fails as a duration

// phaseSwitch is implemented by loadpoints with phase switching settings
type phaseSwitch interface {
	GetCurrents1p() (float64, float64)
	SetCurrents1p(float64, float64) error
	GetPhaseDelays() (time.Duration, time.Duration)
	SetPhaseDelays(time.Duration, time.Duration) error
}

// PhaseSwitchConfigOf returns the phase switching settings of lp
func PhaseSwitchConfigOf(lp API) PhaseSwitchConfig {
	var res PhaseSwitchConfig
	if c, ok := lp.(phaseSwitch); ok {
		res.MinCurrent1p, res.MaxCurrent1p = c.GetCurrents1p()
		up, down := c.GetPhaseDelays()
		res.PhaseScale3pDelay, res.PhaseScale1pDelay = float64(up), float64(down)
	}
	return res
}

// applyPhaseSwitch sets the phase switching settings, after the regular currents
// the 1p ones are checked against
func (payload DynamicConfig) applyPhaseSwitch(lp API) error {
	c, ok := lp.(phaseSwitch)
	if !ok {
		return nil
	}
	if err := c.SetCurrents1p(payload.MinCurrent1p, payload.MaxCurrent1p); err != nil {
		return err
	}
	return c.SetPhaseDelays(time.Duration(payload.PhaseScale3pDelay), time.Duration(payload.PhaseScale1pDelay))
}

// droppedKeys lists config keys the fork once stored in a loadpoint's config and
// no longer reads. The strict decode in SplitConfig would reject them, so they are
// ignored there and removed from the database at start (core/site_lm_dropped.go).
// A key that goes away is added here and stays.
var droppedKeys = []string{"lmpriority"}

// DroppedKeys returns the config keys that are ignored and removed from storage
func DroppedKeys() []string {
	return droppedKeys
}

// withoutDroppedKeys returns payload without the dropped keys. It is a copy once
// one of them occurs, the input stays as it is: the cleanup of the stored config
// has to see the key.
func withoutDroppedKeys(payload map[string]any) map[string]any {
	found := false
	for _, k := range droppedKeys {
		if _, ok := payload[k]; ok {
			found = true
			break
		}
	}
	if !found {
		return payload
	}

	res := maps.Clone(payload)
	for _, k := range droppedKeys {
		delete(res, k)
	}
	return res
}
