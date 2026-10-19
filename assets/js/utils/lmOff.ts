import store from "@/store";

interface LmOffState {
  lmOff?: { enabled?: boolean };
}

// Custom extension: true while the load management (peak) is switched off, so the
// more button of the tab bar shows its warning dot (see MoreItem.vue). Only an
// explicit "off" counts, a missing lmOff means no load management.
export function lmOffBadge(state: LmOffState | undefined = store.state): boolean {
  return state?.lmOff?.enabled === false;
}
