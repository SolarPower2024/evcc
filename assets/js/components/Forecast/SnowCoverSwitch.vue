<template>
	<div class="snow mt-2" data-testid="snow-mode">
		<i18n-t keypath="snowCover.sentence" tag="span" scope="global">
			<template #mode>
				<CustomSelect
					id="snowMode"
					:options="modeOptions"
					:selected="mode"
					:aria-label="$t('snowCover.label')"
					data-testid="snow-mode-select"
					inline
					@change="change"
				>
					<span class="text-decoration-underline fw-bold text-nowrap">{{
						modeText
					}}</span>
				</CustomSelect>
			</template>
		</i18n-t>
		<span v-if="detected" class="text-primary small ms-1" data-testid="snow-detected">
			· {{ $t("snowCover.detected") }}
		</span>
		<span ref="help" v-bind="infoProps" data-testid="snow-help" @click.prevent>
			<shopicon-regular-info size="s"></shopicon-regular-info>
		</span>
		<div
			v-if="error"
			class="alert alert-danger mt-3 mb-0 py-2 small w-100"
			data-testid="snowcover-error"
		>
			{{ error }}
		</div>
	</div>
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/info";
import { defineComponent } from "vue";
import Tooltip from "bootstrap/js/dist/tooltip";
import api from "@/api";
import store from "@/store";
import CustomSelect from "../Helper/CustomSelect.vue";

type SnowMode = "off" | "auto" | "on";

// Custom extension: snow on pv as one choice in a sentence under the solar chart,
// like the battery page: off, automatic (the detection from the weather, see
// core/site_snow_auto.go) or on by hand (see core/site_snow.go). The two stored
// switches stay as they are; the choice sets both.
export default defineComponent({
	name: "SnowCoverSwitch",
	components: { CustomSelect },
	data() {
		return { tooltip: null as Tooltip | null, error: "" };
	},
	computed: {
		snowCover() {
			return !!store.state?.snowCover;
		},
		snowAuto() {
			return !!store.state?.snowAuto;
		},
		// the detection needs the location of an Open-Meteo solar forecast
		autoAvailable() {
			return !!store.state?.snowAutoAvailable;
		},
		mode(): SnowMode {
			if (this.snowAuto) return "auto";
			return this.snowCover ? "on" : "off";
		},
		// automatic and snow found right now
		detected(): boolean {
			return this.snowAuto && this.snowCover;
		},
		modeOptions() {
			const modes: SnowMode[] =
				this.autoAvailable || this.snowAuto ? ["off", "auto", "on"] : ["off", "on"];
			return modes.map((value) => ({ value, name: this.$t(`snowCover.mode.${value}`) }));
		},
		modeText(): string {
			return this.$t(`snowCover.mode.${this.mode}`);
		},
		infoProps() {
			return { class: "info-icon text-muted ms-1", role: "button", tabindex: 0 };
		},
		helpText(): string {
			const help = this.$t("snowCover.help");
			return this.autoAvailable ? `${this.$t("snowCover.autoHelp")} ${help}` : help;
		},
	},
	mounted() {
		const el = this.$refs["help"] as Element | undefined;
		// not markRaw: its read-only __v_skip breaks Tooltip.dispose(), which nulls every own property
		if (el) this.tooltip = new Tooltip(el, { title: () => this.helpText });
	},
	beforeUnmount() {
		this.tooltip?.dispose();
	},
	methods: {
		// off: detection off, snow off; automatic: detection on, it decides; on: detection
		// off, snow on. On failure the choice shows the state evcc has again.
		async change(e: Event) {
			const select = e.target as HTMLSelectElement;
			const next = select.value as SnowMode;
			this.error = "";
			try {
				if (next === "auto") {
					await api.post("snowauto/true");
				} else {
					await api.post("snowauto/false");
					await api.post(`snowcover/${next === "on"}`);
				}
			} catch (err: any) {
				select.value = this.mode;
				this.error = err?.response?.data?.error || err?.message || "error";
				console.error(err);
			}
		},
	},
});
</script>

<style scoped>
.info-icon {
	cursor: help;
	display: inline-block;
	vertical-align: middle;
	line-height: 1;
}
</style>
