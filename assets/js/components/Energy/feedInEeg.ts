// Custom extension: export sold under two feed-in tariffs, see
// core/site_feedin_eeg.go. The energy page splits the grid export into the
// standard feed-in tariff and the EEG part metered by a Home Assistant counter,
// in the chart, the legend and the revenue tiles. Without a counter nothing
// changes.
import api from "@/api";
import colors, { lighten } from "@/colors";
import type { HistorySeries, HistorySlot } from "./GroupChart.vue";
import type { Legend } from "../Sessions/types";
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

// the EEG part next to the standard export, a lighter shade of the export color
export function eegColor(): string {
  return lighten(colors.export || "", 0.5);
}

// bucket key independent of the first slot a history bucket starts with
function bucketKey(start: string, aggregate: string): string {
  const d = new Date(start);
  const pad = (n: number) => String(n).padStart(2, "0");
  const day = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  switch (aggregate) {
    case "month":
      return day.slice(0, 7);
    case "day":
      return day;
    case "hour":
      return `${day} ${pad(d.getHours())}`;
    default:
      return String(d.getTime());
  }
}

// Grid series with the EEG part taken out of the export, plus the EEG part as a
// series of its own, stacked below it in the export direction.
export function splitGridSeries(
  grid: HistorySeries[],
  split: FeedInSplit[],
  aggregate: string,
  gridTitle: string
): HistorySeries[] {
  // EEG left to take out per bucket: after a grid meter swap a bucket has
  // export on several grid series, the EEG part comes off only once
  const eeg = new Map(split.map((b) => [bucketKey(b.start, aggregate), b.eeg]));
  const takeOut = (key: string, export_: number) => {
    const part = Math.min(export_, eeg.get(key) || 0);
    eeg.set(key, (eeg.get(key) || 0) - part);
    return export_ - part;
  };

  const standard = grid.map((s) => ({
    ...s,
    title: gridTitle,
    data: s.data.map((slot): HistorySlot => ({
      ...slot,
      returnEnergy: takeOut(bucketKey(slot.start, aggregate), Math.max(0, slot.returnEnergy)),
    })),
  }));

  const color = eegColor();
  const eegSeries: HistorySeries = {
    title: "EEG",
    group: "eeg",
    color,
    returnColor: color,
    data: split
      .filter((b) => b.eeg > 0)
      .map((b) => ({ start: b.start, end: b.end, energy: 0, returnEnergy: b.eeg })),
  };

  return [eegSeries, ...standard];
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
  return [
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
}

// The grid legend with the export entry replaced by the standard tariff's part and
// the EEG part, in the colors of the chart.
export function splitLegends(
  list: Legend[],
  totals: FeedInSplitTotals,
  t: Translate,
  fmtKWh: (kWh: number) => string
): Legend[] {
  const res = [...list];
  res.splice(
    1,
    1,
    {
      label: t("energy.grid.revenue"),
      color: colors.export || "",
      value: fmtKWh(totals.standard),
    },
    {
      label: t("energy.feedInEeg.eeg"),
      color: eegColor(),
      value: fmtKWh(totals.eeg),
    }
  );
  return res;
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
