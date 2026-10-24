import { describe, expect, test } from "vite-plus/test";
import { OPTIMIZER_AUTOMATIC } from "@/types/evcc";
import { optimizerAutomaticOn, priceLimitIgnored } from "./optimizerAutomatic";

describe("optimizerAutomaticOn", () => {
  test("both levels count as on", () => {
    expect(optimizerAutomaticOn(OPTIMIZER_AUTOMATIC.BATTERY)).toBe(true);
    expect(optimizerAutomaticOn(OPTIMIZER_AUTOMATIC.FULL)).toBe(true);
  });
  test("off, empty and missing count as off", () => {
    expect(optimizerAutomaticOn(OPTIMIZER_AUTOMATIC.OFF)).toBe(false);
    expect(optimizerAutomaticOn("")).toBe(false);
    expect(optimizerAutomaticOn(undefined)).toBe(false);
    expect(optimizerAutomaticOn(null)).toBe(false);
  });
});

describe("priceLimitIgnored", () => {
  test("price and both with automatic mode", () => {
    expect(priceLimitIgnored(OPTIMIZER_AUTOMATIC.BATTERY, "price")).toBe(true);
    expect(priceLimitIgnored(OPTIMIZER_AUTOMATIC.FULL, "both")).toBe(true);
  });
  test("soc never, nor without automatic mode", () => {
    expect(priceLimitIgnored(OPTIMIZER_AUTOMATIC.FULL, "soc")).toBe(false);
    expect(priceLimitIgnored(OPTIMIZER_AUTOMATIC.OFF, "price")).toBe(false);
    expect(priceLimitIgnored(undefined, "both")).toBe(false);
  });
});
