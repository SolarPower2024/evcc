// lowest soc of a Marstek battery, it does not discharge further
export const MARSTEK_MIN_SOC = 11;

// lowest soc threshold of Omnibattery's peak shaving, the lowest reserve for a Marstek
export const MARSTEK_MIN_RESERVE = 20;

// The soc values offered in the battery selects, from max down to min in 5 %
// steps. For a Marstek nothing is offered below its lowest value (marstekMin):
// the minimum soc of 11 % for grid charging, where the range from 15 % down to
// 11 % is offered in 1 % steps, or 20 % for the peak reserve. A value already
// set is always kept, so the select can show it.
export function socSteps(
  max: number,
  min: number,
  marstek: boolean,
  keep?: number,
  marstekMin: number = MARSTEK_MIN_SOC
): number[] {
  const res = new Set<number>();
  for (let i = max; i >= min; i -= 5) {
    res.add(i);
  }

  if (marstek) {
    for (let i = 15; i >= marstekMin; i--) {
      if (i <= max && i >= min) res.add(i);
    }
    for (const v of res) {
      if (v < marstekMin) res.delete(v);
    }
  }

  if (keep !== undefined && keep >= 0 && keep <= 100) {
    res.add(keep);
  }

  return [...res].sort((a, b) => b - a);
}
