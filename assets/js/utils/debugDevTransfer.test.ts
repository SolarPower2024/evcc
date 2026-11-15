import { describe, expect, test } from "vite-plus/test";
import {
  DEV_TRANSFER_DEFAULT,
  canGiveBack,
  canTakeover,
  itemsQuery,
  onlyOne,
} from "./debugDevTransfer";

describe("debugDevTransfer", () => {
  test("the query keeps the order of the checkboxes", () => {
    expect(itemsQuery(["settings", "sessions", "peak"])).toBe("sessions,peak,settings");
    expect(itemsQuery(DEV_TRANSFER_DEFAULT)).toBe("sessions,energy,peak");
    expect(itemsQuery([])).toBe("");
  });

  test("only devices or only settings warns", () => {
    expect(onlyOne(["devices"])).toBe(true);
    expect(onlyOne(["settings", "sessions"])).toBe(true);
    expect(onlyOne(["devices", "settings"])).toBe(false);
    expect(onlyOne(DEV_TRANSFER_DEFAULT)).toBe(false);
  });

  test("the buttons are grey without the file or without a take over", () => {
    expect(canTakeover({ enabled: true })).toBe(true);
    expect(canTakeover({ enabled: true, notFound: true })).toBe(false);
    expect(canTakeover({ enabled: false })).toBe(false);

    const items = DEV_TRANSFER_DEFAULT;
    expect(canGiveBack({ enabled: true }, items)).toBe(false);
    expect(canGiveBack({ enabled: true, since: "2026-11-14T18:00:00Z" }, items)).toBe(true);
    expect(
      canGiveBack({ enabled: true, since: "2026-11-14T18:00:00Z", notFound: true }, items)
    ).toBe(false);
    expect(canGiveBack({ enabled: true, since: "2026-11-14T18:00:00Z" }, [])).toBe(false);
  });
});
