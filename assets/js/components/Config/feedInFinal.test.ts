import { describe, expect, test } from "vite-plus/test";
import { feedInNextFinalization, fmtMarketPrice, monthDate, monthKey } from "./feedInFinal";

const day = (y: number, m: number, d: number) => new Date(y, m - 1, d);

describe("feedInNextFinalization", () => {
  test("before the finalize day, previous month open: this month", () => {
    expect(feedInNextFinalization(day(2026, 9, 10), 15, "2026-07")).toEqual(day(2026, 9, 15));
  });
  test("past the finalize day, previous month still open: due now", () => {
    const now = day(2026, 9, 20);
    expect(feedInNextFinalization(now, 15, "2026-07")).toEqual(now);
  });
  test("previous month done: next month", () => {
    expect(feedInNextFinalization(day(2026, 9, 20), 15, "2026-08")).toEqual(day(2026, 10, 15));
  });
  test("nothing finalized yet", () => {
    expect(feedInNextFinalization(day(2026, 9, 10), 15)).toEqual(day(2026, 9, 15));
  });
  test("january looks at december", () => {
    expect(feedInNextFinalization(day(2027, 1, 20), 15, "2026-12")).toEqual(day(2027, 2, 15));
  });
});

describe("months", () => {
  test("round trip", () => {
    expect(monthKey(monthDate("2026-08"))).toBe("2026-08");
    expect(monthKey(day(2026, 1, 31))).toBe("2026-01");
  });
});

describe("fmtMarketPrice", () => {
  test("three decimals in ct", () => {
    expect(fmtMarketPrice(0.08997, "de")).toBe("8,997 ct/kWh");
    expect(fmtMarketPrice(0.09, "en")).toBe("9.000 ct/kWh");
  });
});
