// Custom extension: pre-check of a battery profile before it is sent, the same rules as
// Validate() in core/lm/profile/profile.go (the server still checks). Values as in the
// profile form: only the ticked ones, the peak limit in kW.

export type ProfileProblem = {
  code: "range" | "step" | "gridChargeOrder" | "batteryOrder";
  /** form key of the offending value, empty for the order rules */
  key: string;
  min?: number;
  max?: number;
  /** step in kW for code "step" */
  step?: number;
};

// percent values: key -> [min, max]
const PERCENT: Record<string, [number, number]> = {
  gridChargeStart: [0, 100],
  gridChargeStop: [0, 100],
  prioritySoc: [0, 100],
  bufferSoc: [0, 100],
  bufferStartSoc: [0, 100],
  peakReserve: [1, 99],
};

const SOLAR_SHARE = "solarShare:";
const PEAK_LIMIT_KW: [number, number] = [2, 20];
const PEAK_LIMIT_STEP_KW = 0.5;

export function profileProblem(values: Record<string, unknown>): ProfileProblem | null {
  const num = (key: string): number | undefined => {
    const v = values[key];
    return typeof v === "number" ? v : undefined;
  };

  for (const [key, [min, max]] of Object.entries(PERCENT)) {
    const v = num(key);
    if (v !== undefined && (v < min || v > max)) return { code: "range", key, min, max };
  }

  const limit = num("peakLimit");
  if (limit !== undefined) {
    const [min, max] = PEAK_LIMIT_KW;
    if (limit < min || limit > max) return { code: "range", key: "peakLimit", min, max };
    if (Math.round(limit * 1000) % (PEAK_LIMIT_STEP_KW * 1000) !== 0) {
      return { code: "step", key: "peakLimit", step: PEAK_LIMIT_STEP_KW };
    }
  }

  for (const key of Object.keys(values)) {
    if (!key.startsWith(SOLAR_SHARE)) continue;
    const v = num(key);
    if (v !== undefined && (v < 0 || v > 100)) return { code: "range", key, min: 0, max: 100 };
  }

  const start = num("gridChargeStart");
  const stop = num("gridChargeStop");
  if (start !== undefined && stop !== undefined && start >= stop) {
    return { code: "gridChargeOrder", key: "" };
  }

  // priority <= buffer <= buffer start, 0 turns buffer and buffer start off
  const priority = num("prioritySoc") ?? 0;
  const buffer = num("bufferSoc") ?? 0;
  const bufferStart = num("bufferStartSoc") ?? 0;
  if ((buffer !== 0 && priority > buffer) || (bufferStart !== 0 && buffer > bufferStart)) {
    return { code: "batteryOrder", key: "" };
  }

  return null;
}
