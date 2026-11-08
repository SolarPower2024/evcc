import type { Product } from "@/components/Config/DeviceModal";

// Custom extension: grid import offers the split fixed price (fixed-netz) next to
// "Fixed Price" and starts with it, see TariffModal.vue. "Fixed Price" stays in the
// list, evcc's e2e tests pick it.
export const SPLIT_TEMPLATE = "fixed-netz";

// grid fee left after a percent zone, in the same unit as fee
export function gridFeeAfterPct(fee: number, pct: number): number {
  return fee * (1 - pct / 100);
}

export function defaultPriceTemplate(tariffType: string | null | undefined): string | undefined {
  if (tariffType === "grid") return SPLIT_TEMPLATE;
  return tariffType === "feedIn" ? "fixed" : undefined;
}

export function priceTemplatesFor<T extends Pick<Product, "template">>(
  generic: T[],
  tariffType: string | null | undefined
): T[] {
  if (tariffType === "grid") return generic;
  return generic.filter((p) => p.template !== SPLIT_TEMPLATE);
}
