interface PeakShavingEntities {
  peakShavingBatteryType?: string;
  peakShavingProtSwitch?: string;
  peakShavingProtLimit?: string;
  peakShavingProtSoc?: string;
}

// Whether peak shaving has what it needs to run: the three entities of the
// controller in Home Assistant (Omnibattery for a Marstek, evcc Peak Fronius for a
// BYD), see core/site_peak_ext.go.
export function peakShavingSetUp(state?: PeakShavingEntities): boolean {
  return !!(state?.peakShavingProtSwitch && state.peakShavingProtLimit && state.peakShavingProtSoc);
}
