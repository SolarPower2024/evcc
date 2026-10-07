// Custom extension: a heater in stages without a power sensor only assumes its
// power, which counts in the home consumption (core/loadpoint_stages.go). The
// energy flow leaves its loadpoint out instead of listing it with 0 kW.
export function measuredLoadpoints<T extends object>(loadpoints: T[]): T[] {
  return loadpoints.filter(
    (lp) => !(lp as { chargePowerEstimated?: boolean }).chargePowerEstimated
  );
}
