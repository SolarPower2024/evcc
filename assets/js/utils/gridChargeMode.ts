// What battery grid charging goes by, see core/site_lm_grid_mode.go: evcc's price
// limit (dynamic tariff), the soc thresholds of the fork, or both (default).
export type GridChargeMode = "both" | "soc" | "price";

const MODES: GridChargeMode[] = ["both", "soc", "price"];

// the mode as published, anything unknown or missing reads as both
export function gridChargeMode(raw?: string | null): GridChargeMode {
  return MODES.find((m) => m === raw) ?? "both";
}

// The modes the select offers. Price needs a dynamic tariff; a price already
// stored stays in the list, so the select can show it.
export function gridChargeModeOptions(
  smartCostAvailable: boolean,
  current: GridChargeMode
): { value: GridChargeMode }[] {
  return MODES.filter((m) => m !== "price" || smartCostAvailable || current === "price").map(
    (value) => ({ value })
  );
}

// evcc's price card (visible with a dynamic tariff or a limit set) is hidden for soc
// only, unless a limit is still set: it stays visible so it can be removed
export function priceCardVisible(
  evccVisible: boolean,
  raw?: string | null,
  limitSet = false
): boolean {
  return evccVisible && (gridChargeMode(raw) !== "soc" || limitSet);
}

// the card of the soc thresholds is hidden for price only, unless the switch is on
// (set through the api): it stays visible so it can be turned off
export function socCardVisible(raw?: string | null, socEnabled = false): boolean {
  return gridChargeMode(raw) !== "price" || socEnabled;
}

// price only: one-time charging stays reachable as a card of its own, otherwise
// it is part of the soc card
export function onceCardVisible(raw?: string | null): boolean {
  return gridChargeMode(raw) === "price";
}
