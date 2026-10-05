import { describe, expect, test } from "vite-plus/test";
import { splitGridSeries, type FeedInSplit } from "./feedInEeg";
import type { HistorySeries } from "./GroupChart.vue";

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
