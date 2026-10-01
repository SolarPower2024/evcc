package loadpoint

// Custom extension: separate current limits for 1p charging, see
// core/loadpoint_phasecurrents.go. The json fields are named as in evcc PR 32505.

// Currents1pConfig holds the 1p current limits of the loadpoint config
type Currents1pConfig struct {
	MinCurrent1p float64 `json:"minCurrent1p"` // 0 = use minCurrent
	MaxCurrent1p float64 `json:"maxCurrent1p"` // 0 = use maxCurrent
}

// currents1p is implemented by loadpoints with 1p current limits
type currents1p interface {
	GetCurrents1p() (float64, float64)
	SetCurrents1p(float64, float64) error
}

// Currents1pConfigOf returns the 1p current limits of lp
func Currents1pConfigOf(lp API) Currents1pConfig {
	var res Currents1pConfig
	if c, ok := lp.(currents1p); ok {
		res.MinCurrent1p, res.MaxCurrent1p = c.GetCurrents1p()
	}
	return res
}

// applyCurrents1p sets the 1p current limits, after the regular ones they are checked against
func (payload DynamicConfig) applyCurrents1p(lp API) error {
	if c, ok := lp.(currents1p); ok {
		return c.SetCurrents1p(payload.MinCurrent1p, payload.MaxCurrent1p)
	}
	return nil
}
