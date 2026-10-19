import { describe, expect, test } from "vite-plus/test";
import { batteryChargeLabelKey, fmtPowerEstimate, measuredLoadpoints } from "./lmEnergyflow";

describe("measuredLoadpoints", () => {
  test("leaves out loadpoints with assumed power", () => {
    const wallbox = { title: "Wallbox", chargePower: 0 };
    const heater = { title: "Boiler", chargePower: 0, chargePowerEstimated: true };
    const metered = { title: "Heizstab", chargePower: 3000, chargePowerEstimated: false };
    expect(measuredLoadpoints([wallbox, heater, metered])).toEqual([wallbox, metered]);
  });
});

describe("batteryChargeLabelKey", () => {
  test("hold wins over grid charging, which wins over the regular label", () => {
    expect(batteryChargeLabelKey(true, true)).toBe("main.energyflow.batteryChargeHold");
    expect(batteryChargeLabelKey(true, false)).toBe("main.energyflow.batteryChargeHold");
    expect(batteryChargeLabelKey(false, true)).toBe("main.energyflow.batteryChargeGrid");
    expect(batteryChargeLabelKey(false, false)).toBe("main.energyflow.batteryCharge");
  });
});

describe("fmtPowerEstimate", () => {
  const kw = (v: number) => `${v / 1000} kW`;

  test("an assumed power is marked", () => {
    expect(fmtPowerEstimate(3000, 0, 3000, kw)).toBe("≈ 3 kW");
  });

  test("a measured power is left to the regular format", () => {
    expect(fmtPowerEstimate(3000, 2800, 3000, kw)).toBeNull();
    expect(fmtPowerEstimate(0, 0, 0, kw)).toBeNull();
  });
});
