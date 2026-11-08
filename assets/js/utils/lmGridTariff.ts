import type { Product } from "@/components/Config/DeviceModal";

// Custom extension: the split fixed price (fixed-netz) replaces "Fixed Price" for a new
// grid tariff, see TariffModal.vue. Existing tariffs keep the template they were saved
// with, so while editing both stay in the list.
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
  tariffType: string | null | undefined,
  isNew: boolean
): T[] {
  if (tariffType !== "grid") return generic.filter((p) => p.template !== SPLIT_TEMPLATE);
  return isNew ? generic.filter((p) => p.template !== "fixed") : generic;
}
