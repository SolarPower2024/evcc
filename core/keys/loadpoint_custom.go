package keys

// Custom extension: loadpoint keys of the fork, see core/loadpoint_phasecurrents.go.
// The 1p currents are named as in evcc PR 32505, so an upstream version reads the same settings.
const (
	MinCurrent1p      = "minCurrent1p"      // min current, 1p override (0 = use minCurrent)
	MaxCurrent1p      = "maxCurrent1p"      // max current, 1p override (0 = use maxCurrent)
	PhaseScale3pDelay = "phaseScale3pDelay" // delay before scaling up to 3p (0 = enable delay)
	PhaseScale1pDelay = "phaseScale1pDelay" // delay before scaling down to 1p (0 = disable delay)
)
