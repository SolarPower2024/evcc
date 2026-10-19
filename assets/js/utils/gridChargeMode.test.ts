import { describe, expect, test } from "vite-plus/test";
import {
  gridChargeMode,
  gridChargeModeOptions,
  onceCardVisible,
  priceCardVisible,
  socCardVisible,
} from "./gridChargeMode";

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

describe("cards on the battery page", () => {
  test("price card: evcc's own condition and not soc only", () => {
    // with a dynamic tariff or a limit set (evcc's condition true)
    expect(priceCardVisible(true, "both")).toBe(true);
    expect(priceCardVisible(true, undefined)).toBe(true);
    expect(priceCardVisible(true, "price")).toBe(true);
    expect(priceCardVisible(true, "soc")).toBe(false);
    // without a dynamic tariff: never, whatever is stored
    expect(priceCardVisible(false, "both")).toBe(false);
    expect(priceCardVisible(false, "price")).toBe(false);
    expect(priceCardVisible(false, "soc")).toBe(false);
  });
  test("soc card unless price only", () => {
    expect(socCardVisible("both")).toBe(true);
    expect(socCardVisible(undefined)).toBe(true);
    expect(socCardVisible("soc")).toBe(true);
    expect(socCardVisible("price")).toBe(false);
    expect(socCardVisible("price", false)).toBe(false);
  });
  test("soc card stays for price while the soc switch is on", () => {
    expect(socCardVisible("price", true)).toBe(true);
    expect(socCardVisible("soc", true)).toBe(true);
  });
  test("once card of its own only for price only", () => {
    expect(onceCardVisible("price")).toBe(true);
    expect(onceCardVisible("soc")).toBe(false);
    expect(onceCardVisible("both")).toBe(false);
    expect(onceCardVisible(undefined)).toBe(false);
  });
});
