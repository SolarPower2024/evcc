import { describe, expect, test } from "vite-plus/test";
import { peakShavingSetUp } from "./peakShaving";

describe("peakShavingSetUp", () => {
  test("BYD needs the discharge entity", () => {
    expect(peakShavingSetUp({})).toBe(false);
    expect(peakShavingSetUp({ peakShavingBatteryType: "byd" })).toBe(false);
    expect(peakShavingSetUp({ peakShavingBatteryType: "byd", peakShavingEntity: "number.x" })).toBe(
      true
    );
    expect(peakShavingSetUp({ peakShavingEntity: "number.x" })).toBe(true);
    expect(peakShavingSetUp(undefined)).toBe(false);
  });

  test("Marstek needs the three entities of the peak shaving, not the discharge entity", () => {
    const marstek = { peakShavingBatteryType: "marstek" };
    expect(peakShavingSetUp({ ...marstek, peakShavingEntity: "number.x" })).toBe(false);
    expect(
      peakShavingSetUp({
        ...marstek,
        peakShavingProtSwitch: "switch.p",
        peakShavingProtLimit: "number.l",
      })
    ).toBe(false);
    expect(
      peakShavingSetUp({
        ...marstek,
        peakShavingProtSwitch: "switch.p",
        peakShavingProtLimit: "number.l",
        peakShavingProtSoc: "number.s",
      })
    ).toBe(true);
  });
});
