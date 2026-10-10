import { describe, expect, test } from "vite-plus/test";
import { socSteps } from "./socSteps";

describe("socSteps", () => {
  test("5 % steps", () => {
    expect(socSteps(30, 0, false)).toEqual([30, 25, 20, 15, 10, 5, 0]);
  });

  test("Marstek: 1 % steps from 15 % down to 11 %, nothing below", () => {
    expect(socSteps(30, 0, true)).toEqual([30, 25, 20, 15, 14, 13, 12, 11]);
    expect(socSteps(95, 5, true).slice(-6)).toEqual([20, 15, 14, 13, 12, 11]);
  });

  test("Marstek within the range", () => {
    expect(socSteps(100, 20, true)).toEqual([
      100, 95, 90, 85, 80, 75, 70, 65, 60, 55, 50, 45, 40, 35, 30, 25, 20,
    ]);
    expect(socSteps(13, 0, true)).toEqual([13, 12, 11]);
  });

  test("a value set keeps its place", () => {
    expect(socSteps(30, 0, false, 12)).toEqual([30, 25, 20, 15, 12, 10, 5, 0]);
    expect(socSteps(30, 0, true, 8)).toEqual([30, 25, 20, 15, 14, 13, 12, 11, 8]);
    expect(socSteps(30, 0, true, 20)).toEqual([30, 25, 20, 15, 14, 13, 12, 11]);
  });
});
