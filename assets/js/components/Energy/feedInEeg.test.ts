import { describe, expect, test, vi } from "vite-plus/test";
import api from "@/api";
import { feedInEegStats, feedInSplitTotals, loadFeedInSplit, type FeedInSplit } from "./feedInEeg";

vi.mock("@/api", () => ({ default: { get: vi.fn() } }));

const start = "2026-10-05T10:00:00Z";
const end = "2026-10-05T10:15:00Z";

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

  test("a revenue of 0 is hidden, the cost stays", () => {
    const zero = feedInSplitTotals([
      bucket({
        standard: 0,
        eeg: 0.22,
        standardRevenue: 0,
        eegRevenue: 0,
        standardPriced: 0,
        eegPriced: 0.22,
      }),
    ]);
    const cost = { import: 0.73, export: 0, importEnergy: 3, exportEnergy: 0.22, avgGrid: 0.247 };
    expect(feedInEegStats(zero, cost, fmt, t).map((s) => s.key)).toEqual(["gridImport"]);
    const half = feedInSplitTotals([
      bucket({ standard: 1, standardRevenue: 0.004, standardPriced: 1 }),
    ]);
    expect(feedInEegStats(half, cost, fmt, t).map((s) => s.key)).toEqual(["gridImport"]);
    const some = feedInSplitTotals([bucket({ eeg: 1, eegRevenue: 0.05, eegPriced: 1 })]);
    expect(feedInEegStats(some, cost, fmt, t).map((s) => s.key)).toEqual([
      "gridImport",
      "gridExportEeg",
    ]);
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
