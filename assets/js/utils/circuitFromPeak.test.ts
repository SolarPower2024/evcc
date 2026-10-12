import { describe, expect, test } from "vite-plus/test";
import { circuitPeakHintLimit } from "./circuitFromPeak";

describe("circuitPeakHintLimit", () => {
  const on = {
    lmOff: { circuit: "main" },
    lmAdvanced: { circuitFromPeak: true },
    peakShavingLimit: 3500,
  };

  test("the load management circuit shows the peak limit while the switch is on", () => {
    expect(circuitPeakHintLimit(on, "main")).toBe(3500);
    expect(circuitPeakHintLimit({ ...on, peakShavingLimit: undefined }, "main")).toBe(0);
  });

  test("nothing changes without the switch, for other circuits and without a circuit", () => {
    expect(circuitPeakHintLimit(on, "garage")).toBeUndefined();
    expect(circuitPeakHintLimit(on, undefined)).toBeUndefined();
    expect(circuitPeakHintLimit({ ...on, lmAdvanced: { circuitFromPeak: false } }, "main")).toBe(
      undefined
    );
    expect(circuitPeakHintLimit({ ...on, lmAdvanced: {} }, "main")).toBeUndefined();
    expect(circuitPeakHintLimit({ ...on, lmOff: { circuit: "" } }, "main")).toBeUndefined();
    expect(circuitPeakHintLimit(undefined, "main")).toBeUndefined();
    expect(circuitPeakHintLimit({}, "main")).toBeUndefined();
  });
});
