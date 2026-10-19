import { describe, expect, test, vi } from "vite-plus/test";
import api from "@/api";
import {
  feedInEegStats,
  feedInSplitTotals,
  loadFeedInSplit,
  splitGridSeries,
  splitLegends,
  type FeedInSplit,
} from "./feedInEeg";
import type { Legend } from "../Sessions/types";
import type { HistorySeries } from "./GroupChart.vue";

vi.mock("@/api", () => ({ default: { get: vi.fn() } }));

const start = "2026-10-05T10:00:00Z";
const end = "2026-10-05T10:15:00Z";

const grid = (title: string, returnEnergy: number): HistorySeries => ({
  title,
  group: "grid",
  data: [{ start, end, energy: 0, returnEnergy }],
});

const split = (eeg: number): FeedInSplit[] => [
  {
    start,
    end,
    export: 0,
    eeg,
    standard: 0,
    eegRevenue: 0,
    standardRevenue: 0,
    eegPriced: 0,
    standardPriced: 0,
  },
];

describe("splitGridSeries", () => {
  test("takes the EEG part out of the export", () => {
    const [eeg, std] = splitGridSeries([grid("grid", 3)], split(1), "15m", "Netz");
    expect(eeg!.data[0]!.returnEnergy).toBe(1);
    expect(std!.data[0]!.returnEnergy).toBe(2);
  });

  test("after a grid meter swap the EEG part comes off only once", () => {
    const [, a, b] = splitGridSeries([grid("old", 1), grid("new", 2)], split(1.5), "15m", "Netz");
    expect(a!.data[0]!.returnEnergy).toBe(0);
    expect(b!.data[0]!.returnEnergy).toBe(1.5);
  });

  test("never below zero", () => {
    const [, std] = splitGridSeries([grid("grid", 1)], split(2), "15m", "Netz");
    expect(std!.data[0]!.returnEnergy).toBe(0);
  });
});

const bucket = (over: Partial<FeedInSplit>): FeedInSplit => ({ ...split(0)[0]!, ...over });

const t = (key: string) => `t:${key}`;

describe("feedInEegStats", () => {
  const fmt = { money: (v: number) => `${v} EUR`, price: (v: number) => `${v}/kWh` };
  const totals = feedInSplitTotals([
    bucket({
      standard: 4,
      eeg: 2,
      standardRevenue: 0.4,
      eegRevenue: 0.3,
      standardPriced: 4,
      eegPriced: 2,
    }),
    bucket({
      standard: 6,
      eeg: 3,
      standardRevenue: 0.6,
      eegRevenue: 0.45,
      standardPriced: 6,
      eegPriced: 3,
    }),
  ]);

  test("sums the buckets", () => {
    expect(totals.standard).toBe(10);
    expect(totals.eegRevenue).toBeCloseTo(0.75);
  });

  test("cost, revenue and EEG revenue in this order", () => {
    const cost = { import: 2, export: 1, importEnergy: 10, exportEnergy: 15, avgGrid: 0.2 };
    const [imp, std, eeg] = feedInEegStats(totals, cost, fmt, t);
    expect([imp!.key, std!.key, eeg!.key]).toEqual(["gridImport", "gridExport", "gridExportEeg"]);
    expect(imp!.number).toBe(2);
    expect(imp!.sub).toBe("ø 0.2/kWh");
    expect(std!.number).toBeCloseTo(1);
    expect(eeg!.label).toBe("t:energy.feedInEeg.revenue");
  });

  test("without a grid price the import shows empty, the revenue is still shown", () => {
    const [imp, std] = feedInEegStats(totals, undefined, fmt, t);
    expect(imp!.empty).toBe("t:energy.stat.noPrice");
    expect(std!.number).toBeCloseTo(1);
  });

  test("an export without a price shows empty", () => {
    const [, std] = feedInEegStats(
      feedInSplitTotals([bucket({ standard: 4, standardPriced: 0 })]),
      undefined,
      fmt,
      t
    );
    expect(std!.empty).toBe("t:energy.stat.noPrice");
  });
});

describe("splitLegends", () => {
  const list: Legend[] = [
    { label: "import", color: "a", value: "1" },
    { label: "export", color: "b", value: "2" },
    { label: "price", color: "c", value: "3" },
  ];

  test("replaces the export by the standard and the EEG part", () => {
    const totals = feedInSplitTotals([bucket({ standard: 4, eeg: 2 })]);
    const res = splitLegends(list, totals, t, (v) => `${v} kWh`);
    expect(res.map((l) => l.label)).toEqual([
      "import",
      "t:energy.grid.revenue",
      "t:energy.feedInEeg.eeg",
      "price",
    ]);
    expect(res[1]!.value).toBe("4 kWh");
    expect(res[2]!.value).toBe("2 kWh");
    expect(list).toHaveLength(3);
  });
});

describe("loadFeedInSplit", () => {
  const from = new Date("2026-10-05T00:00:00Z");
  const to = new Date("2026-10-06T00:00:00Z");

  test("without an entity it is empty and asks nothing", async () => {
    vi.mocked(api.get).mockClear();
    expect(await loadFeedInSplit(from, to, "day", undefined)).toEqual([]);
    expect(api.get).not.toHaveBeenCalled();
  });

  test("with an entity it returns the split", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: split(1) });
    expect(await loadFeedInSplit(from, to, "day", "sensor.eeg")).toEqual(split(1));
  });

  test("a failing request is empty", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    vi.mocked(api.get).mockRejectedValueOnce(new Error("x"));
    expect(await loadFeedInSplit(from, to, "day", "sensor.eeg")).toEqual([]);
  });
});
