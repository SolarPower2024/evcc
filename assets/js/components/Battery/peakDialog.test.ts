import { describe, expect, test } from "vite-plus/test";
import type { LmLoadStatus } from "@/types/evcc-lm";
import de from "../../../../i18n/de.json";
import en from "../../../../i18n/en.json";
import {
  dialogTitleKey,
  dialogVisible,
  loadStateText,
  loadTone,
  loadsShown,
  sortedLoads,
} from "./peakDialog";
import type { Fmt } from "./peakStatus";

const translate =
  (lang: object) =>
  (key: string, params: Record<string, unknown> = {}): string => {
    const v = key.split(".").reduce<any>((o, k) => o?.[k], lang);
    if (typeof v !== "string") return `?${key}`;
    return v.replace(/\{(\w+)\}/g, (_, n) => String(params[n] ?? `{${n}}`));
  };
const tEn = translate(en);
const tDe = translate(de);

const f: Fmt = {
  w: (w) => `${(w / 1000).toFixed(1)} kW`,
  bare: (w) => (w / 1000).toFixed(1),
  pct: (p) => `${Math.round(p)} %`,
};
const until = () => "18:30";

const load = (over: Partial<LmLoadStatus>): LmLoadStatus => ({
  name: "lp1",
  title: "Garage",
  priority: 1,
  power: 0,
  state: "running",
  ...over,
});

const peak: any = { peakStatus: { state: "free" } };
const withLoads: any = { lmStatus: { loads: [load({})], events: [] } };

describe("menu entry and title", () => {
  test("peak shaving set up", () => {
    expect(dialogVisible(peak)).toBe(true);
    expect(dialogTitleKey(peak)).toBe("battery.peakStatus.title");
    expect(tEn(dialogTitleKey(peak))).toBe("Peak shaving");
    expect(tDe(dialogTitleKey(peak))).toBe("Peak Shaving");
  });

  test("loads without peak shaving: load management", () => {
    expect(dialogVisible(withLoads)).toBe(true);
    expect(tEn(dialogTitleKey(withLoads))).toBe("Load management");
    expect(tDe(dialogTitleKey(withLoads))).toBe("Lastmanagement");
  });

  test("neither: no entry", () => {
    expect(dialogVisible({})).toBe(false);
    expect(dialogVisible(undefined)).toBe(false);
    expect(dialogVisible({ lmStatus: { loads: [], events: [] } })).toBe(false);
  });

  test("peak shaving and loads: peak shaving", () => {
    expect(tDe(dialogTitleKey({ ...peak, ...withLoads }))).toBe("Peak Shaving");
  });
});

describe("the loads section", () => {
  test("needs a circuit with a power or current limit and loads", () => {
    const circuits = { main: { maxPower: 10000 } };
    expect(loadsShown({ ...withLoads, circuits })).toBe(true);
    expect(loadsShown({ ...withLoads, circuits: { main: { maxCurrent: 35 } } })).toBe(true);
    expect(loadsShown({ ...withLoads, circuits: { main: { maxPower: 0, maxCurrent: 0 } } })).toBe(false);
    expect(loadsShown({ ...withLoads, circuits: {} })).toBe(false);
    expect(loadsShown({ ...withLoads })).toBe(false);
    expect(loadsShown({ circuits, lmStatus: { loads: [], events: [] } })).toBe(false);
  });

  test("highest priority first", () => {
    const list = [load({ name: "a", priority: 1 }), load({ name: "b", priority: 5 })];
    expect(sortedLoads(list).map((l) => l.name)).toEqual(["b", "a"]);
    expect(sortedLoads(undefined)).toEqual([]);
  });
});

describe("state texts", () => {
  const cases: [Partial<LmLoadStatus>, string, string, string][] = [
    [{ state: "running" }, "Running", "Läuft", "success"],
    [{ state: "running", battery: true }, "Charging", "Lädt", "success"],
    [
      { state: "throttled", allowed: 4000, requested: 11000 },
      "Throttled · 4.0 of 11.0 kW",
      "Gedrosselt · 4.0 von 11.0 kW",
      "warning",
    ],
    [
      { state: "waiting", allowed: 1000, requested: 4000 },
      "Waiting · needs 4.0 kW, 1.0 kW free",
      "Wartet · braucht 4.0 kW, 1.0 kW frei",
      "info",
    ],
    [
      { state: "shed", until: "x" },
      "Shed · held off until 18:30",
      "Abgeworfen · gesperrt bis 18:30",
      "danger",
    ],
    [
      { state: "shed", battery: true, until: "x" },
      "Blocked · until 18:30",
      "Gesperrt · bis 18:30",
      "danger",
    ],
    [
      { state: "paused", until: "x" },
      "Paused · peak until 18:30",
      "Pausiert · Peak bis 18:30",
      "muted",
    ],
    [{ state: "off" }, "Off", "Aus", "muted"],
  ];

  for (const [over, textEn, textDe, tone] of cases) {
    test(`${over.state}${over.battery ? " battery" : ""}`, () => {
      const l = load(over);
      expect(loadStateText(l, tEn, f, until)).toBe(textEn);
      expect(loadStateText(l, tDe, f, until)).toBe(textDe);
      expect(loadTone(l)).toBe(tone);
    });
  }
});
