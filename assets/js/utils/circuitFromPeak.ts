interface CircuitFromPeakState {
  lmOff?: { circuit?: string; fromPeak?: boolean };
  lmAdvanced?: { circuitFromPeak?: boolean };
  peakShavingLimit?: number;
  peakShaving?: boolean;
  circuits?: Record<string, { title?: string }>;
}

// The limit shown in the dialog of the load management (peak) circuit while its
// power limit is replaced by the peak limit (Erweitert → Grenze =
// Peak-Shaving-Grenze, see core/site_lm_switch.go), in W. Undefined for every
// other circuit and while the switch is off, so the dialog stays as evcc has it.
export function circuitPeakHintLimit(
  state: CircuitFromPeakState | undefined,
  circuit: string | undefined
): number | undefined {
  if (!circuit || circuit !== state?.lmOff?.circuit) return undefined;
  if (!state.lmAdvanced?.circuitFromPeak) return undefined;
  return state.peakShavingLimit || 0;
}
