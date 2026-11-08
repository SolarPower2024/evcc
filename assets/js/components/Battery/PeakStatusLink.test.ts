import { mount, config } from "@vue/test-utils";
import { beforeEach, describe, expect, test } from "vite-plus/test";
import store from "@/store";
import PeakStatusLink from "./PeakStatusLink.vue";
import en from "../../../../i18n/en.json";

config.global.mocks["$t"] = (key: string) =>
  key.split(".").reduce<any>((o, k) => o?.[k], en) ?? key;

const slot = '<div data-testid="soc">57 %</div>';

describe("PeakStatusLink", () => {
  beforeEach(() => {
    store.state.peakStatus = undefined;
  });

  test("without peak shaving the card is as in evcc: nothing around the content", () => {
    const wrapper = mount(PeakStatusLink, { slots: { default: slot } });

    expect(wrapper.html()).toBe(slot);
    expect(wrapper.find("[data-testid=peak-status-link]").exists()).toBe(false);
  });

  test("not set up (empty state): the same", () => {
    store.state.peakStatus = { state: "" } as any;
    const wrapper = mount(PeakStatusLink, { slots: { default: slot } });

    expect(wrapper.html()).toBe(slot);
  });

  test("set up: tappable with keyboard and label, the content stays", () => {
    store.state.peakStatus = { state: "off" } as any;
    const wrapper = mount(PeakStatusLink, { slots: { default: slot } });

    const link = wrapper.find("[data-testid=peak-status-link]");
    expect(link.exists()).toBe(true);
    expect(link.attributes("role")).toBe("button");
    expect(link.attributes("tabindex")).toBe("0");
    expect(link.attributes("aria-label")).toBe("Show peak status");
    expect(link.find("[data-testid=soc]").text()).toBe("57 %");
    expect(link.find("shopicon-regular-info").exists()).toBe(true);
  });
});
