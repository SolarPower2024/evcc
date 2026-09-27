// Custom extension: load management priorities from a dragged order, top =
// highest, see components/Config/LmPrioritiesModal.vue. The priorities also rank
// solar surplus, the planner and the optimizer, so as few as possible change:
// the moved load takes a value between its new neighbours, closest to its old
// one. Only without room there do the loads above or below move along, keeping
// the gaps and ties they had.

export type Priorities = Record<string, number>;

// movedName returns the one entry that moved between two orders
export function movedName(before: string[], after: string[]): string | undefined {
  const without = (list: string[], name: string) => list.filter((n) => n !== name).join("\n");
  let res: string | undefined;
  let distance = 0;
  for (const name of after) {
    const d = Math.abs(after.indexOf(name) - before.indexOf(name));
    if (d > distance && without(before, name) === without(after, name)) {
      res = name;
      distance = d;
    }
  }
  return res;
}

// orderPriorities returns the priorities for the order after moving one entry
export function orderPriorities(
  order: string[],
  old: Priorities,
  moved: string,
  max = 10
): Priorities {
  const k = order.indexOf(moved);
  const n = order.length;
  if (k < 0 || n < 2) return { ...old };

  // required step from each entry to the next one below: 1 next to the moved
  // entry, else as before (0 keeps a tie)
  const gap = order
    .slice(0, -1)
    .map((name, j) => (j === k || j + 1 === k ? 1 : old[name]! > old[order[j + 1]!]! ? 1 : 0));
  const above = k > 0 ? old[order[k - 1]!]! : undefined;
  const below = k < n - 1 ? old[order[k + 1]!]! : undefined;
  const lo = below === undefined ? 0 : below + 1;
  const hi = above === undefined ? max : above - 1;

  const start = () => order.map((name) => old[name]!);
  const result = (w: number[]) => Object.fromEntries(order.map((name, i) => [name, w[i]!]));

  if (lo <= hi) {
    const w = start();
    w[k] = Math.min(hi, Math.max(lo, old[moved]!));
    return result(w);
  }

  // no room: raise the loads above, or else lower the loads below
  const up = start();
  up[k] = lo;
  for (let j = k - 1; j >= 0; j--) up[j] = Math.max(up[j]!, up[j + 1]! + gap[j]!);

  const down = start();
  down[k] = hi;
  for (let j = k + 1; j < n; j++) down[j] = Math.min(down[j]!, down[j - 1]! - gap[j - 1]!);

  const valid = (w: number[]) => w.every((v) => v >= 0 && v <= max);
  const changes = (w: number[]) => order.filter((name, i) => w[i] !== old[name]).length;
  const candidates = [up, down].filter(valid).sort((a, b) => changes(a) - changes(b));
  if (candidates.length) return result(candidates[0]!);

  // more steps than values: renumber from the bottom, ties at the top
  const w: number[] = Array.from({ length: n }, () => 0);
  for (let j = n - 2; j >= 0; j--) w[j] = Math.min(max, w[j + 1]! + gap[j]!);
  return result(w);
}
