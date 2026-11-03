// Custom extension: export sold under two feed-in tariffs, see
// core/site_feedin_eeg.go. The energy page splits the grid revenue tiles
// into the standard feed-in tariff and the EEG part metered by a Home Assistant
// counter. Chart and legend stay as upstream. Without a counter nothing
// changes.
import api from "@/api";
import type { HistorySeries } from "./GroupChart.vue";
import type { FlowCost, StatItem } from "./types";

export interface FeedInSplit {
  start: string;
  end: string;
  export: number; // kWh, grid meter
  eeg: number; // kWh, EEG counter
  standard: number; // kWh, export minus EEG
  eegRevenue: number;
  standardRevenue: number;
  eegPriced: number; // kWh with a known EEG price
  standardPriced: number;
}

export type FeedInSplitTotals = Omit<FeedInSplit, "start" | "end">;

// title of the EEG counter's own series, see metrics.FeedInEeg
const FEED_IN_EEG = "feedin-eeg";

// the EEG counter is shown in the grid card, not again among the meters
export function withoutFeedInEeg(meters: HistorySeries[]): HistorySeries[] {
  return meters.filter((s) => s.title !== FEED_IN_EEG);
}

export async function fetchFeedInSplit(
  from: Date,
  to: Date,
  aggregate: string
): Promise<FeedInSplit[]> {
  const { data } = await api.get("feedinsplit", {
    params: { from: from.toISOString(), to: to.toISOString(), aggregate },
  });
  return data || [];
}

export function feedInSplitTotals(split: FeedInSplit[]): FeedInSplitTotals {
  const sum = (key: keyof FeedInSplitTotals) => split.reduce((acc, b) => acc + b[key], 0);
  return {
    export: sum("export"),
    eeg: sum("eeg"),
    standard: sum("standard"),
    eegRevenue: sum("eegRevenue"),
    standardRevenue: sum("standardRevenue"),
    eegPriced: sum("eegPriced"),
    standardPriced: sum("standardPriced"),
  };
}

export type Translate = (key: string) => string;

// number formats of the page, as the components own them
export interface StatFormats {
  money: (amount: number) => string; // with the currency symbol
  price: (perKWh: number) => string;
}

// Revenue per feed-in tariff, priced slot by slot on its own and so also shown
// without a grid price.
export function feedInEegStats(
  totals: FeedInSplitTotals,
  cost: FlowCost | undefined,
  fmt: StatFormats,
  t: Translate
): StatItem[] {
  const empty = t("energy.stat.noPrice");
  const money = (key: string, label: string, amount: number, energy: number): StatItem => ({
    key,
    label,
    number: amount,
    format: fmt.money,
    sub: energy ? `ø ${fmt.price(amount / energy)}` : "",
  });
  const revenue = (
    key: string,
    label: string,
    amount: number,
    energy: number,
    priced: number
  ): StatItem =>
    energy > 0 && !priced
      ? { key, label, empty }
      : { ...money(key, label, amount, priced), accent: "text-accent1" };
  const stats: StatItem[] = [
    cost
      ? money("gridImport", t("energy.grid.cost"), cost.import, cost.importEnergy)
      : { key: "gridImport", label: t("energy.grid.cost"), empty },
    revenue(
      "gridExport",
      t("energy.grid.revenue"),
      totals.standardRevenue,
      totals.standard,
      totals.standardPriced
    ),
    revenue(
      "gridExportEeg",
      t("energy.feedInEeg.revenue"),
      totals.eegRevenue,
      totals.eeg,
      totals.eegPriced
    ),
  ];
  // a revenue of 0 (rounded to the cent) is not shown; without a price the hint stays
  return stats.filter(
    (s) => s.key === "gridImport" || !!s.empty || Math.round((s.number || 0) * 100) !== 0
  );
}

// The split of the export for the period, empty without an EEG counter or when it
// cannot be read (the page then shows the grid as a whole).
export async function loadFeedInSplit(
  from: Date,
  to: Date,
  aggregate: string,
  entity: string | undefined
): Promise<FeedInSplit[]> {
  if (!entity) return [];
  try {
    return await fetchFeedInSplit(from, to, aggregate);
  } catch (e) {
    console.error("Failed to load feed-in split", e);
    return [];
  }
}
