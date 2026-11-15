import { describe, expect, test } from "vite-plus/test";
import { feedInAnswer, feedInRequest, isFeedInSection, loginMissing } from "./debugFeedIn";

describe("debugFeedIn", () => {
  test("a missing login is told before the request", () => {
    expect(loginMissing(false, false)).toBe(true);
    expect(loginMissing(true, false)).toBe(false);
    expect(loginMissing(false, true)).toBe(false);
  });

  test("a 200 is always a success", () => {
    expect(feedInAnswer(200)).toBe("ok");
    expect(feedInAnswer(204)).toBe("ok");
  });

  test("wrong password and errors", () => {
    expect(feedInAnswer(401)).toBe("password");
    expect(feedInAnswer(403)).toBe("password");
    expect(feedInAnswer(400)).toBe("error");
    expect(feedInAnswer(500)).toBe("error");
  });

  test("the day range sections", () => {
    expect(isFeedInSection("feedIn")).toBe(true);
    expect(isFeedInSection("feedInRecalc")).toBe(true);
    expect(isFeedInSection("events")).toBe(false);
  });

  test("preview reads, the second step changes", () => {
    expect(feedInRequest("feedIn", false)).toEqual({ method: "get", url: "/db/metrics/feedin" });
    expect(feedInRequest("feedIn", true)).toEqual({ method: "delete", url: "/db/metrics/feedin" });
    expect(feedInRequest("feedInRecalc", false)).toEqual({
      method: "get",
      url: "/db/metrics/feedinrecalc",
    });
    expect(feedInRequest("feedInRecalc", true)).toEqual({
      method: "post",
      url: "/db/metrics/feedinrecalc",
    });
  });
});
