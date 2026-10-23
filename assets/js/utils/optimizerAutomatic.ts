import { OPTIMIZER_AUTOMATIC } from "@/types/evcc";
import type { GridChargeMode } from "./gridChargeMode";

// Whether the optimizer's automatic mode is on (level battery or full), see
// core/site_optimizer_gate.go. The fork's notes next to its settings show only
// then.
export function optimizerAutomaticOn(level?: string | null): boolean {
  return !!level && level !== OPTIMIZER_AUTOMATIC.OFF;
}

// evcc ignores the price limit in automatic mode, the optimizer decides by
// price: the note belongs to the modes that go by price
export function priceLimitIgnored(level: string | null | undefined, mode: GridChargeMode): boolean {
  return optimizerAutomaticOn(level) && (mode === "price" || mode === "both");
}
