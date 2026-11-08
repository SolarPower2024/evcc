import { describe, expect, test } from "vite-plus/test";
import { defaultPriceTemplate, priceTemplatesFor, gridFeeAfterPct } from "./lmGridTariff";

const generic = [{ template: "fixed" }, { template: "fixed-netz" }, { template: "fixed-zones" }];
const names = (list: { template: string }[]) => list.map((p) => p.template);

describe("priceTemplatesFor", () => {
  test("new grid tariff gets the split template instead of fixed price", () => {
    expect(names(priceTemplatesFor(generic, "grid", true))).toEqual(["fixed-netz", "fixed-zones"]);
  });
  test("editing a grid tariff keeps both", () => {
    expect(names(priceTemplatesFor(generic, "grid", false))).toEqual([
      "fixed",
      "fixed-netz",
      "fixed-zones",
    ]);
  });
  test("other tariff types never see the split template", () => {
    for (const type of ["feedIn", "feedInEeg", "planner", "co2", undefined]) {
      for (const isNew of [true, false]) {
        expect(names(priceTemplatesFor(generic, type, isNew))).toEqual(["fixed", "fixed-zones"]);
      }
    }
  });
});

describe("defaultPriceTemplate", () => {
  test("grid starts with the split template, feed-in as before", () => {
    expect(defaultPriceTemplate("grid")).toBe("fixed-netz");
    expect(defaultPriceTemplate("feedIn")).toBe("fixed");
    expect(defaultPriceTemplate("feedInEeg")).toBeUndefined();
    expect(defaultPriceTemplate(null)).toBeUndefined();
  });
});

describe("gridFeeAfterPct", () => {
  test("deducts the percentage from the grid fee", () => {
    expect(gridFeeAfterPct(0.102, 20)).toBeCloseTo(0.0816, 6);
    expect(gridFeeAfterPct(0.085, 0)).toBeCloseTo(0.085, 6);
    expect(gridFeeAfterPct(0.085, 100)).toBeCloseTo(0, 6);
  });
});
