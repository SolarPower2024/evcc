// Custom extension: the texts and numbers of the peak status dialog, see
// core/site_peak_status.go. The state comes finished from the backend, these
// functions only turn it into text and bar values. Pure, so they are tested
// without a component (peakStatus.test.ts).
import type { LmEvent, PeakState, PeakStatus } from "@/types/evcc-lm";

export type Translate = (key: string, params?: Record<string, unknown>) => string;

/** Number formats of the dialog: power (kW with unit, without) and percent, as the battery card formats them. */
export interface Fmt {
  w: (watt: number) => string;
  /** Power in kW without the unit. */
  bare: (watt: number) => string;
  pct: (percent: number) => string;
}

const NBSP = " ";

/** A number and its unit stay on one line. */
const nb = (text: string): string => text.replace(/(\d) (?=%|[kMG]?W\b)/g, `$1${NBSP}`);

/** Formats for the dialog from the formatter of the app. */
export function makeFmt(
  fmtW: (watt: number) => string,
  fmtPercentage: (p: number) => string,
  fmtBare: (watt: number) => string
): Fmt {
  return { w: (watt) => nb(fmtW(watt)), bare: fmtBare, pct: (p) => nb(fmtPercentage(p)) };
}

const P = "battery.peakStatus";

/** Color of the tile per state, evcc variables (light and dark). */
const TONES: Record<PeakState, string> = {
  off: "var(--evcc-gray)",
  storage: "var(--evcc-gray)",
  meter: "var(--evcc-orange)",
  gridCharge: "var(--evcc-darker-green)",
  gridDischarge: "var(--evcc-darker-yellow)",
  holdShaving: "var(--evcc-red)",
  hold: "var(--evcc-default-text)",
  shaving: "var(--evcc-red)",
  saving: "var(--evcc-darker-yellow)",
  free: "var(--evcc-darker-green)",
};

export function heroTone(state: PeakState): string {
  return TONES[state];
}

export function heroTitle(st: PeakStatus, t: Translate): string {
  return t(`${P}.state.${st.state}.title`);
}

/** The power the battery gives against the peak: measured, else what evcc passes on. */
function covering(st: PeakStatus): number {
  return st.battery > 0 ? st.battery : st.setpoint;
}

function params(st: PeakStatus, f: Fmt): Record<string, unknown> {
  return {
    soc: f.pct(st.soc),
    reserve: f.pct(st.reserve),
    limit: f.w(st.limit),
    grid: f.w(st.grid),
    house: f.w(st.house),
    battery: f.w(covering(st)),
    power: f.w(st.chargePower ?? 0),
  };
}

/** The sentence of the tile. */
export function heroText(st: PeakStatus, t: Translate, f: Fmt): string {
  return t(`${P}.state.${st.state}.text`, params(st, f));
}

/** The line under the values of the tile. */
export function limitsText(st: PeakStatus, t: Translate, f: Fmt): string {
  return t(`${P}.reserveLimit`, { reserve: f.pct(st.reserve), limit: f.w(st.limit) });
}

/** The longer sentence under "what is happening". */
export function detailText(st: PeakStatus, t: Translate, f: Fmt): string {
  let text = t(`${P}.state.${st.state}.detail`, params(st, f));

  // the band of the integration: free again from the reserve plus 2 %
  if ((st.state === "saving" || st.state === "shaving") && st.freeAt) {
    text += " " + t(`${P}.freeAt`, { freeAt: f.pct(st.freeAt) });
  }

  return text;
}

export interface MiniValue {
  key: "soc" | "grid" | "quarter" | "battery";
  label: string;
  value: string;
  /** Bar fill, 0 to 1. */
  fill: number;
  /** Mark on the bar, 0 to 1 (the reserve). */
  mark?: number;
}

const unit = (v: number) => Math.min(1, Math.max(0, v));

/** The three values of the tile with their bars. */
export function miniValues(st: PeakStatus, t: Translate, f: Fmt): MiniValue[] {
  const res: MiniValue[] = [
    {
      key: "soc",
      label: t(`${P}.mini.soc`),
      value: f.pct(st.soc),
      fill: unit(st.soc / 100),
      mark: unit(st.reserve / 100),
    },
    {
      key: "grid",
      label: t(`${P}.mini.grid`),
      value: f.w(st.grid),
      fill: st.limit > 0 ? unit(st.grid / st.limit) : 0,
    },
  ];

  if (st.state === "shaving" || st.state === "holdShaving") {
    const power = covering(st);
    res.push({
      key: "battery",
      label: t(`${P}.mini.battery`),
      value: f.w(power),
      fill: st.limit > 0 ? unit(power / st.limit) : 0,
    });
  } else {
    res.push({
      key: "quarter",
      label: t(`${P}.mini.quarter`),
      value: st.window ? f.w(st.window.avg) : "–",
      fill: st.window && st.limit > 0 ? unit(st.window.avg / st.limit) : 0,
    });
  }

  return res;
}

export interface Note {
  text: string;
  tone: "ok" | "bad" | "plain";
}

