<template>
	<div class="group round-box p-4">
		<p class="text-muted mb-3">{{ $t("config.peakshaving.description") }}</p>

		<label class="form-label" for="peakShavingEntity">
			{{ $t("config.peakshaving.entityLabel") }}
		</label>
		<input
			id="peakShavingEntity"
			v-model="entity"
			type="text"
			class="form-control"
			:class="{ 'is-invalid': error }"
			placeholder="input_number.battery_peak_power"
			data-testid="peakshaving-entity"
			@change="save"
		/>
		<div v-if="error" class="invalid-feedback d-block">{{ error }}</div>
		<div v-else-if="saved" class="form-text text-success">
			{{ $t("config.peakshaving.saved") }}
		</div>
		<div v-else class="form-text">{{ $t("config.peakshaving.entityHelp") }}</div>

		<label class="form-label mt-4" for="peakShavingChargePower">
			{{ $t("config.peakshaving.chargePowerLabel") }}
		</label>
		<div class="input-group">
			<input
				id="peakShavingChargePower"
				v-model="chargePower"
				type="number"
				min="0"
				step="100"
				class="form-control"
				:class="{ 'is-invalid': chargeError }"
				placeholder="0"
				data-testid="peakshaving-chargepower"
				@change="saveChargePower"
			/>
			<span class="input-group-text">W</span>
		</div>
		<div v-if="chargeError" class="invalid-feedback d-block">{{ chargeError }}</div>
		<div v-else class="form-text">{{ $t("config.peakshaving.chargePowerHelp") }}</div>

		<div class="mt-2" :class="chargeUnknown ? 'text-danger' : 'text-muted'">
			<strong>{{ $t("config.peakshaving.chargePowerEffective") }}</strong>
			{{ effectiveText }}
		</div>

		<p class="text-muted mt-4 mb-0 small">{{ $t("config.peakshaving.hint") }}</p>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import store from "@/store";
import api from "@/api";
import formatter from "@/mixins/formatter";

// The peak shaving target entity. The switch, the peak limit and the reserve soc
// are operating controls and live on the battery page instead.
export default defineComponent({
	name: "PeakShavingConfig",
	mixins: [formatter],
	data() {
		return {
			entity: "",
			error: "",
			saved: false,
			chargePower: 0,
			chargeError: "",
		};
	},
	computed: {
		configured(): string {
			return store.state.peakShavingEntity ?? "";
		},
		configuredChargePower(): number {
			return store.state.peakShavingChargePower ?? 0;
		},
		chargeUnknown(): boolean {
			return (store.state.peakShavingChargePowerEffective ?? 0) <= 0;
		},
		// spells out the value the grid charge gate actually works with, and
		// where it came from - it used to be invisible
		effectiveText(): string {
			if (this.chargeUnknown) {
				return this.$t("config.peakshaving.chargePowerUnknown");
			}
			const w = this.fmtW(store.state.peakShavingChargePowerEffective ?? 0);
			const source = this.$t(
				`config.peakshaving.source.${store.state.peakShavingChargePowerSource ?? "unknown"}`
			);
			return `${w} (${source})`;
		},
	},
	watch: {
		configured: {
			handler(v: string) {
				this.entity = v;
			},
			immediate: true,
		},
		configuredChargePower: {
			handler(v: number) {
				this.chargePower = v;
			},
			immediate: true,
		},
	},
	methods: {
		async save() {
			const value = this.entity.trim();
			this.error = "";
			this.saved = false;

			try {
				if (value) {
					await api.post(`peakshavingentity/${encodeURIComponent(value)}`);
				} else {
					await api.delete("peakshavingentity");
				}
				this.saved = true;
			} catch (err: any) {
				// the backend rejects a non-number entity and an unreachable
				// target, so surface that instead of keeping a dead setting
				this.error =
					err?.response?.data?.error || this.$t("config.peakshaving.entityInvalid");
				this.entity = this.configured;
				console.error(err);
			}
		},
		async saveChargePower() {
			const value = Math.max(0, Math.round(Number(this.chargePower) || 0));
			this.chargeError = "";

			try {
				await api.post(`peakshavingchargepower/${encodeURIComponent(value)}`);
				this.chargePower = value;
			} catch (err: any) {
				this.chargeError =
					err?.response?.data?.error || this.$t("config.peakshaving.chargePowerInvalid");
				this.chargePower = this.configuredChargePower;
				console.error(err);
			}
		},
	},
});
</script>
