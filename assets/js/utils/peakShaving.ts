interface PeakShavingEntities {
  peakShavingBatteryType?: string;
  peakShavingEntity?: string;
  peakShavingProtSwitch?: string;
  peakShavingProtLimit?: string;
  peakShavingProtSoc?: string;
}

// Whether peak shaving has what it needs to run: the entity receiving the
// discharge setpoint, for a Marstek the three entities of Omnibattery's peak
// shaving, see core/site_peak_omni.go.
export function peakShavingSetUp(state?: PeakShavingEntities): boolean {
  if (state?.peakShavingBatteryType === "marstek") {
    return !!(
      state.peakShavingProtSwitch &&
      state.peakShavingProtLimit &&
      state.peakShavingProtSoc
    );
  }
  return !!state?.peakShavingEntity;
}
