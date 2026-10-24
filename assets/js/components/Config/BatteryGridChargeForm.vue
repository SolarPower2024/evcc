<template>
	<form class="container mx-0 px-0" data-testid="gridcharge-form" @submit.prevent="save">
		<p>{{ $t("config.gridcharge.description") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>

		<!-- custom: what grid charging goes by, see core/site_lm_grid_mode.go -->
		<FormRow
			id="gridChargeMode"
			:label="$t('config.batterysettings.gridChargeMode')"
			:help="$t('config.batterysettings.gridChargeModeHelp')"
		>
			<select
				id="gridChargeMode"
				v-model="mode"
				class="form-select"
				data-testid="gridcharge-mode"
			>
				<option v-for="o in modeOptions" :key="o.value" :value="o.value">
					{{ $t(`config.batterysettings.mode.${o.value}`) }}
				</option>
			</select>
		</FormRow>
		<p v-if="priceIgnored" class="small text-muted" data-testid="gridcharge-optimizer">
			{{ $t("config.batterysettings.gridChargeModeOptimizer") }}
		</p>

		<FormRow
			id="gridChargeEntity"
			:label="$t('config.gridcharge.entityLabel')"
			:help="$t('config.gridcharge.entityHelp')"
			optional
		>
			<input
				id="gridChargeEntity"
				v-model="entity"
				type="text"
				class="form-control"
				placeholder="input_number.battery_charge_power"
				data-testid="gridcharge-entity"
			/>
		</FormRow>
		<!-- custom: Omnibattery is switched to manual control through it, see core/site_peak_omni.go -->
		<p v-if="marstek" class="small text-muted" data-testid="gridcharge-marstek">
			{{ $t("config.gridcharge.marstekHint") }}
		</p>

		<FormRow
			id="gridChargePower"
			:label="$t('config.gridcharge.chargePowerLabel')"
			:help="$t('config.gridcharge.chargePowerHelp')"
			optional
		>
			<div class="input-group">
				<input
					id="gridChargePower"
					v-model="chargePower"
					type="number"
					min="0"
					step="1"
					class="form-control"
					placeholder="0"
					data-testid="gridcharge-power"
				/>
				<span class="input-group-text">W</span>
			</div>
		</FormRow>

		<!-- the values the grid charge logic actually works with -->
		<p class="mb-1" :class="chargeUnknown ? 'text-danger' : 'text-muted'">
			<strong>{{ $t("config.gridcharge.chargePowerEffective") }}</strong>
			{{ effectiveText }}
		</p>
		<p v-if="dynamic" class="mb-0 text-muted">
			<strong>{{ $t("config.gridcharge.setpoint") }}</strong>
			{{ setpointText }}
		</p>

		<div class="mt-4 d-flex justify-content-end">
			<button
				type="submit"
				class="btn btn-primary flex-grow-1 flex-sm-grow-0 px-4"
				data-testid="gridcharge-save"
				:disabled="saving || !changed"
			>
				<span
					v-if="saving"
					class="spinner-border spinner-border-sm"
					role="status"
					aria-hidden="true"
				></span>
				{{ $t("config.general.save") }}
			</button>
		</div>
	</form>
</template>

<script>
import FormRow from "./FormRow.vue";
import store from "@/store";
import api from "@/api";
import formatter from "@/mixins/formatter";
import { gridChargeMode, gridChargeModeOptions } from "@/utils/gridChargeMode";
import { priceLimitIgnored } from "@/utils/optimizerAutomatic";

// How the home battery charges from the grid: with a charge power entity evcc
// sizes the power to the peak limit and the circuit, without one charging is
// switched on and off at the expected charge power. The mode decides whether it
// goes by soc, by price or both.
export default {
	name: "BatteryGridChargeForm",
	components: { FormRow },
	mixins: [formatter],
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			entity: "",
			chargePower: 0,
			mode: "both",
			initialEntity: "",
			initialChargePower: 0,
			initialMode: "both",
		};
	},
	computed: {
		changed() {
			return (
				this.entity.trim() !== this.initialEntity ||
				this.normalizedChargePower !== this.initialChargePower ||
				this.mode !== this.initialMode
			);
		},
		normalizedChargePower() {
			return Math.max(0, Math.round(Number(this.chargePower) || 0));
		},
		// price needs a dynamic tariff; a stored price stays selectable
		modeOptions() {
			return gridChargeModeOptions(!!store.state?.smartCostAvailable, this.initialMode);
		},
		// evcc ignores the price limit in automatic mode, see core/site_optimizer_gate.go
		priceIgnored() {
			return priceLimitIgnored(store.state?.optimizerAutomatic, this.mode);
		},
		marstek() {
			return store.state?.peakShavingBatteryType === "marstek";
		},
		dynamic() {
			return !!store.state?.peakShavingChargeEntity;
		},
		chargeUnknown() {
			return (store.state?.peakShavingChargePowerEffective ?? 0) <= 0;
		},
		effectiveText() {
			if (this.chargeUnknown) {
				return this.$t("config.gridcharge.chargePowerUnknown");
			}
			const watt = this.fmtW(
				store.state?.peakShavingChargePowerEffective ?? 0,
				this.POWER_UNIT.W
			);
			const source = this.$t(
				`config.gridcharge.source.${store.state?.peakShavingChargePowerSource ?? "unknown"}`
			);
			return `${watt} (${source})`;
		},
		setpointText() {
			return this.fmtW(store.state?.peakShavingChargeSetpoint ?? 0, this.POWER_UNIT.W);
		},
	},
	methods: {
		reset() {
			const entity = store?.state?.peakShavingChargeEntity || "";
			const power = store?.state?.peakShavingChargePower || 0;
			const mode = gridChargeMode(store?.state?.batteryGridChargeMode);
			this.saving = false;
			this.error = "";
			this.entity = entity;
			this.initialEntity = entity;
			this.chargePower = power;
			this.initialChargePower = power;
			this.mode = mode;
			this.initialMode = mode;
		},
		async save() {
			this.saving = true;
			this.error = "";

			try {
				// the mode first, so a rejected entity does not leave it unsaved
				if (this.mode !== this.initialMode) {
					await api.post(`batterygridchargemode/${this.mode}`);
					this.initialMode = this.mode;
				}

				// the power next: the entity can be rejected, the power cannot
				if (this.normalizedChargePower !== this.initialChargePower) {
					await api.post(
						`peakshavingchargepower/${encodeURIComponent(this.normalizedChargePower)}`
					);
					this.initialChargePower = this.normalizedChargePower;
				}

				const entity = this.entity.trim();
				if (entity !== this.initialEntity) {
					if (entity) {
						await api.post(`peakshavingchargeentity/${encodeURIComponent(entity)}`);
					} else {
						await api.delete("peakshavingchargeentity");
					}
					this.initialEntity = entity;
				}

				this.$emit("changed");
			} catch (e) {
				this.error =
					e?.response?.data?.error ||
					e.message ||
					this.$t("config.gridcharge.entityInvalid");
			}

			this.saving = false;
		},
	},
};
</script>
<style scoped>
.container {
	margin-left: calc(var(--bs-gutter-x) * -0.5);
	margin-right: calc(var(--bs-gutter-x) * -0.5);
	padding-right: 0;
}
</style>
