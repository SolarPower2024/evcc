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
			<!-- custom: battery type Marstek (Omnibattery), see core/site_peak_omni.go -->
			<FormRow
				id="peakShavingBatteryType"
				:label="$t('config.peakshaving.batteryTypeLabel')"
				:help="$t('config.peakshaving.batteryTypeHelp')"
			>
				<select
					id="peakShavingBatteryType"
					v-model="batteryType"
					class="form-select"
					data-testid="peakshaving-battery-type"
				>
					<option value="marstek">Marstek (Omnibattery)</option>
					<option value="byd">BYD</option>
				</select>
			</FormRow>

			<template v-if="batteryType === 'marstek'">
				<FormRow
					id="peakShavingManualEntity"
					:label="$t('config.peakshaving.manualEntityLabel')"
					:help="$t('config.peakshaving.manualEntityHelp')"
				>
					<input
						id="peakShavingManualEntity"
						v-model="manualEntity"
						type="text"
						class="form-control"
						placeholder="switch.marstek_venus_battery_manual_mode"
						data-testid="peakshaving-manual-entity"
					/>
				</FormRow>

				<FormRow
					id="peakShavingModeEntity"
					:label="$t('config.peakshaving.modeEntityLabel')"
					:help="$t('config.peakshaving.modeEntityHelp')"
				>
					<input
						id="peakShavingModeEntity"
						v-model="modeEntity"
						type="text"
						class="form-control"
						placeholder="select.marstek_venus_1_betriebsmodus_erzwingen"
						data-testid="peakshaving-mode-entity"
					/>
				</FormRow>

				<FormRow
					id="peakShavingProtSwitch"
					:label="$t('config.peakshaving.protSwitchLabel')"
					:help="$t('config.peakshaving.protSwitchHelp')"
				>
					<input
						id="peakShavingProtSwitch"
						v-model="protSwitch"
						type="text"
						class="form-control"
						placeholder="switch.marstek_venus_system_spitzenlastkappung"
						data-testid="peakshaving-prot-switch"
					/>
				</FormRow>

				<FormRow
					id="peakShavingProtLimit"
					:label="$t('config.peakshaving.protLimitLabel')"
					:help="$t('config.peakshaving.protLimitHelp')"
				>
					<input
						id="peakShavingProtLimit"
						v-model="protLimit"
						type="text"
						class="form-control"
						placeholder="number.marstek_venus_system_spitzenlastkappung_limit"
						data-testid="peakshaving-prot-limit"
					/>
				</FormRow>

				<FormRow
					id="peakShavingProtSoc"
					:label="$t('config.peakshaving.protSocLabel')"
					:help="$t('config.peakshaving.protSocHelp')"
				>
					<input
						id="peakShavingProtSoc"
						v-model="protSoc"
						type="text"
						class="form-control"
						placeholder="number.marstek_venus_system_spitzenlastkappung_soc_schwelle"
						data-testid="peakshaving-prot-soc"
					/>
				</FormRow>
			</template>

			<!-- Marstek: not written, Omnibattery's peak shaving gets the limit instead -->
			<FormRow
				v-if="batteryType !== 'marstek'"
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

			<!-- custom: skip writes below this change, see numberSetter in core/site_peakshaving.go -->
			<FormRow
				id="peakShavingWriteTolerance"
				:label="$t('config.peakshaving.writeToleranceLabel')"
				:help="$t('config.peakshaving.writeToleranceHelp')"
			>
				<div class="input-group">
					<input
						id="peakShavingWriteTolerance"
						v-model.number="tolerance"
						type="number"
						step="any"
						class="form-control"
						data-testid="peakshaving-write-tolerance"
					/>
					<span class="input-group-text">W</span>
				</div>
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

