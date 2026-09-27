import { describe, expect, test } from "vite-plus/test";
import { movedName, orderPriorities } from "./lmPriorityOrder";

// heaters 4, 3, 2, battery 1, wallbox 0
const old = { h1: 4, h3: 3, h2: 2, bat: 1, wb: 0 };
const move = (order: string[], prios: Record<string, number> = old) =>
  orderPriorities(order, prios, movedName(Object.keys(prios), order)!);

describe("movedName", () => {
  test("finds the dragged entry", () => {
    expect(movedName(["a", "b", "c", "d"], ["d", "a", "b", "c"])).toBe("d");
    expect(movedName(["a", "b", "c", "d"], ["b", "c", "d", "a"])).toBe("a");
    expect(movedName(["a", "b", "c"], ["a", "b", "c"])).toBeUndefined();
  });
});

describe("orderPriorities", () => {
  test("to the top: only the moved one changes", () => {
    expect(move(["wb", "h1", "h3", "h2", "bat"])).toEqual({ ...old, wb: 5 });
  });

  test("keeps the old value when it fits", () => {
    const prios = { a: 9, b: 5, c: 1 };
    expect(move(["b", "a", "c"], prios)).toEqual({ a: 9, b: 10, c: 1 });
    // a swap of neighbours counts as the lower one moved up
    expect(move(["a", "c", "b"], prios)).toEqual({ a: 9, b: 5, c: 6 });
    expect(orderPriorities(["x", "y", "z"], { x: 9, y: 6, z: 1 }, "y")).toEqual({
      x: 9,
      y: 6,
      z: 1,
    });
  });

  test("between two neighbours with room", () => {
    const prios = { a: 10, b: 7, c: 3, d: 0 };
    // d between a and b
    expect(move(["a", "d", "b", "c"], prios)).toEqual({ a: 10, b: 7, c: 3, d: 8 });
  });

  test("no room: the loads above move along", () => {
    // heater 1 to the bottom
    expect(move(["h3", "h2", "bat", "wb", "h1"])).toEqual({
      h3: 4,
      h2: 3,
      bat: 2,
      wb: 1,
      h1: 0,
    });
  });

  test("no room at the top: the loads below move", () => {
    const prios = { a: 10, b: 9, c: 8 };
    expect(move(["c", "a", "b"], prios)).toEqual({ a: 9, b: 8, c: 10 });
  });

  test("ties stay ties", () => {
    const prios = { a: 5, b: 3, c: 3, d: 2 };
    expect(move(["d", "a", "b", "c"], prios)).toEqual({
      a: 5,
      b: 3,
      c: 3,
      d: 6,
    });
    // a below the tie: fewer changes when the load below moves down
    expect(move(["b", "c", "a", "d"], prios)).toEqual({ a: 2, b: 3, c: 3, d: 1 });
  });

  test("more loads than values", () => {
    const names = Array.from({ length: 12 }, (_, i) => `l${i}`);
    const prios = Object.fromEntries(names.map((n, i) => [n, Math.max(0, 10 - i)]));
    const order = [...names.slice(1), names[0]!];
    const res = orderPriorities(order, prios, "l0");
    const values = order.map((n) => res[n]!);
    expect(values.every((v, i) => i === 0 || v <= values[i - 1]!)).toBe(true);
    expect(Math.min(...values)).toBe(0);
    expect(Math.max(...values)).toBe(10);
  });
});
