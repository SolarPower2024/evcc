import { describe, expect, test } from "vite-plus/test";
import { profileProblem } from "./lmProfile";

describe("profileProblem", () => {
  test("empty and valid", () => {
    expect(profileProblem({})).toBeNull();
    expect(
      profileProblem({
        gridChargeStart: 20,
        gridChargeStop: 80,
        prioritySoc: 30,
        bufferSoc: 50,
        bufferStartSoc: 60,
        peakReserve: 30,
        peakLimit: 6.5,
        "solarShare:wb": 100,
      })
    ).toBeNull();
  });

  test.each([
    ["gridChargeStart", -1, 0, 100],
    ["gridChargeStop", 101, 0, 100],
    ["prioritySoc", 101, 0, 100],
    ["bufferSoc", -1, 0, 100],
    ["bufferStartSoc", 101, 0, 100],
    ["peakReserve", 0, 1, 99],
    ["peakReserve", 100, 1, 99],
    ["peakLimit", 1.5, 2, 20],
    ["peakLimit", 20.5, 2, 20],
    ["solarShare:wb", 101, 0, 100],
    ["solarShare:wb", -1, 0, 100],
  ])("%s = %s is out of range", (key, value, min, max) => {
    expect(profileProblem({ [key]: value })).toEqual({ code: "range", key, min, max });
  });

  test.each([
    ["gridChargeStart", 0],
    ["gridChargeStop", 100],
    ["peakReserve", 1],
    ["peakReserve", 99],
    ["peakLimit", 2],
    ["peakLimit", 20],
    ["solarShare:wb", 0],
    ["solarShare:wb", 100],
  ])("%s = %s is on the border", (key, value) => {
    expect(profileProblem({ [key]: value })).toBeNull();
  });

  test("peak limit in 0.5 kW steps", () => {
    expect(profileProblem({ peakLimit: 6.25 })).toEqual({
      code: "step",
      key: "peakLimit",
      step: 0.5,
    });
    expect(profileProblem({ peakLimit: 6.5 })).toBeNull();
  });

  test("grid charge start below stop", () => {
    expect(profileProblem({ gridChargeStart: 80, gridChargeStop: 80 })).toEqual({
      code: "gridChargeOrder",
      key: "",
    });
    expect(profileProblem({ gridChargeStart: 90, gridChargeStop: 80 })?.code).toBe(
      "gridChargeOrder"
    );
    expect(profileProblem({ gridChargeStart: 79, gridChargeStop: 80 })).toBeNull();
    expect(profileProblem({ gridChargeStart: 90 })).toBeNull();
  });

  test("priority <= buffer <= buffer start, 0 is off", () => {
    expect(profileProblem({ prioritySoc: 60, bufferSoc: 50 })?.code).toBe("batteryOrder");
    expect(profileProblem({ bufferSoc: 70, bufferStartSoc: 60 })?.code).toBe("batteryOrder");
    expect(profileProblem({ prioritySoc: 50, bufferSoc: 50, bufferStartSoc: 50 })).toBeNull();
    expect(profileProblem({ prioritySoc: 60, bufferSoc: 0 })).toBeNull();
    expect(profileProblem({ bufferSoc: 70, bufferStartSoc: 0 })).toBeNull();
  });
});