/** The notes: mode, storage connection, discharge, hints of the integration. */
export function notes(st: PeakStatus, t: Translate, f: Fmt): Note[] {
  const res: Note[] = [];

  // before evcc has set a mode the battery runs as in normal
  const mode = st.batteryMode && st.batteryMode !== "unknown" ? st.batteryMode : "normal";
  res.push({
    text: t(`${P}.notes.mode`, { mode: t(`${P}.mode.${mode}`) }),
    tone: "plain",
  });

  res.push(
    st.storageOk
      ? { text: t(`${P}.notes.connected`), tone: "ok" }
      : { text: t(`${P}.notes.notConnected`), tone: "bad" }
  );

  // no word on the discharge where it is not evcc's call: grid charging and
  // discharge, a hold of another kind
  const other = st.state === "gridCharge" || st.state === "gridDischarge";
  if (st.limited && !other) {
    res.push({ text: t(`${P}.notes.dischargeMax`, { power: f.w(st.setpoint) }), tone: "plain" });
  } else if (!st.held && !other) {
    res.push({ text: t(`${P}.notes.dischargeFree`), tone: "plain" });
  }

  if (st.storageHint) {
    res.push({ text: t(`${P}.hint.${st.storageHint}`), tone: "bad" });
  }

  return res;
}

export interface WindowInfo {
  /** Average so far against the limit, 0 to 1. */
  avgFill: number;
  /** Elapsed part of the quarter hour, 0 to 1. */
  elapsed: number;
  minutesLeft: number;
  allowed: number;
  /** The allowed power is below the grid power now: the average will rise above the limit. */
  warn: boolean;
}

/** The running quarter hour; now in ms. */
export function windowInfo(st: PeakStatus, now: number): WindowInfo | null {
  const w = st.window;
  if (!w) return null;

  const start = new Date(w.start).getTime();
  const end = new Date(w.end).getTime();
  const span = end - start;

  return {
    avgFill: st.limit > 0 ? unit(w.avg / st.limit) : 0,
    elapsed: span > 0 ? unit((now - start) / span) : 0,
    minutesLeft: Math.max(0, Math.ceil((end - now) / 60000)),
    allowed: w.allowed,
    warn: w.allowed < st.grid,
  };
}

/**
 * Event types of the peak shaving ("Zuletzt"), the others are the actions of the
 * load management ("Letzte Eingriffe"). The same list as lm.PeakEvent in
 * core/lm/status.go; core/site_lm_events_test.go (TestPeakEventListsMatch) checks them.
 */
const PEAK_EVENTS = new Set<string>([
  "peak",
  "peakEnd",
  "reserveBelow",
  "reserveAbove",
  "batteryMode",
  "peakSwitch",
  "meter",
  "storage",
  "gridChargePaused",
]);

/** The events of the peak shaving, in the order given (newest first). */
export function peakEvents(events: LmEvent[] = []): LmEvent[] {
  return events.filter((e) => PEAK_EVENTS.has(e.type));
}

/** The actions of the load management: shed, throttled, not following, grid charging denied. */
export function actionEvents(events: LmEvent[] = []): LmEvent[] {
  return events.filter((e) => !PEAK_EVENTS.has(e.type));
}

/** The text of an event; the type itself for one without text. */
export function eventText(e: LmEvent, t: Translate, f: Fmt): string {
  const k = (name: string, p?: Record<string, unknown>) => t(`lmoverview.event.${name}`, p);

  switch (e.type) {
    case "shed":
      return e.a > 0
        ? k("shedGuard", { load: e.load, power: f.w(e.b), minutes: Math.round(e.a) })
        : k("shed", { load: e.load, power: f.w(e.b) });
    case "throttled":
      return k("throttled", { load: e.load, requested: f.bare(e.a), allowed: f.w(e.b) });
    case "gridChargePaused":
      return k("gridChargePaused", { demand: f.w(e.a), limit: f.w(e.b) });
    case "gridChargeDenied":
      return k("gridChargeDenied", { allowed: f.w(e.a), wanted: f.w(e.b) });
    case "peak":
      return k("peak", { demand: f.w(e.a), limit: f.w(e.b) });
    case "notFollowing":
      return k("notFollowing", {
        load: e.load === "battery" ? t("lmoverview.battery") : e.load,
        power: f.w(e.a),
        allowed: f.w(e.b),
      });
    case "peakEnd":
      return k("peakEnd", { power: f.w(e.a), minutes: Math.max(1, Math.round(e.b / 60)) });
    case "reserveBelow":
      return k("reserveBelow", { reserve: f.pct(e.b) });
    case "reserveAbove":
      return k("reserveAbove", { reserve: f.pct(e.b) });
    case "batteryMode":
      return k("batteryMode", { mode: t(`${P}.mode.${e.load || "unknown"}`) });
    case "peakSwitch":
      return k(e.a ? "peakSwitchOn" : "peakSwitchOff");
    case "meter":
      return k(e.a ? "meterLost" : "meterBack");
    case "storage":
      return k(e.a ? "storageLost" : "storageBack");
    default:
      return String((e as LmEvent).type);
  }
}
