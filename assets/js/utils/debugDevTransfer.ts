// The exchange of the test version with evcc custom in Konfiguration → System →
// Debug (DebugDevTransfer.vue), see core/devtransfer.
import type { DevTransferItem, DevTransferStatus } from "@/types/evcc-lm";

/** The items in the order of the checkboxes. */
export const DEV_TRANSFER_ITEMS: DevTransferItem[] = [
  "sessions",
  "energy",
  "peak",
  "devices",
  "settings",
];

/** What the give back starts with: the recording of the test time. */
export const DEV_TRANSFER_DEFAULT: DevTransferItem[] = ["sessions", "energy", "peak"];

/** The items as the query of the api takes them, in the order of the checkboxes. */
export function itemsQuery(selected: DevTransferItem[]): string {
  return DEV_TRANSFER_ITEMS.filter((i) => selected.includes(i)).join(",");
}

/** Devices without settings or settings without devices can leave references broken. */
export function onlyOne(selected: DevTransferItem[]): boolean {
  return selected.includes("devices") !== selected.includes("settings");
}

/** The take over needs the real database. */
export function canTakeover(s: DevTransferStatus): boolean {
  return s.enabled && !s.notFound;
}

/** The give back needs the real database, data taken over before and a selection. */
export function canGiveBack(s: DevTransferStatus, selected: DevTransferItem[]): boolean {
  return canTakeover(s) && !!s.since && selected.length > 0;
}
