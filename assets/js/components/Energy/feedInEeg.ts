// Custom extension: export sold under two feed-in tariffs, see
// core/site_feedin_eeg.go. The energy page splits the grid export into the
// standard feed-in tariff and the EEG part metered by a Home Assistant counter,
// in the chart, the legend and the revenue tiles. Without a counter nothing
// changes.
import api from "@/api";
import colors, { lighten } from "@/colors";
import type { HistorySeries, HistorySlot } from "./GroupChart.vue";

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
  const eeg = new Map(split.map((b) => [bucketKey(b.start, aggregate), b.eeg]));

  const standard = grid.map((s) => ({
    ...s,
    title: gridTitle,
    data: s.data.map(
      (slot): HistorySlot => ({
        ...slot,
        returnEnergy: Math.max(
          0,
          slot.returnEnergy - (eeg.get(bucketKey(slot.start, aggregate)) || 0)
        ),
      })
    ),
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
