// Custom extension: a heater in stages without a power sensor only assumes its
// power, which counts in the home consumption (core/loadpoint_stages.go). The
// energy flow leaves its loadpoint out instead of listing it with 0 kW.
export function measuredLoadpoints<T extends object>(loadpoints: T[]): T[] {
  return loadpoints.filter(
    (lp) => !(lp as { chargePowerEstimated?: boolean }).chargePowerEstimated
  );
}

// the label of the home battery's charge line: held, charged from the grid (the
// existing "Netzladen: aktiv" subline only shows with a price limit set, so soc-based
// grid charging would go unmentioned) or the regular one
export function batteryChargeLabelKey(hold: boolean, gridActive: boolean): string {
  if (hold) return "main.energyflow.batteryChargeHold";
  if (gridActive) return "main.energyflow.batteryChargeGrid";
  return "main.energyflow.batteryCharge";
}

// an assumed power in whole kW with a narrow space, so it fits a phone; null when the
// power is measured and the regular format applies
export function fmtPowerEstimate(
  value: number,
  chargePower: number,
  chargePowerEstimate: number,
  fmtKW: (value: number) => string
): string | null {
  if (!chargePower && chargePowerEstimate) return `≈ ${fmtKW(value)}`;
  return null;
}
