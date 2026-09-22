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

			<p class="text-muted mt-3 small">{{ $t("config.peakshaving.hint") }}</p>

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

// Target entity for the peak shaving discharge setpoint. The switch, the peak
// limit and the reserve soc are operating controls and live on the battery page.
export default {
	name: "PeakShavingModal",
	components: { FormRow, GenericModal },
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			entity: "",
			initialEntity: "",
		};
	},
	computed: {
		changed() {
			return this.entity.trim() !== this.initialEntity;
		},
	},
	methods: {
		reset() {
			const entity = store?.state?.peakShavingEntity || "";
			this.saving = false;
			this.error = "";
			this.entity = entity;
			this.initialEntity = entity;
		},
		open() {
			this.reset();
		},
		async save() {
			this.saving = true;
			this.error = "";

			try {
				const entity = this.entity.trim();
				if (entity) {
					await api.post(`peakshavingentity/${encodeURIComponent(entity)}`);
				} else {
					await api.delete("peakshavingentity");
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
