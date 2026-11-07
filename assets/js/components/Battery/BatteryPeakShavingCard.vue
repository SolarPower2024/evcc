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
					<a
						href="#"
						class="limit-link text-decoration-underline fw-bold text-nowrap"
						data-testid="battery-peak-shaving-limit"
						@click.prevent="openLimit"
						>{{ limitText }}</a
					>
				</template>
			</i18n-t>
		</div>

		<p
			v-if="optimizerAutomatic"
			class="small text-muted mt-3 mb-0"
			data-testid="battery-peak-shaving-optimizer"
		>
			{{ $t("battery.peakShaving.optimizer") }}
		</p>

		<div v-if="error" class="alert alert-danger mt-3 mb-0 py-2 small">{{ error }}</div>

		<div
			v-if="enabled && !limit"
			class="alert alert-warning mt-3 mb-0 py-2 small"
			data-testid="battery-peak-shaving-no-limit"
		>
			{{ $t("battery.peakShaving.noLimit") }}
		</div>

		<div
			v-if="entitiesMissing"
			class="alert alert-warning mt-3 mb-0 py-2 small"
			data-testid="battery-peak-shaving-missing"
		>
			{{ $t("battery.peakShaving.noEntities") }}
		</div>
	</Card>
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/lightning";
import { defineComponent } from "vue";
import formatter from "@/mixins/formatter";
import api from "@/api";
import store from "@/store";
import { socSteps, MARSTEK_MIN_RESERVE } from "@/utils/socSteps";
import { peakShavingSetUp } from "@/utils/peakShaving";
import { optimizerAutomaticOn } from "@/utils/optimizerAutomatic";
import { openModal } from "@/configModal";
import Card from "../Helper/Card.vue";
import InlineSocSelect from "./InlineSocSelect.vue";

// Peak shaving: hold the battery's lower soc range back and spend it only on
// grid demand above the peak limit, which is what a demand charge is billed on.
// The limit is evcc's grid import peak limit, set in its dialog, see core/site_peak_limit.go.
//
// Careful with method names here: the formatter mixin carries a `fmtLimit` data
// property, and data shadows methods on the instance. A method of that name is
// silently replaced by the number and only blows up at render time, taking the
// whole <i18n-t> subtree - the select included - down with it.
export default defineComponent({
	name: "BatteryPeakShavingCard",
	components: { Card, InlineSocSelect },
	mixins: [formatter],
	props: {
		enabled: Boolean,
		limit: { type: Number, default: 0 },
		reserve: { type: Number, default: 30 },
		// reported by the backend, not inferred from power: a setpoint can
		// legitimately equal the free-discharge value
		shaving: Boolean,
	},
	data() {
		return {
			selectedReserve: 30,
			error: "",
		};
	},
	computed: {
		// the battery covers peaks even when the optimizer holds it, see core/site_optimizer_gate.go
		optimizerAutomatic(): boolean {
			return optimizerAutomaticOn(store.state?.optimizerAutomatic);
		},
		subtitle(): string {
			if (!this.enabled) return this.$t("battery.peakShaving.off");
			return this.$t(
				this.shaving ? "battery.peakShaving.shaving" : "battery.peakShaving.normal"
			);
		},
		marstek(): boolean {
			return store.state?.peakShavingBatteryType === "marstek";
		},
		// the controller of the battery type needs its three entities
		entitiesMissing(): boolean {
			return !peakShavingSetUp(store.state);
		},
		// shown in kW, stored in watts
		limitText(): string {
			return this.limit > 0 ? `${this.fmtNumber(this.limit / 1000, 1)} kW` : "– kW";
		},
		// a Marstek gets 5 % steps down to 20 %, the lowest threshold of Omnibattery's
		// peak shaving, see utils/socSteps.ts
		reserveOptions() {
			return socSteps(95, 5, this.marstek, this.selectedReserve, MARSTEK_MIN_RESERVE).map(
				(i) => ({
					value: i,
					name: this.socText(i),
				})
			);
		},
	},
	watch: {
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
		// evcc's dialog on the configuration page, closing it comes back here
		async openLimit() {
			const router = this.$router;
			const from = router.currentRoute.value.fullPath;
			await openModal("hems", { type: "static" });
			if (router.currentRoute.value.path === "/config") {
				router.push(from);
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
		apiError(err: any): string {
			return err?.response?.data?.error || err?.message || "error";
		},
	},
});
</script>

<style scoped>
.limit-link {
	color: inherit;
}
</style>
