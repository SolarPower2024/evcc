<template>
	<div v-if="note">
		<div class="form-check form-switch mt-2 mb-0 d-md-none">
			<input
				id="snowCoverRow"
				:checked="snowCover"
				class="form-check-input"
				type="checkbox"
				role="switch"
				data-testid="snowcover-switch-row"
				@change="change"
			/>
			<label class="form-check-label text-muted" for="snowCoverRow">
				{{ $t("snowCover.label") }}
				<span
					ref="coverHelp"
					v-bind="infoProps"
					data-testid="snowcover-help-row"
					@click.prevent
				>
					<shopicon-regular-info size="s"></shopicon-regular-info>
				</span>
			</label>
		</div>
		<div v-if="autoAvailable" class="form-check form-switch mt-2 mb-0">
			<input
				id="snowAuto"
				:checked="snowAuto"
				class="form-check-input"
				type="checkbox"
				role="switch"
				data-testid="snowauto-switch"
				@change="changeAuto"
			/>
			<label class="form-check-label text-muted" for="snowAuto">
				{{ $t("snowCover.autoLabel") }}
				<span ref="autoHelp" v-bind="infoProps" data-testid="snowauto-help" @click.prevent>
					<shopicon-regular-info size="s"></shopicon-regular-info>
				</span>
			</label>
		</div>
	</div>
	<div v-else class="form-check form-switch mb-0 text-nowrap d-none d-md-block">
		<input
			id="snowCover"
			:checked="snowCover"
			class="form-check-input"
			type="checkbox"
			role="switch"
			data-testid="snowcover-switch"
			@change="change"
		/>
		<label class="form-check-label text-muted" for="snowCover">
			{{ $t("snowCover.label") }}
			<span ref="coverHelp" v-bind="infoProps" data-testid="snowcover-help" @click.prevent>
				<shopicon-regular-info size="s"></shopicon-regular-info>
			</span>
		</label>
	</div>
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/info";
import { defineComponent, markRaw } from "vue";
import Tooltip from "bootstrap/js/dist/tooltip";
import api from "@/api";
import store from "@/store";

// Custom extension: switch "snow on pv" on the forecast page, see core/site_snow.go,
// and the switch to detect it automatically under the chart, see core/site_snow_auto.go.
// In the card header from md up; below that it sits under the chart, as the header
// has no room for it next to the title and evcc's switch. The help texts are
// tooltips on the info icons.
export default defineComponent({
	name: "SnowCoverSwitch",
	props: {
		note: Boolean,
	},
	data() {
		return { tooltips: {} as Record<string, Tooltip> };
	},
	computed: {
		snowCover() {
			return !!store.state?.snowCover;
		},
		// turned on by the detection from the weather
		snowCoverAuto() {
			return !!store.state?.snowCoverAuto;
		},
		snowAuto() {
			return !!store.state?.snowAuto;
		},
		// the detection needs the location of an Open-Meteo solar forecast
		autoAvailable() {
			return !!store.state?.snowAutoAvailable;
		},
		infoProps() {
			return { class: "info-icon text-muted ms-1", role: "button", tabindex: 0 };
		},
		coverHelp(): string {
			const help = this.$t("snowCover.help");
			return this.snowCover && this.snowCoverAuto
				? `${this.$t("snowCover.autoOn")} ${help}`
				: help;
		},
	},
	// the detection's icon only exists with an Open-Meteo solar forecast, so the
	// tooltips follow the icons rendered
	mounted() {
		this.syncTooltips();
	},
	updated() {
		this.syncTooltips();
	},
	beforeUnmount() {
		Object.values(this.tooltips).forEach((t) => t.dispose());
	},
	methods: {
		syncTooltips() {
			const titles: Record<string, () => string> = {
				coverHelp: () => this.coverHelp,
				autoHelp: () => this.$t("snowCover.autoHelp"),
			};
			for (const [ref, title] of Object.entries(titles)) {
				const el = this.$refs[ref] as Element | undefined;
				if (el && !this.tooltips[ref]) {
					this.tooltips[ref] = markRaw(new Tooltip(el, { title }));
				} else if (!el && this.tooltips[ref]) {
					this.tooltips[ref].dispose();
					delete this.tooltips[ref];
				}
			}
		},
		change(e: Event) {
			this.post(e, "snowcover", this.snowCover);
		},
		changeAuto(e: Event) {
			this.post(e, "snowauto", this.snowAuto);
		},
		// on failure the switch goes back to the state evcc has, the store did not change
		async post(e: Event, path: string, current: boolean) {
			const input = e.target as HTMLInputElement;
			try {
				await api.post(`${path}/${input.checked}`);
			} catch (err) {
				input.checked = current;
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
