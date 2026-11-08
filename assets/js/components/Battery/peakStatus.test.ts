import { describe, expect, test } from "vite-plus/test";
import type { LmEvent, PeakState, PeakStatus } from "@/types/evcc-lm";
import de from "../../../../i18n/de.json";
import en from "../../../../i18n/en.json";
import {
  type Fmt,
  detailText,
  eventText,
  heroText,
  heroTitle,
  heroTone,
  limitsText,
  makeFmt,
  miniValues,
  notes,
  peakEvents,
  windowInfo,
} from "./peakStatus";

// $t on the real language files, with {name} filled in
const translate =
  (lang: object) =>
  (key: string, params: Record<string, unknown> = {}): string => {
    const v = key.split(".").reduce<any>((o, k) => o?.[k], lang);
    if (typeof v !== "string") return `?${key}`;
    return v.replace(/\{(\w+)\}/g, (_, n) => String(params[n] ?? `{${n}}`));
  };

const tEn = translate(en);
const tDe = translate(de);

// plain spaces, so the expected texts read as they are; makeFmt is tested below
const f: Fmt = {
  w: (w) => `${(w / 1000).toFixed(1)} kW`,
  bare: (w) => (w / 1000).toFixed(1),
  pct: (p) => `${Math.round(p)} %`,
};
const kw = f;

const base: PeakStatus = {
  state: "free",
  type: "byd",
  soc: 18,
  reserve: 25,
  freeAt: 27,
  limit: 4000,
  grid: 2300,
  house: 2300,
  battery: 0,
  setpoint: 0,
  limited: true,
  held: false,
  batteryMode: "normal",
  storageOk: true,
  window: {
    avg: 2800,
    allowed: 5600,
    start: "2026-11-07T14:15:00Z",
    end: "2026-11-07T14:30:00Z",
  },
  month: { peak: 6100, peakAt: "2026-11-03T18:00:00Z", interventions: 4 },
};

const states: PeakState[] = [
  "off",
  "storage",
  "meter",
  "gridCharge",
  "gridDischarge",
  "holdShaving",
  "hold",
  "shaving",
  "saving",
  "free",
];

describe("every state has a title, a sentence and a detail in de and en", () => {
  for (const state of states) {
    for (const [lang, t] of [
      ["en", tEn],
      ["de", tDe],
    ] as const) {
      test(`${state} ${lang}`, () => {
        const st = { ...base, state, chargePower: 2500, battery: 1600, house: 5600 };
        for (const text of [heroTitle(st, t), heroText(st, t, kw), detailText(st, t, kw)]) {
          expect(text, text).not.toContain("?battery");
          expect(text, text).not.toMatch(/\{\w+\}/);
          expect(text.length).toBeGreaterThan(3);
        }
      });
    }
  }
});

describe("texts with real numbers", () => {
  test("shaving", () => {
    const st = { ...base, state: "shaving" as const, house: 5600, battery: 1600 };
    expect(heroText(st, tEn, kw)).toBe(
      "The house needs 5.6 kW. The battery adds 1.6 kW, the grid stays at 4.0 kW."
    );
    expect(heroText(st, tDe, kw)).toBe(
      "Das Haus braucht 5.6 kW. Die Batterie gibt 1.6 kW dazu, das Netz bleibt bei 4.0 kW."
    );
  });

  test("saving: the detail, free from only with the hysteresis (byd)", () => {
    const st = { ...base, state: "saving" as const };
    expect(detailText(st, tDe, kw)).toBe(
      "Speicher 18 % liegt unter der Reserve 25 %. Die Batterie entlädt nur, was über 4.0 kW Netzbezug geht. Gerade sind es 2.3 kW, deshalb entlädt sie nicht. Frei ab 27 %."
    );
    expect(detailText({ ...st, freeAt: undefined }, tDe, kw)).not.toContain("Frei ab");
  });

  test("the battery gives the setpoint when no power is measured yet", () => {
    const st = { ...base, state: "holdShaving" as const, battery: 0, setpoint: 1600 };
    expect(heroText(st, tEn, kw)).toBe(
      "The battery is held and gives only 1.6 kW against the peak."
    );
  });

  test("grid discharge", () => {
    const st = { ...base, state: "gridDischarge" as const };
    expect(heroTitle(st, tEn)).toBe("Grid discharge");
    expect(heroText(st, tDe, kw)).toBe("evcc entlädt die Batterie ins Netz.");
  });

  test("grid charging", () => {
    const st = { ...base, state: "gridCharge" as const, chargePower: 2500 };
    expect(heroText(st, tDe, kw)).toBe("evcc lädt die Batterie mit 2.5 kW aus dem Netz.");
  });
});

