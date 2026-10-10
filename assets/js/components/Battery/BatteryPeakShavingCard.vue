<template>
	<Card :title="$t('batterySettings.peakShavingTab')" :subtitle="subtitle">
		<div class="form-check form-switch mb-3">
			<input
				id="batteryPeakShaving"
				:checked="enabled"
				class="form-check-input"
				type="checkbox"
				role="switch"
				data-testid="battery-peak-shaving-switch"
				@change="changeEnabled"
			/>
			<label class="form-check-label" for="batteryPeakShaving">
				{{ $t("battery.peakShaving.enable") }}
			</label>
		</div>

		<div class="d-flex gap-3">
			<shopicon-regular-lightning
				size="s"
				class="text-primary flex-shrink-0 mt-1"
			></shopicon-regular-lightning>
			<i18n-t keypath="battery.peakShaving.description" tag="p" class="mb-0" scope="global">
				<template #reserve>
					<InlineSocSelect
						id="batteryPeakShavingReserve"
						:options="reserveOptions"
						:selected="selectedReserve"
						:label="socText(selectedReserve)"
						nowrap
						@change="changeReserve"
					/>
				</template>
				<template #limit>
					<InlineSocSelect
						id="batteryPeakShavingLimit"
						:options="limitOptions"
						:selected="selectedLimit"
						:label="peakLimitText(selectedLimit)"
						nowrap
						@change="changeLimit"
					/>
				</template>
			</i18n-t>
		</div>

		<div v-if="error" class="alert alert-danger mt-3 mb-0 py-2 small">{{ error }}</div>

		<div v-if="!entity" class="alert alert-warning mt-3 mb-0 py-2 small">
			{{ $t("battery.peakShaving.noEntity") }}
		</div>
	</Card>
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/lightning";
import { defineComponent, type PropType } from "vue";
import type { PeakFollow } from "@/types/evcc";
import formatter from "@/mixins/formatter";
import api from "@/api";
import store from "@/store";
import { socSteps } from "@/utils/socSteps";
import Card from "../Helper/Card.vue";
import InlineSocSelect from "./InlineSocSelect.vue";

// peak limit range, picked in 0.5 kW steps but sent and stored in watts
const MIN_LIMIT = 2000;
const MAX_LIMIT = 20000;
const LIMIT_STEP = 500;

// Peak shaving: hold the battery's lower soc range back and spend it only on
// grid demand above the peak limit, which is what a demand charge is billed on.
//
// Careful with method names here: the formatter mixin carries a `fmtLimit` data
// property, and data shadows methods on the instance. A method of that name is
// silently replaced by the number and only blows up at render time, taking the
// whole <i18n-t> subtree - both selects included - down with it.
export default defineComponent({
	name: "BatteryPeakShavingCard",
	components: { Card, InlineSocSelect },
	mixins: [formatter],
	props: {
		enabled: Boolean,
		limit: { type: Number, default: 5000 },
		reserve: { type: Number, default: 30 },
		// reported by the backend, not inferred from power: a setpoint can
		// legitimately equal the free-discharge value
		shaving: Boolean,
		entity: { type: String, default: "" },
		// follow the peak: { enabled, buffer, base }, see core/site_peak_follow.go
		follow: { type: Object as PropType<PeakFollow>, default: undefined },
	},
	data() {
		return {
			selectedLimit: 5000,
			selectedReserve: 30,
			error: "",
		};
	},
	computed: {
		subtitle(): string {
			if (!this.enabled) return this.$t("battery.peakShaving.off");
			return this.$t(
				this.shaving ? "battery.peakShaving.shaving" : "battery.peakShaving.normal"
			);
		},
		followOn(): boolean {
			return !!this.follow?.enabled;
		},
		// the limit set by hand: the base while following the peak
		handLimit(): number {
			return this.followOn && this.follow?.base ? this.follow.base : this.limit;
		},
		limitOptions() {
			const options = [];
			for (let w = MAX_LIMIT; w >= MIN_LIMIT; w -= LIMIT_STEP) {
				options.push({ value: w, name: this.peakLimitText(w) });
			}
			return options;
		},
		// a Marstek gets 1 % steps from 15 % down to its minimum, see utils/socSteps.ts
		reserveOptions() {
			const marstek = store.state?.peakShavingBatteryType === "marstek";
			return socSteps(95, 5, marstek, this.selectedReserve).map((i) => ({
				value: i,
				name: this.socText(i),
			}));
		},
	},
	watch: {
		limit: {
			handler() {
				this.selectedLimit = this.handLimit;
			},
			immediate: true,
		},
		follow: {
			handler() {
				this.selectedLimit = this.handLimit;
			},
			deep: true,
		},
		reserve: {
			handler(v) {
				this.selectedReserve = v;
			},
			immediate: true,
		},
	},
	methods: {
		async changeEnabled(e: Event) {
			const target = e.target as HTMLInputElement;
			this.error = "";
			try {
				await api.post(`peakshaving/${target.checked}`);
			} catch (err: any) {
				target.checked = this.enabled; // revert to stay in sync with state
				this.error = this.apiError(err);
				console.error(err);
			}
		},
		async changeLimit($event: Event) {
			const value = parseInt(($event.target as HTMLInputElement).value, 10);
			const previous = this.selectedLimit;
			this.selectedLimit = value;
			this.error = "";

			try {
				await api.post(`peakshavinglimit/${encodeURIComponent(value)}`);
			} catch (err: any) {
				this.selectedLimit = previous;
				this.error = this.apiError(err);
				console.error(err);
			}
		},
		async changeReserve($event: Event) {
			const value = parseInt(($event.target as HTMLInputElement).value, 10);
			const previous = this.selectedReserve;
			this.selectedReserve = value;
			this.error = "";

			try {
				await api.post(`peakshavingreserve/${encodeURIComponent(value)}`);
			} catch (err: any) {
				this.selectedReserve = previous;
				this.error = this.apiError(err);
				console.error(err);
			}
		},
		socText(soc: number): string {
			return this.fmtPercentage(soc);
		},
		// shown in kW, stored in watts
		peakLimitText(watt: number): string {
			return `${this.fmtNumber(watt / 1000, 1)} kW`;
		},
		apiError(err: any): string {
			return err?.response?.data?.error || err?.message || "error";
		},
	},
});
</script>
