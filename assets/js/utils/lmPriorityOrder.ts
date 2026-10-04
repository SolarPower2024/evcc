// Custom extension: load management priorities from a dragged order, see
// components/Config/LmPrioritiesModal.vue. Numbered from the bottom: the last
// load gets 0, the one above 1 and so on, at most max (more loads share it). The
// home battery is not in the order: it always stands below with -1.

export type Priorities = Record<string, number>;

// orderPriorities returns the priorities for an order, top = highest
export function orderPriorities(order: string[], max = 10): Priorities {
  const n = order.length;
  return Object.fromEntries(order.map((name, i) => [name, Math.min(max, n - 1 - i)]));
}
