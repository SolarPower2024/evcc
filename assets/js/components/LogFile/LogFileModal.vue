<template>
	<GenericModal
		id="logFileModal"
		ref="modal"
		:title="$t('logFile.title')"
		data-testid="logfile-modal"
		@open="open"
	>
		<p>{{ $t("logFile.description") }}</p>
		<p v-if="error" class="text-danger" data-testid="logfile-error">{{ error }}</p>

		<form class="container mx-0 px-0" @submit.prevent="save">
			<div class="form-check form-switch mb-3">
				<input
					id="logFileEnabled"
					v-model="enabled"
					class="form-check-input"
					type="checkbox"
					role="switch"
					data-testid="logfile-enabled"
				/>
				<label class="form-check-label" for="logFileEnabled">
					{{ $t("logFile.enabledLabel") }}
				</label>
			</div>

			<FormRow id="logFileLevel" :label="$t('logFile.levelLabel')">
				<select
					id="logFileLevel"
					v-model="level"
					class="form-select"
					data-testid="logfile-level"
				>
					<option v-for="l in levels" :key="l" :value="l">{{ l.toUpperCase() }}</option>
				</select>
			</FormRow>

			<FormRow id="logFileDays" :label="$t('logFile.daysLabel')">
				<div class="input-group">
					<input
						id="logFileDays"
						v-model.number="days"
						type="number"
						step="any"
						class="form-control"
						data-testid="logfile-days"
					/>
					<span class="input-group-text">{{ $t("logFile.daysUnit") }}</span>
				</div>
			</FormRow>

			<dl v-if="state" class="small mb-0 mt-4" data-testid="logfile-state">
				<div class="d-flex gap-2">
					<dt class="fw-normal evcc-gray">{{ $t("logFile.folder") }}</dt>
					<dd class="mb-1 text-break" data-testid="logfile-dir">
						{{ state.dir || "-" }}
					</dd>
				</div>
				<div class="d-flex gap-2">
					<dt class="fw-normal evcc-gray">{{ $t("logFile.files") }}</dt>
					<dd class="mb-1" data-testid="logfile-files">{{ state.files }}</dd>
				</div>
				<div class="d-flex gap-2">
					<dt class="fw-normal evcc-gray">{{ $t("logFile.size") }}</dt>
					<dd class="mb-1" data-testid="logfile-size">{{ formatSize(state.size) }}</dd>
				</div>
				<div v-if="state.error" class="d-flex gap-2 text-danger">
					<dt class="fw-normal">{{ $t("logFile.failed") }}</dt>
					<dd class="mb-1" data-testid="logfile-failure">{{ state.error }}</dd>
				</div>
			</dl>

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
					data-testid="logfile-save"
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

<script lang="ts">
import { defineComponent } from "vue";
import GenericModal from "../Helper/GenericModal.vue";
import FormRow from "../Config/FormRow.vue";
import api from "@/api";
import type { LogFileState } from "@/types/evcc-lm";

const MIN_DAYS = 1;
const MAX_DAYS = 90;
const LEVELS = ["error", "warn", "info", "debug", "trace"];

function formatSize(bytes: number) {
	if (bytes < 1024) return `${bytes} B`;
	if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
	if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
	return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

// Custom extension: switch the log file on, its level and retention, see
// util/logstash/file_custom.go. No min, max or step on the input: the browser
// would block the submit without a message, the range is checked in save.
export default defineComponent({
	name: "LogFileModal",
	components: { FormRow, GenericModal },
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			state: null as LogFileState | null,
			enabled: false,
			level: "debug",
			days: 14,
			levels: LEVELS,
		};
	},
	computed: {
		changed() {
			const s = this.state;
			return (
				!s || s.enabled !== this.enabled || s.level !== this.level || s.days !== this.days
			);
		},
	},
	methods: {
		formatSize,
		async open() {
			this.saving = false;
			this.error = "";
			try {
				this.apply((await api.get("logfile")).data);
			} catch (e) {
				this.error = this.message(e);
			}
		},
		apply(state: LogFileState) {
			this.state = state;
			this.enabled = state.enabled;
			this.level = state.level;
			this.days = state.days;
		},
		message(e: any): string {
			return e?.response?.data?.error || e?.message || this.$t("logFile.invalid");
		},
		async save() {
			if (!Number.isInteger(this.days) || this.days < MIN_DAYS || this.days > MAX_DAYS) {
				this.error = this.$t("logFile.invalidDays", { min: MIN_DAYS, max: MAX_DAYS });
				return;
			}

			this.saving = true;
			this.error = "";

			try {
				const res = await api.post("logfile", {
					enabled: this.enabled,
					level: this.level,
					days: this.days,
				});
				this.apply(res.data);
				this.$emit("changed");
			} catch (e) {
				this.error = this.message(e);
			}

			this.saving = false;
		},
	},
});
</script>
<style scoped>
.container {
	margin-left: calc(var(--bs-gutter-x) * -0.5);
	margin-right: calc(var(--bs-gutter-x) * -0.5);
	padding-right: 0;
}
dt {
	min-width: 5rem;
}
</style>
