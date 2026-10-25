import { describe, expect, test } from "vite-plus/test";
import { peakShavingSetUp } from "./peakShaving";

describe("peakShavingSetUp", () => {
  const entities = {
    peakShavingProtSwitch: "switch.p",
    peakShavingProtLimit: "number.l",
    peakShavingProtSoc: "number.s",
  };

  test("needs the three entities of the peak shaving", () => {
    expect(peakShavingSetUp({})).toBe(false);
    expect(peakShavingSetUp(undefined)).toBe(false);
    expect(peakShavingSetUp({ peakShavingProtSwitch: "switch.p" })).toBe(false);
    expect(
      peakShavingSetUp({ peakShavingProtSwitch: "switch.p", peakShavingProtLimit: "number.l" })
    ).toBe(false);
    expect(peakShavingSetUp(entities)).toBe(true);
  });

  test("the same for both battery types", () => {
    for (const peakShavingBatteryType of ["byd", "marstek", undefined]) {
      expect(peakShavingSetUp({ peakShavingBatteryType })).toBe(false);
      expect(peakShavingSetUp({ ...entities, peakShavingBatteryType })).toBe(true);
    }
  });

  test("the request is not needed", () => {
    expect(peakShavingSetUp({ ...entities, peakShavingBatteryType: "byd" })).toBe(true);
  });
});
