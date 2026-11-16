import { mount, config } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import PlanPowerHint from "./PlanPowerHint.vue";
import Preview from "./Preview.vue";

config.global.mocks["$i18n"] = { locale: "de-DE" };
config.global.mocks["$t"] = (a: any) => a;

describe("plan power hint", () => {
  test("shows nothing without a reason", () => {
    expect(mount(PlanPowerHint).find("[data-testid=plan-power-hint]").exists()).toBe(false);
    expect(
      mount(PlanPowerHint, { props: { reason: "" } })
        .find("[data-testid=plan-power-hint]")
        .exists()
    ).toBe(false);
    expect(
      mount(PlanPowerHint, { props: { reason: "other" } })
        .find("[data-testid=plan-power-hint]")
        .exists()
    ).toBe(false);
  });

  test("names the limit", () => {
    const peak = mount(PlanPowerHint, { props: { reason: "peak" } }).get(
      "[data-testid=plan-power-hint]"
    );
    expect(peak.text()).toBe("planPower.peak");
    expect(peak.attributes("title")).toBe("planPower.title");

    const circuit = mount(PlanPowerHint, { props: { reason: "circuit" } }).get(
      "[data-testid=plan-power-hint]"
    );
    expect(circuit.text()).toBe("planPower.circuit");
  });
});

describe("preview", () => {
  const props = { duration: 22000, power: 6500, plan: [], rates: [] };

  test("shows the hint next to the power only with a reason", () => {
    const limited = mount(Preview, { props: { ...props, powerReason: "peak" } });
    expect(limited.get("[data-testid=plan-power-hint]").text()).toBe("planPower.peak");

    const free = mount(Preview, { props });
    expect(free.find("[data-testid=plan-power-hint]").exists()).toBe(false);
  });
});
