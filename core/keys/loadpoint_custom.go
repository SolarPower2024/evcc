package keys

// Custom extension: loadpoint keys of the fork, see core/loadpoint_phasecurrents.go.
// Named as in evcc PR 32505, so an upstream version reads the same settings.
const (
	MinCurrent1p = "minCurrent1p" // min current, 1p override (0 = use minCurrent)
	MaxCurrent1p = "maxCurrent1p" // max current, 1p override (0 = use maxCurrent)
)