// Target entity for the peak shaving discharge setpoint and the energy sensor
// metering the 15 minute window; for the battery type Marstek also the switch of
// the manual control and the select of the forced mode (grid charging) and the
// switch, limit and soc threshold of Omnibattery's peak shaving. The switch, the peak
// limit and the reserve soc are operating controls and live on the battery page.
export default {
	name: "PeakShavingModal",
	components: { FormRow, GenericModal },
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			batteryType: "byd",
			initialBatteryType: "byd",
			manualEntity: "",
			initialManualEntity: "",
			modeEntity: "",
			initialModeEntity: "",
			protSwitch: "",
			initialProtSwitch: "",
			protLimit: "",
			initialProtLimit: "",
			protSoc: "",
			initialProtSoc: "",
			entity: "",
			initialEntity: "",
			energyEntity: "",
			initialEnergyEntity: "",
			follow: false,
			initialFollow: false,
			buffer: 0.5,
			initialBuffer: 0.5,
			tolerance: 0,
			initialTolerance: 0,
		};
	},
	computed: {
		batteryTypeChanged() {
			return this.batteryType !== this.initialBatteryType;
		},
		marstekChanged() {
			return (
				this.batteryType === "marstek" &&
				(this.manualEntity.trim() !== this.initialManualEntity ||
					this.modeEntity.trim() !== this.initialModeEntity ||
					this.protSwitch.trim() !== this.initialProtSwitch ||
					this.protLimit.trim() !== this.initialProtLimit ||
					this.protSoc.trim() !== this.initialProtSoc)
			);
		},
		// hidden for a Marstek, so not saved then
		entityChanged() {
			return this.batteryType !== "marstek" && this.entity.trim() !== this.initialEntity;
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
		toleranceChanged() {
			return this.tolerance !== this.initialTolerance;
		},
		changed() {
			return (
				this.batteryTypeChanged ||
				this.marstekChanged ||
				this.entityChanged ||
				this.energyEntityChanged ||
				this.followChanged ||
				this.bufferChanged ||
				this.toleranceChanged
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
			const manualEntity = store?.state?.peakShavingManualEntity || "";
			const modeEntity = store?.state?.peakShavingModeEntity || "";
			const protSwitch = store?.state?.peakShavingProtSwitch || "";
			const protLimit = store?.state?.peakShavingProtLimit || "";
			const protSoc = store?.state?.peakShavingProtSoc || "";
			const batteryType = store?.state?.peakShavingBatteryType || "byd";
			this.saving = false;
			this.error = "";
			this.batteryType = batteryType;
			this.initialBatteryType = batteryType;
			this.manualEntity = manualEntity;
			this.initialManualEntity = manualEntity;
			this.modeEntity = modeEntity;
			this.initialModeEntity = modeEntity;
			this.protSwitch = protSwitch;
			this.initialProtSwitch = protSwitch;
			this.protLimit = protLimit;
			this.initialProtLimit = protLimit;
			this.protSoc = protSoc;
			this.initialProtSoc = protSoc;
			this.entity = entity;
			this.initialEntity = entity;
			this.energyEntity = energyEntity;
			this.initialEnergyEntity = energyEntity;
			const follow = store?.state?.peakFollow;
			this.follow = !!follow?.enabled;
			this.initialFollow = this.follow;
			this.buffer = (follow?.buffer ?? 500) / 1000;
			this.initialBuffer = this.buffer;
			// stored with the advanced settings, see core/site_lm_advanced.go
			this.tolerance = store?.state?.lmAdvanced?.writeTolerance ?? 0;
			this.initialTolerance = this.tolerance;
		},
		open() {
			this.reset();
		},
		// an empty entity removes the setting
		async postEntity(route, entity) {
			if (entity) {
				await api.post(`${route}/${encodeURIComponent(entity)}`);
			} else {
				await api.delete(route);
			}
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
			if (
				this.toleranceChanged &&
				(!Number.isInteger(this.tolerance) || this.tolerance < 0 || this.tolerance > 1000)
			) {
				this.error = this.$t("config.peakshaving.writeToleranceInvalid");
				return;
			}

			// the battery type Marstek cannot shave peaks without Omnibattery's peak
			// shaving; the manual switch and mode are only for grid charging
			if (
				this.batteryType === "marstek" &&
				store?.state?.peakShaving &&
				(!this.protSwitch.trim() || !this.protLimit.trim() || !this.protSoc.trim())
			) {
				this.error = this.$t("config.peakshaving.marstekRequired");
				return;
			}

			this.saving = true;
			this.error = "";

			try {
				// the entities first, the backend checks them; the type only changes
				// once they are accepted (BYD with peak shaving on needs the discharge
				// entity before the type)
				const entity = this.entity.trim();
				if (this.entityChanged) {
					await this.postEntity("peakshavingentity", entity);
					this.initialEntity = entity;
				}

				if (this.batteryType === "marstek") {
					const manualEntity = this.manualEntity.trim();
					if (manualEntity !== this.initialManualEntity) {
						await this.postEntity("peakshavingmanualentity", manualEntity);
						this.initialManualEntity = manualEntity;
					}

					const modeEntity = this.modeEntity.trim();
					if (modeEntity !== this.initialModeEntity) {
						await this.postEntity("peakshavingmodeentity", modeEntity);
						this.initialModeEntity = modeEntity;
					}

					const protSwitch = this.protSwitch.trim();
					if (protSwitch !== this.initialProtSwitch) {
						await this.postEntity("peakshavingprotswitch", protSwitch);
						this.initialProtSwitch = protSwitch;
					}

					const protLimit = this.protLimit.trim();
					if (protLimit !== this.initialProtLimit) {
						await this.postEntity("peakshavingprotlimit", protLimit);
						this.initialProtLimit = protLimit;
					}

					const protSoc = this.protSoc.trim();
					if (protSoc !== this.initialProtSoc) {
						await this.postEntity("peakshavingprotsoc", protSoc);
						this.initialProtSoc = protSoc;
					}
				}
				if (this.batteryTypeChanged) {
					await api.post(`peakshavingbatterytype/${this.batteryType}`);
					this.initialBatteryType = this.batteryType;
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
				if (this.toleranceChanged) {
					await api.post(`lmadvanced/writeTolerance/${this.tolerance}`);
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
