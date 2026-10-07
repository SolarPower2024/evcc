import { describe, expect, test } from "vite-plus/test";
import { measuredLoadpoints } from "./lmEnergyflow";

describe("measuredLoadpoints", () => {
  test("leaves out loadpoints with assumed power", () => {
    const wallbox = { title: "Wallbox", chargePower: 0 };
    const heater = { title: "Boiler", chargePower: 0, chargePowerEstimated: true };
    const metered = { title: "Heizstab", chargePower: 3000, chargePowerEstimated: false };
    expect(measuredLoadpoints([wallbox, heater, metered])).toEqual([wallbox, metered]);
  });
});