describe("tone", () => {
  test("a color per state", () => {
    expect(heroTone("meter")).toBe("var(--evcc-orange)");
    expect(heroTone("shaving")).toBe("var(--evcc-red)");
    expect(heroTone("free")).toBe("var(--evcc-darker-green)");
    expect(heroTone("off")).toBe("var(--evcc-gray)");
    for (const s of states) expect(heroTone(s)).toMatch(/^var\(--evcc-/);
  });
});

describe("miniValues", () => {
  test("soc with the reserve mark, grid up to the limit, quarter hour", () => {
    const [soc, grid, quarter] = miniValues({ ...base, state: "saving" }, tEn, kw);
    expect(soc).toMatchObject({ key: "soc", value: "18 %", fill: 0.18, mark: 0.25 });
    expect(grid).toMatchObject({ key: "grid", value: "2.3 kW" });
    expect(grid.fill).toBeCloseTo(0.575);
    expect(quarter).toMatchObject({ key: "quarter", value: "2.8 kW" });
    expect(quarter.label).toBe("15 min");
    expect(miniValues({ ...base, state: "saving" }, tDe, kw)[2].label).toBe("15 Min");
    expect(quarter.fill).toBeCloseTo(0.7);
  });

  test("covering a peak shows the battery instead of the quarter hour", () => {
    for (const state of ["shaving", "holdShaving"] as const) {
      const third = miniValues({ ...base, state, battery: 1600 }, tEn, kw)[2];
      expect(third).toMatchObject({ key: "battery", value: "1.6 kW" });
      expect(third.fill).toBeCloseTo(0.4);
    }
  });

  test("bars stay inside, no window", () => {
    const m = miniValues({ ...base, soc: 120, grid: 9000, window: undefined }, tEn, kw);
    expect(m[0].fill).toBe(1);
    expect(m[1].fill).toBe(1);
    expect(m[2].value).toBe("–");
    expect(m[2].fill).toBe(0);
  });
});

describe("notes", () => {
  test("mode, connection, discharge", () => {
    const n = notes({ ...base, batteryMode: "hold", limited: true, setpoint: 0 }, tEn, kw);
    expect(n.map((x) => x.text)).toEqual([
      "Mode: hold",
      "Storage connected",
      "Discharge max. 0.0 kW",
    ]);
    expect(n[1].tone).toBe("ok");

    const free = notes({ ...base, limited: false, storageOk: false }, tDe, kw);
    expect(free.map((x) => x.text)).toEqual([
      "Modus: normal",
      "Speicher nicht erreichbar",
      "Entladen frei",
    ]);
    expect(free[1].tone).toBe("bad");
  });

  test("mode normal before evcc has set a mode", () => {
    const n = notes({ ...base, batteryMode: "unknown", limited: false }, tDe, kw);
    expect(n.map((x) => x.text)).toEqual(["Modus: normal", "Speicher verbunden", "Entladen frei"]);
    expect(notes({ ...base, batteryMode: "", limited: false }, tDe, kw)[0].text).toBe(
      "Modus: normal"
    );
  });

  test("a hold of another kind says nothing on the discharge", () => {
    const n = notes(
      { ...base, state: "hold", batteryMode: "hold", held: true, limited: false },
      tEn,
      kw
    );
    expect(n.map((x) => x.text)).toEqual(["Mode: hold", "Storage connected"]);
  });

  test("grid charge and discharge say nothing on the discharge either", () => {
    for (const state of ["gridCharge", "gridDischarge"] as const) {
      for (const limited of [false, true]) {
        const n = notes({ ...base, state, limited }, tEn, kw);
        expect(n.some((x) => x.text.startsWith("Discharge"))).toBe(false);
      }
    }
  });

  test("the hint of the integration (byd)", () => {
    const n = notes({ ...base, storageHint: "modeStuck" }, tDe, kw);
    expect(n.at(-1)?.text).toBe("Fronius übernimmt den Modus nicht");
    expect(notes({ ...base }, tDe, kw)).toHaveLength(3);
  });

  test("every mode has a name", () => {
    for (const mode of ["normal", "hold", "holdcharge", "charge", "discharge"]) {
      expect(notes({ ...base, batteryMode: mode }, tEn, kw)[0].text).not.toContain("?");
    }
  });
});

describe("number and unit stay together", () => {
  const fmt = makeFmt(
    (w) => `${(w / 1000).toFixed(1).replace(".", ",")} kW`,
    (p) => `${Math.round(p)} %`,
    (w) => (w / 1000).toFixed(1).replace(".", ",")
  );

  test("makeFmt", () => {
    expect(fmt.w(4000)).toBe("4,0 kW");
    expect(fmt.bare(4000)).toBe("4,0");
    expect(fmt.pct(25)).toBe("25 %");
    expect(fmt.pct(25)).not.toContain(" ");
  });

  test("in the sentences, the tile, the notes and the events", () => {
    const st = { ...base, state: "saving" as const, limited: true, setpoint: 1600 };
    const all = [
      heroText(st, tDe, fmt),
      detailText(st, tDe, fmt),
      limitsText(st, tDe, fmt),
      ...miniValues(st, tDe, fmt).map((m) => m.value),
      ...notes(st, tDe, fmt).map((n) => n.text),
      eventText({ at: "", type: "reserveBelow", a: 24, b: 25 }, tDe, fmt),
      eventText({ at: "", type: "peakEnd", a: 1600, b: 180 }, tDe, fmt),
    ].join(" | ");

    expect(all).not.toMatch(/\d (%|kW)/);
    expect(all).toContain("25 %");
    expect(all).toContain("4,0 kW");
  });
});

describe("windowInfo", () => {
  const at = (iso: string) => new Date(iso).getTime();

  test("the running quarter hour", () => {
    const w = windowInfo(base, at("2026-11-07T14:21:00Z"));
    expect(w?.minutesLeft).toBe(9);
    expect(w?.elapsed).toBeCloseTo(0.4);
    expect(w?.avgFill).toBeCloseTo(0.7);
    expect(w?.allowed).toBe(5600);
    expect(w?.warn).toBe(false);
  });

  test("warns when the allowed power is below the grid draw now", () => {
    const w = windowInfo(
      { ...base, grid: 6000, window: { ...base.window!, allowed: 3000 } },
      at("2026-11-07T14:21:00Z")
    );
    expect(w?.warn).toBe(true);
  });

  test("no window", () => {
    expect(windowInfo({ ...base, window: undefined }, 0)).toBeNull();
  });
});

describe("events", () => {
  const ev = (type: LmEvent["type"], a = 0, b = 0, load?: string): LmEvent => ({
    at: "2026-11-07T14:00:00Z",
    type,
    a,
    b,
    load,
  });

  test("texts de and en", () => {
    const cases: [LmEvent, string, string][] = [
      [
        ev("peakEnd", 1600, 180),
        "Peak over · battery covered up to 1.6 kW, 3 min",
        "Spitze vorbei · Batterie deckte bis 1.6 kW, 3 min",
      ],
      [
        ev("reserveBelow", 24, 25),
        "Below 25 %: now saving for peaks",
        "Unter 25 %: spart jetzt für Spitzen",
      ],
      [ev("reserveAbove", 26, 25), "Above 25 %: battery free", "Über 25 %: Batterie frei"],
      [ev("batteryMode", 0, 0, "hold"), "evcc: mode hold", "evcc: Modus Halten"],
      [ev("peakSwitch", 1), "Peak shaving on", "Peak Shaving an"],
      [ev("peakSwitch", 0), "Peak shaving off", "Peak Shaving aus"],
      [ev("meter", 1), "Meter outage · battery free", "Messausfall · Batterie frei"],
      [ev("meter", 0), "Meter back", "Messung wieder da"],
      [ev("storage", 1), "Storage not reachable", "Speicher nicht erreichbar"],
      [ev("storage", 0), "Storage connected again", "Speicher wieder verbunden"],
    ];

    for (const [e, textEn, textDe] of cases) {
      expect(eventText(e, tEn, kw), e.type).toBe(textEn);
      expect(eventText(e, tDe, kw), e.type).toBe(textDe);
    }
  });

  test("a peak of less than 30 s counts as one minute", () => {
    expect(eventText(ev("peakEnd", 1000, 20), tEn, kw)).toContain("1 min");
  });

  test("the load management types have their texts", () => {
    const cases: [LmEvent, string, string][] = [
      [
        ev("shed", 0, 9000, "Garage"),
        "Garage shed · circuit at 9.0 kW",
        "Garage abgeworfen · Stromkreis bei 9.0 kW",
      ],
      [
        ev("shed", 10, 9000, "Garage"),
        "Garage shed · circuit at 9.0 kW, held off 10 min",
        "Garage abgeworfen · Stromkreis bei 9.0 kW, 10 min gesperrt",
      ],
      [
        ev("throttled", 11000, 6000, "Garage"),
        "Garage throttled 11.0 → 6.0 kW",
        "Garage gedrosselt 11.0 → 6.0 kW",
      ],
      [
        ev("peak", 5600, 4000),
        "Peak covered · demand 5.6 kW, limit 4.0 kW",
        "Peak abgefangen · Bedarf 5.6 kW, Grenze 4.0 kW",
      ],
    ];

    for (const [e, textEn, textDe] of cases) {
      expect(eventText(e, tEn, kw), e.type).toBe(textEn);
      expect(eventText(e, tDe, kw), e.type).toBe(textDe);
    }
  });

  test("every type has a text, not the type name", () => {
    const types: LmEvent["type"][] = [
      "shed",
      "throttled",
      "gridChargePaused",
      "gridChargeDenied",
      "peak",
      "notFollowing",
      "peakEnd",
      "reserveBelow",
      "reserveAbove",
      "batteryMode",
      "peakSwitch",
      "meter",
      "storage",
    ];
    for (const type of types) {
      for (const t of [tEn, tDe]) {
        const text = eventText(ev(type, 1, 2, "x"), t, kw);
        expect(text, type).not.toBe(type);
        expect(text, type).not.toMatch(/^\?|\{\w+\}/);
      }
    }
  });

  test("the dialog lists all events, newest first", () => {
    const at = (iso: string, type: LmEvent["type"]): LmEvent => ({ ...ev(type), at: iso });
    const list = [
      at("2026-11-07T14:00:00Z", "shed"),
      at("2026-11-07T16:00:00Z", "peak"),
      at("2026-11-07T15:00:00Z", "throttled"),
    ];
    expect(peakEvents(list).map((e) => e.type)).toEqual(["peak", "throttled", "shed"]);
    expect(peakEvents(undefined)).toEqual([]);
  });
});

describe("texts de and en stay the same size", () => {
  const keys = (o: any, p = ""): string[] =>
    Object.entries(o).flatMap(([k, v]) =>
      typeof v === "object" && v ? keys(v, `${p}${k}.`) : [`${p}${k}`]
    );

  test("same keys under battery.peakStatus", () => {
    expect(keys((de as any).battery.peakStatus).sort()).toEqual(
      keys((en as any).battery.peakStatus).sort()
    );
  });

  test("same placeholders", () => {
    const holders = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort().join();
    const walk = (a: any, b: any, path: string) => {
      for (const k of Object.keys(a)) {
        if (typeof a[k] === "object") walk(a[k], b[k], `${path}.${k}`);
        else expect(holders(a[k]), `${path}.${k}`).toBe(holders(b[k]));
      }
    };
    walk((de as any).battery.peakStatus, (en as any).battery.peakStatus, "battery.peakStatus");
  });
});
