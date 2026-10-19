import { describe, expect, test } from "vite-plus/test";
import { gridChargeMode, gridChargeModeOptions } from "./gridChargeMode";

describe("gridChargeMode", () => {
  test("a known value is kept", () => {
    expect(gridChargeMode("soc")).toBe("soc");
    expect(gridChargeMode("price")).toBe("price");
    expect(gridChargeMode("both")).toBe("both");
  });
  test("missing or unknown reads as both", () => {
    expect(gridChargeMode(undefined)).toBe("both");
    expect(gridChargeMode(null)).toBe("both");
    expect(gridChargeMode("cheap")).toBe("both");
  });
});

describe("gridChargeModeOptions", () => {
  const values = (a: boolean, c: "both" | "soc" | "price") =>
    gridChargeModeOptions(a, c).map((o) => o.value);

  test("with a dynamic tariff all three", () => {
    expect(values(true, "both")).toEqual(["both", "soc", "price"]);
  });
  test("without one only both and soc", () => {
    expect(values(false, "both")).toEqual(["both", "soc"]);
    expect(values(false, "soc")).toEqual(["both", "soc"]);
  });
  test("a stored price stays selectable without a tariff", () => {
    expect(values(false, "price")).toEqual(["both", "soc", "price"]);
  });
});
