<template>
	<GenericModal
		id="gridChargeModal"
		ref="modal"
		:title="$t('config.gridcharge.title')"
		data-testid="gridcharge-modal"
		config-modal-name="gridcharge"
		@open="open"
	>
		<p>{{ $t("config.gridcharge.description") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>

		<form ref="form" class="container mx-0 px-0" @submit.prevent="save">
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

			<div class="mt-4 d-flex justify-content-between gap-2 flex-column flex-sm-row">
				<button
					type="button"
					class="btn btn-link text-muted btn-cancel"
					data-bs-dismiss="modal"
				>
					{{ $t("config.general.cancel") }}
				</button>

				<button
					type="submit"
					class="btn btn-primary order-1 order-sm-2 flex-grow-1 flex-sm-grow-0 px-4"
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
	</GenericModal>
</template>

<script>
import GenericModal from "../Helper/GenericModal.vue";
import FormRow from "./FormRow.vue";
import store from "@/store";
import api from "@/api";
import formatter from "@/mixins/formatter";

// How the home battery charges from the grid: with a charge power entity evcc
// sizes the power to the peak limit and the circuit, without one charging is
// switched on and off at the expected charge power.
export default {
	name: "GridChargeModal",
	components: { FormRow, GenericModal },
	mixins: [formatter],
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			entity: "",
			chargePower: 0,
			initialEntity: "",
			initialChargePower: 0,
		};
	},
	computed: {
		changed() {
			return (
				this.entity.trim() !== this.initialEntity ||
				this.normalizedChargePower !== this.initialChargePower
			);
		},
		normalizedChargePower() {
			return Math.max(0, Math.round(Number(this.chargePower) || 0));
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
			this.saving = false;
			this.error = "";
			this.entity = entity;
			this.initialEntity = entity;
			this.chargePower = power;
			this.initialChargePower = power;
		},
		open() {
			this.reset();
		},
		async save() {
			this.saving = true;
			this.error = "";

			try {
				// the power first: the entity can be rejected, the power cannot
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
				}

				this.$emit("changed");
				this.$refs.modal.close();
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
