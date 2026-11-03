<template>
	<GenericModal
		id="feedInEegModal"
		ref="modal"
		:title="$t('config.feedineeg.title')"
		data-testid="feedineeg-modal"
		config-modal-name="feedineeg"
		@open="open"
	>
		<p>{{ $t("config.feedineeg.description") }}</p>
		<p v-if="error" class="text-danger" data-testid="feedineeg-error">{{ error }}</p>

		<form class="container mx-0 px-0" @submit.prevent="save">
			<FormRow
				id="feedInEegEntity"
				:label="$t('config.feedineeg.entityLabel')"
				:help="$t('config.feedineeg.entityHelp')"
				optional
			>
				<input
					id="feedInEegEntity"
					v-model="entity"
					type="text"
					class="form-control"
					placeholder="sensor.eeg_export_energy"
					data-testid="feedineeg-entity"
				/>
			</FormRow>

			<FormRow
				id="importEegEntity"
				:label="$t('config.feedineeg.importLabel')"
				:help="$t('config.feedineeg.importHelp')"
				optional
			>
				<input
					id="importEegEntity"
					v-model="importEntity"
					type="text"
					class="form-control"
					placeholder="sensor.eeg_import_energy"
					data-testid="importeeg-entity"
				/>
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
					data-testid="feedineeg-save"
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

// Custom extension: the Home Assistant counters of the EEG, the export under the
// second feed-in tariff and the import shown in the grid chart, see
// core/site_feedin_eeg.go and core/site_import_eeg.go
export default {
	name: "FeedInEegModal",
	components: { FormRow, GenericModal },
	data() {
		return {
			saving: false,
			error: "",
			entity: "",
			initialEntity: "",
			importEntity: "",
			initialImportEntity: "",
		};
	},
	computed: {
		changed() {
			return (
				this.entity.trim() !== this.initialEntity ||
				this.importEntity.trim() !== this.initialImportEntity
			);
		},
	},
	methods: {
		open() {
			const entity = store?.state?.feedInEegEntity || "";
			this.saving = false;
			this.error = "";
			this.entity = entity;
			this.initialEntity = entity;
			const importEntity = store?.state?.importEegEntity || "";
			this.importEntity = importEntity;
			this.initialImportEntity = importEntity;
		},
		async post(route, entity) {
			if (entity) {
				await api.post(`${route}/${encodeURIComponent(entity)}`);
			} else {
				await api.delete(route);
			}
		},
		async save() {
			this.saving = true;
			this.error = "";

			try {
				const entity = this.entity.trim();
				if (entity !== this.initialEntity) {
					await this.post("feedineegentity", entity);
					this.initialEntity = entity;
				}
				const importEntity = this.importEntity.trim();
				if (importEntity !== this.initialImportEntity) {
					await this.post("importeegentity", importEntity);
					this.initialImportEntity = importEntity;
				}
				this.$refs.modal.close();
			} catch (e) {
				// the backend rejects anything but an energy counter
				this.error = e?.response?.data?.error || e.message;
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
