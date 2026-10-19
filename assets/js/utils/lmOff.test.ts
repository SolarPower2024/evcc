import { describe, expect, test } from "vite-plus/test";
import { lmOffBadge } from "./lmOff";

describe("lmOffBadge", () => {
  test("off shows the badge", () => {
    expect(lmOffBadge({ lmOff: { enabled: false } })).toBe(true);
  });
  test("on, missing or undefined do not", () => {
    expect(lmOffBadge({ lmOff: { enabled: true } })).toBe(false);
    expect(lmOffBadge({ lmOff: {} })).toBe(false);
    expect(lmOffBadge({})).toBe(false);
    expect(lmOffBadge(undefined)).toBe(false);
  });
});
