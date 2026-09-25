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

		<hr class="my-3" />

		<div class="d-flex justify-content-between">
			<span class="text-muted">{{ $t("battery.peakShaving.windowAvg") }}</span>
			<span class="fw-bold" :class="{ 'text-danger': overLimit }">
				{{ windowAvgText }}
			</span>
		</div>
		<div v-if="enabled" class="d-flex justify-content-between mt-1">
			<span class="text-muted">{{ allowedLabel }}</span>
			<span class="fw-bold" data-testid="battery-peak-shaving-allowed">
				{{ allowedText }}
			</span>
		</div>
		<div class="d-flex justify-content-between mt-1">
			<span class="text-muted">{{ $t("battery.peakShaving.current") }}</span>
			<span class="fw-bold">{{ currentLabel }}</span>
		</div>
	</Card>
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/lightning";
import { defineComponent } from "vue";
import formatter from "@/mixins/formatter";
import api from "@/api";
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
		power: { type: Number, default: 0 },
		windowAvg: { type: Number, default: 0 },
		// grid power that keeps the window's average at the limit, until windowEnd
		allowed: { type: Number, default: 0 },
		windowEnd: { type: String, default: "" },
		// reported by the backend, not inferred from power: a setpoint can
		// legitimately equal the free-discharge value
		shaving: Boolean,
		entity: { type: String, default: "" },
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
		overLimit(): boolean {
			return this.windowAvg > this.limit;
		},
		windowAvgText(): string {
			return this.fmtW(this.windowAvg);
		},
		allowedLabel(): string {
			const time = this.windowEnd ? this.fmtHourMinute(new Date(this.windowEnd)) : "–";
			return this.$t("battery.peakShaving.allowed", { time });
		},
		allowedText(): string {
			return this.fmtW(this.allowed);
		},
		currentLabel(): string {
			if (!this.enabled) return "—";
			return this.shaving ? this.fmtW(this.power) : this.$t("battery.peakShaving.free");
		},
		limitOptions() {
			const options = [];
			for (let w = MAX_LIMIT; w >= MIN_LIMIT; w -= LIMIT_STEP) {
				options.push({ value: w, name: this.peakLimitText(w) });
			}
			return options;
		},
		reserveOptions() {
			const options = [];
			for (let i = 95; i >= 5; i -= 5) {
				options.push({ value: i, name: this.socText(i) });
			}
			return options;
		},
	},
	watch: {
		limit: {
			handler(v) {
				this.selectedLimit = v;
			},
			immediate: true,
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
