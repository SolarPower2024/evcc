// Custom extension: what the peak shaving dialog shows besides the peak status:
// the loads of the load management with their state, and the title of the entry
// and the dialog. Pure, so they are tested without a component (peakDialog.test.ts).
import type { LmLoadStatus, LmState } from "@/types/evcc-lm";
import type { Fmt, Translate } from "./peakStatus";

type DialogState = Pick<LmState, "peakStatus" | "lmStatus"> & {
  circuits?: Record<string, { maxPower?: number; maxCurrent?: number }>;
};

/** Peak shaving is set up, also while off. */
export function hasPeakStatus(state: DialogState | undefined): boolean {
  return !!state?.peakStatus?.state;
}

/** The entry in the more menu and the dialog: peak shaving is set up or loads are in load management. */
export function dialogVisible(state: DialogState | undefined): boolean {
  return hasPeakStatus(state) || !!state?.lmStatus?.loads?.length;
}

/** Without peak shaving the dialog is the load management one. */
export function dialogTitleKey(state: DialogState | undefined): string {
  return hasPeakStatus(state) ? "battery.peakStatus.title" : "battery.peakStatus.titleLm";
}

/** The loads section: a circuit has a power or current limit and there are loads. */
export function loadsShown(state: DialogState | undefined): boolean {
  const limited = Object.values(state?.circuits || {}).some(
    (c) => (c.maxPower ?? 0) > 0 || (c.maxCurrent ?? 0) > 0
  );
  return limited && !!state?.lmStatus?.loads?.length;
}

/** The loads, highest priority first. */
export function sortedLoads(loads: LmLoadStatus[] = []): LmLoadStatus[] {
  return [...loads].sort((a, b) => b.priority - a.priority);
}

export type LoadTone = "success" | "warning" | "danger" | "info" | "muted";

const TONES: Record<LmLoadStatus["state"], LoadTone> = {
  running: "success",
  throttled: "warning",
  shed: "danger",
  waiting: "info",
  paused: "muted",
  off: "muted",
};

export function loadTone(l: LmLoadStatus): LoadTone {
  return TONES[l.state];
}

/** The state of a load as text; the power stands beside it. `until` formats a time. */
export function loadStateText(
  l: LmLoadStatus,
  t: Translate,
  f: Fmt,
  until: (iso: string) => string
): string {
  const d = (key: string, params?: Record<string, unknown>) =>
    t(`lmoverview.stateDetail.${key}`, params);
  const time = l.until ? until(l.until) : "–";

  switch (l.state) {
    case "running":
      return l.battery ? d("charging") : t("lmoverview.state.running");
    case "throttled":
      return d("throttled", { allowed: f.bare(l.allowed ?? 0), requested: f.w(l.requested ?? 0) });
    case "shed":
      return l.battery ? d("blocked", { time }) : d("shed", { time });
    case "waiting":
      return d("waiting", {
        requested: f.w(l.requested ?? 0),
        allowed: f.w(Math.max(0, l.allowed ?? 0)),
      });
    case "paused":
      return d("paused", { time });
  }

  return t("lmoverview.state.off");
}
