// Custom extension: the texts and numbers of the peak status dialog, see
// core/site_peak_status.go. The state comes finished from the backend, these
// functions only turn it into text and bar values. Pure, so they are tested
// without a component (peakStatus.test.ts).
import type { LmEvent, PeakState, PeakStatus } from "@/types/evcc-lm";

export type Translate = (key: string, params?: Record<string, unknown>) => string;
export type FmtPower = (watt: number) => string;

const P = "battery.peakStatus";

/** Color of the tile per state, evcc variables (light and dark). */
const TONES: Record<PeakState, string> = {
  off: "var(--evcc-gray)",
  storage: "var(--evcc-gray)",
  meter: "var(--evcc-orange)",
  gridCharge: "var(--evcc-darker-green)",
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

function params(st: PeakStatus, fmtW: FmtPower): Record<string, unknown> {
  return {
    soc: `${Math.round(st.soc)} %`,
    reserve: `${Math.round(st.reserve)} %`,
    limit: fmtW(st.limit),
    grid: fmtW(st.grid),
    house: fmtW(st.house),
    battery: fmtW(covering(st)),
    power: fmtW(st.chargePower ?? 0),
  };
}

/** The sentence of the tile. */
export function heroText(st: PeakStatus, t: Translate, fmtW: FmtPower): string {
  return t(`${P}.state.${st.state}.text`, params(st, fmtW));
}

/** The longer sentence under "what is happening". */
export function detailText(st: PeakStatus, t: Translate, fmtW: FmtPower): string {
  const p = params(st, fmtW);
  let text = t(`${P}.state.${st.state}.detail`, p);

  // the band of the integration: free again from the reserve plus 2 %
  if ((st.state === "saving" || st.state === "shaving") && st.freeAt) {
    text += " " + t(`${P}.freeAt`, { freeAt: `${Math.round(st.freeAt)} %` });
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
export function miniValues(st: PeakStatus, t: Translate, fmtW: FmtPower): MiniValue[] {
  const res: MiniValue[] = [
    {
      key: "soc",
      label: t(`${P}.mini.soc`),
      value: `${Math.round(st.soc)} %`,
      fill: unit(st.soc / 100),
      mark: unit(st.reserve / 100),
    },
    {
      key: "grid",
      label: t(`${P}.mini.grid`),
      value: fmtW(st.grid),
      fill: st.limit > 0 ? unit(st.grid / st.limit) : 0,
    },
  ];

  if (st.state === "shaving" || st.state === "holdShaving") {
    const power = covering(st);
    res.push({
      key: "battery",
      label: t(`${P}.mini.battery`),
      value: fmtW(power),
      fill: st.limit > 0 ? unit(power / st.limit) : 0,
    });
  } else {
    res.push({
      key: "quarter",
      label: t(`${P}.mini.quarter`),
      value: st.window ? fmtW(st.window.avg) : "–",
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
export function notes(st: PeakStatus, t: Translate, fmtW: FmtPower): Note[] {
  const mode = t(`${P}.mode.${st.batteryMode || "unknown"}`);

  const res: Note[] = [
    { text: t(`${P}.notes.mode`, { mode }), tone: "plain" },
    st.storageOk
      ? { text: t(`${P}.notes.connected`), tone: "ok" }
      : { text: t(`${P}.notes.notConnected`), tone: "bad" },
    st.limited
      ? { text: t(`${P}.notes.dischargeMax`, { power: fmtW(st.setpoint) }), tone: "plain" }
      : { text: t(`${P}.notes.dischargeFree`), tone: "plain" },
  ];

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

/** Event types shown in the dialog. */
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

export function peakEvents(events: LmEvent[] = []): LmEvent[] {
  return events.filter((e) => PEAK_EVENTS.has(e.type));
}

/** The text of the event types of the peak status; undefined for the others. */
export function eventText(e: LmEvent, t: Translate, fmtW: FmtPower): string | undefined {
  const k = (name: string, p?: Record<string, unknown>) => t(`lmoverview.event.${name}`, p);

  switch (e.type) {
    case "peakEnd":
      return k("peakEnd", { power: fmtW(e.a), minutes: Math.max(1, Math.round(e.b / 60)) });
    case "reserveBelow":
      return k("reserveBelow", { reserve: `${Math.round(e.b)} %` });
    case "reserveAbove":
      return k("reserveAbove", { reserve: `${Math.round(e.b)} %` });
    case "batteryMode":
      return k("batteryMode", { mode: t(`${P}.mode.${e.load || "unknown"}`) });
    case "peakSwitch":
      return k(e.a ? "peakSwitchOn" : "peakSwitchOff");
    case "meter":
      return k(e.a ? "meterLost" : "meterBack");
    case "storage":
      return k(e.a ? "storageLost" : "storageBack");
    default:
      return undefined;
  }
}
