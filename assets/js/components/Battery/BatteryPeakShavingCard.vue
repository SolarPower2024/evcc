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
						:label="fmtSoc(selectedReserve)"
						nowrap
						@change="changeReserve"
					/>
				</template>
				<template #limit>
					<InlineSocSelect
						id="batteryPeakShavingLimit"
						:options="limitOptions"
						:selected="selectedLimit"
						:label="fmtLimit(selectedLimit)"
						nowrap
						@change="changeLimit"
					/>
				</template>
			</i18n-t>
		</div>

		<hr class="my-3" />

		<div class="d-flex justify-content-between">
			<span class="text-muted">{{ $t("battery.peakShaving.windowAvg") }}</span>
			<span class="fw-bold" :class="{ 'text-danger': overLimit }">
				{{ fmtWatt(windowAvg) }}
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

// peak limit range, set and shown in kW while everything else stays in watts
const minLimit = 2000;
const maxLimit = 20000;
const step = 500;

// Peak shaving: hold the battery's lower soc range back and spend it only on
// grid demand above the peak limit, which is what a demand charge is billed on.
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
		freeValue: { type: Number, default: 10000 },
	},
	data() {
		return {
			selectedLimit: 5000,
			selectedReserve: 30,
		};
	},
	computed: {
		subtitle(): string {
			if (!this.enabled) return this.$t("battery.peakShaving.off");
			return this.$t(
				this.shaving ? "battery.peakShaving.shaving" : "battery.peakShaving.normal"
			);
		},
		// the free value signals unrestricted discharge, anything else is a setpoint
		shaving(): boolean {
			return this.power !== this.freeValue;
		},
		overLimit(): boolean {
			return this.windowAvg > this.limit;
		},
		currentLabel(): string {
			if (!this.enabled) return "—";
			return this.shaving ? this.fmtWatt(this.power) : this.$t("battery.peakShaving.free");
		},
		// 2 to 20 kW in 0.5 kW steps
		limitOptions() {
			const options = [];
			for (let w = maxLimit; w >= minLimit; w -= step) {
				options.push({ value: w, name: this.fmtLimit(w) });
			}
			return options;
		},
		reserveOptions() {
			const options = [];
			for (let i = 95; i >= 5; i -= 5) {
				options.push({ value: i, name: this.fmtSoc(i) });
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
			try {
				await api.post(`peakshaving/${target.checked}`);
			} catch (err) {
				target.checked = this.enabled; // revert to stay in sync with state
				console.error(err);
			}
		},
		async changeLimit($event: Event) {
			const w = parseInt(($event.target as HTMLInputElement).value, 10);
			const previous = this.selectedLimit;
			this.selectedLimit = w;
			try {
				await api.post(`peakshavinglimit/${encodeURIComponent(w)}`);
			} catch (err) {
				this.selectedLimit = previous;
				console.error(err);
			}
		},
		async changeReserve($event: Event) {
			const soc = parseInt(($event.target as HTMLInputElement).value, 10);
			const previous = this.selectedReserve;
			this.selectedReserve = soc;
			try {
				await api.post(`peakshavingreserve/${encodeURIComponent(soc)}`);
			} catch (err) {
				this.selectedReserve = previous;
				console.error(err);
			}
		},
		fmtSoc(soc: number) {
			return this.fmtPercentage(soc);
		},
		// the limit is set and shown in kW, everything else stays in watts
		fmtLimit(w: number) {
			return `${this.fmtNumber(w / 1000, 1)} kW`;
		},
		fmtWatt(w: number) {
			return this.fmtW(w);
		},
	},
});
</script>
