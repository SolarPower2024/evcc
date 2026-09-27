import { describe, expect, test } from "vite-plus/test";
import { orderPriorities } from "./lmPriorityOrder";

describe("orderPriorities", () => {
  test("numbered from the bottom", () => {
    expect(orderPriorities(["h2", "h1", "h3", "bat", "wb"])).toEqual({
      h2: 4,
      h1: 3,
      h3: 2,
      bat: 1,
      wb: 0,
    });
  });

  test("a single load", () => {
    expect(orderPriorities(["a"])).toEqual({ a: 0 });
  });

  test("more loads than values share the highest", () => {
    const names = Array.from({ length: 12 }, (_, i) => `l${i}`);
    const res = orderPriorities(names);
    expect(res["l0"]).toBe(10);
    expect(res["l1"]).toBe(10);
    expect(res["l2"]).toBe(9);
    expect(res["l11"]).toBe(0);
  });
});
