package metrics

// Custom extension: the collector of the monthly peak, see
// core/site_peak_circuit.go. It records the energy counter of the main
// circuit's meter per 15 minute slot, in a group of its own: the groups of the
// balances (grid, pv, battery, home, loadpoint, consumer) and of the monitoring
// meters do not include it, so it is in no balance and no flow.

// GridPeak is the group of the peak collector, GridPeakName its name and title
const (
	GridPeak     = "gridpeak"
	GridPeakName = "grid-peak"
)
