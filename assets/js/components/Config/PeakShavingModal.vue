<template>
	<GenericModal
		id="peakShavingModal"
		ref="modal"
		:title="$t('config.peakshaving.title')"
		data-testid="peakshaving-modal"
		config-modal-name="peakshaving"
		@open="open"
	>
		<p>{{ $t("config.peakshaving.description") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>

		<form ref="form" class="container mx-0 px-0" @submit.prevent="save">
			<FormRow
				id="peakShavingEntity"
				:label="$t('config.peakshaving.entityLabel')"
				:help="$t('config.peakshaving.entityHelp')"
			>
				<input
					id="peakShavingEntity"
					v-model="entity"
					type="text"
					class="form-control"
					placeholder="input_number.battery_peak_power"
					data-testid="peakshaving-entity"
				/>
			</FormRow>

			<FormRow
				id="peakShavingEnergyEntity"
				:label="$t('config.peakshaving.energyEntityLabel')"
				:help="$t('config.peakshaving.energyEntityHelp')"
				optional
			>
				<input
					id="peakShavingEnergyEntity"
					v-model="energyEntity"
					type="text"
					class="form-control"
					placeholder="sensor.grid_import_energy"
					data-testid="peakshaving-energy-entity"
				/>
			</FormRow>

			<p class="small mb-0" data-testid="peakshaving-source">
				{{ $t("config.peakshaving.sourceLabel") }}:
				<strong>{{ sourceText }}</strong>
			</p>

			<p class="text-muted mt-3 small">{{ $t("config.peakshaving.hint") }}</p>

			<!-- custom: follow the peak, see core/site_peak_follow.go -->
			<hr class="my-4" />
			<div class="form-check form-switch mb-2">
				<input
					id="peakFollow"
					v-model="follow"
					class="form-check-input"
					type="checkbox"
					role="switch"
					data-testid="peakshaving-follow"
				/>
				<label class="form-check-label" for="peakFollow">
					{{ $t("config.peakshaving.followLabel") }}
				</label>
			</div>
			<p class="small text-muted">{{ $t("config.peakshaving.followHelp") }}</p>
			<FormRow
				v-if="follow"
				id="peakFollowBuffer"
				:label="$t('config.peakshaving.followBufferLabel')"
				:help="$t('config.peakshaving.followBufferHelp')"
			>
				<div class="input-group">
					<input
						id="peakFollowBuffer"
						v-model.number="buffer"
						type="number"
						step="any"
						class="form-control"
						data-testid="peakshaving-follow-buffer"
					/>
					<span class="input-group-text">kW</span>
				</div>
			</FormRow>
			<FormRow
				v-if="follow"
				id="peakFollowCircuit"
				:label="$t('config.peakshaving.followCircuitLabel')"
				:help="$t('config.peakshaving.followCircuitHelp')"
				optional
			>
				<select
					id="peakFollowCircuit"
					v-model="followCircuit"
					class="form-select"
					data-testid="peakshaving-follow-circuit"
				>
					<option value="">{{ $t("config.peakshaving.followCircuitNone") }}</option>
					<option v-for="c in circuitOptions" :key="c.name" :value="c.name">
						{{ c.title }}
					</option>
				</select>
			</FormRow>

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
import formatter from "@/mixins/formatter";
import store from "@/store";
import api from "@/api";

// Target entity for the peak shaving discharge setpoint and the energy sensor
// metering the 15 minute window. The switch, the peak limit and the reserve soc
// are operating controls and live on the battery page.
export default {
	name: "PeakShavingModal",
	components: { FormRow, GenericModal },
	mixins: [formatter],
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			entity: "",
			initialEntity: "",
			energyEntity: "",
			initialEnergyEntity: "",
			follow: false,
			initialFollow: false,
			buffer: 0.5,
			initialBuffer: 0.5,
			followCircuit: "",
			initialFollowCircuit: "",
		};
	},
	computed: {
		entityChanged() {
			return this.entity.trim() !== this.initialEntity;
		},
		energyEntityChanged() {
			return this.energyEntity.trim() !== this.initialEnergyEntity;
		},
		followChanged() {
			return this.follow !== this.initialFollow;
		},
		bufferChanged() {
			return this.buffer !== this.initialBuffer;
		},
		followCircuitChanged() {
			return this.followCircuit !== this.initialFollowCircuit;
		},
		// circuits with a power limit, configured or changed at runtime
		circuitOptions() {
			const configured = store?.state?.lmOff?.limits || {};
			return Object.entries(store?.state?.circuits || {})
				.filter(([name, c]) => c.maxPower > 0 || configured[name] > 0)
				.map(([name, c]) => ({
					name,
					title: `${c.title || name} (${this.fmtW(configured[name] || c.maxPower)})`,
				}));
		},
		changed() {
			return (
				this.entityChanged ||
				this.energyEntityChanged ||
				this.followChanged ||
				this.bufferChanged ||
				this.followCircuitChanged
			);
		},
		sourceText() {
			const source = store?.state?.peakShavingSource || "power";
			return this.$t(`config.peakshaving.source.${source}`);
		},
	},
	methods: {
		reset() {
			const entity = store?.state?.peakShavingEntity || "";
			const energyEntity = store?.state?.peakShavingEnergyEntity || "";
			this.saving = false;
			this.error = "";
			this.entity = entity;
			this.initialEntity = entity;
			this.energyEntity = energyEntity;
			this.initialEnergyEntity = energyEntity;
			const follow = store?.state?.peakFollow;
			this.follow = !!follow?.enabled;
			this.initialFollow = this.follow;
			this.buffer = (follow?.buffer ?? 500) / 1000;
			this.initialBuffer = this.buffer;
			this.followCircuit = follow?.circuit || "";
			this.initialFollowCircuit = this.followCircuit;
		},
		open() {
			this.reset();
		},
		async save() {
			// 0-5 kW in 0.1 kW steps, see core/site_peak_follow.go
			const bufferW = Math.round(this.buffer * 1000);
			if (
				this.bufferChanged &&
				(typeof this.buffer !== "number" ||
					Number.isNaN(this.buffer) ||
					bufferW < 0 ||
					bufferW > 5000 ||
					bufferW % 100 !== 0)
			) {
				this.error = this.$t("config.peakshaving.followBufferInvalid");
				return;
			}

			this.saving = true;
			this.error = "";

			try {
				const entity = this.entity.trim();
				if (this.entityChanged) {
					if (entity) {
						await api.post(`peakshavingentity/${encodeURIComponent(entity)}`);
					} else {
						await api.delete("peakshavingentity");
					}
					this.initialEntity = entity;
				}

				const energyEntity = this.energyEntity.trim();
				if (this.energyEntityChanged) {
					if (energyEntity) {
						await api.post(
							`peakshavingenergyentity/${encodeURIComponent(energyEntity)}`
						);
					} else {
						await api.delete("peakshavingenergyentity");
					}
				}

				if (this.bufferChanged) {
					await api.post(`peakfollowbuffer/${bufferW}`);
				}
				if (this.followCircuitChanged) {
					if (this.followCircuit) {
						await api.post(
							`peakfollowcircuit/${encodeURIComponent(this.followCircuit)}`
						);
					} else {
						await api.delete("peakfollowcircuit");
					}
				}
				if (this.followChanged) {
					await api.post(`peakfollow/${this.follow}`);
				}

				this.$emit("changed");
				this.$refs.modal.close();
			} catch (e) {
				// the backend rejects a non-number entity and an unreachable
				// target, so show that rather than closing on a dead setting
				this.error =
					e?.response?.data?.error ||
					e.message ||
					this.$t("config.peakshaving.entityInvalid");
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
