<template>
	<GenericModal
		id="lmShedGuardModal"
		ref="modal"
		:title="$t('config.lmshedguard.title')"
		data-testid="lmshedguard-modal"
		config-modal-name="lmshedguard"
		@open="open"
	>
		<p>{{ $t("config.lmshedguard.description") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>

		<form ref="form" class="container mx-0 px-0" @submit.prevent="save">
			<FormRow
				id="lmShedGuardMinutes"
				:label="$t('config.lmshedguard.minutesLabel')"
				:help="$t('config.lmshedguard.minutesHelp')"
			>
				<div class="input-group">
					<input
						id="lmShedGuardMinutes"
						v-model.number="minutes"
						type="number"
						step="any"
						class="form-control"
						data-testid="lmshedguard-minutes"
					/>
					<span class="input-group-text">min</span>
				</div>
			</FormRow>

			<h6 class="mt-4">{{ $t("config.lmshedguard.loadpointsLabel") }}</h6>
			<p v-if="!loadpoints.length" class="text-muted">
				{{ $t("config.lmshedguard.noLoadpoints") }}
			</p>
			<div v-for="lp in loadpoints" :key="lp.name" class="d-flex mb-2">
				<input
					:id="rowId(lp.name)"
					v-model="protectedNames[lp.name]"
					class="form-check-input"
					type="checkbox"
					:data-testid="`lmshedguard-${lp.name}`"
				/>
				<label class="form-check-label ms-2" :for="rowId(lp.name)">
					{{ lp.title }}
				</label>
			</div>

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

const MAX_MINUTES = 120;

// No min, max or step on the input: the browser would then block the submit
// without a message, the range is checked in save instead.
//
// Shed guard: a protected loadpoint that load management had to switch off
// stays off for the given minutes, so it does not flap while the demand hovers
// around the limit. Only loadpoints on a circuit can be shed, so only those are
// offered.
export default {
	name: "LmShedGuardModal",
	components: { FormRow, GenericModal },
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			minutes: 0,
			protectedNames: {},
			initialMinutes: 0,
			initialProtected: {},
		};
	},
	computed: {
		loadpoints() {
			return (store.state?.lmPriorities || [])
				.filter((l) => !l.battery)
				.map((l) => ({ name: l.name, title: l.title || l.name }));
		},
		changedProtection() {
			return this.loadpoints
				.map((lp) => lp.name)
				.filter((name) => !!this.protectedNames[name] !== !!this.initialProtected[name]);
		},
		minutesChanged() {
			return this.minutes !== this.initialMinutes;
		},
		changed() {
			return this.minutesChanged || this.changedProtection.length > 0;
		},
	},
	methods: {
		rowId(name) {
			return `lmShedGuard-${name.replace(/[^a-zA-Z0-9_-]/g, "_")}`;
		},
		open() {
			const names = {};
			(store.state?.lmShedProtected || []).forEach((name) => (names[name] = true));

			this.saving = false;
			this.error = "";
			this.minutes = store.state?.lmShedGuard ?? 0;
			this.initialMinutes = this.minutes;
			this.protectedNames = { ...names };
			this.initialProtected = { ...names };
		},
		async save() {
			if (!Number.isInteger(this.minutes) || this.minutes < 0 || this.minutes > MAX_MINUTES) {
				this.error = this.$t("config.lmshedguard.invalidMinutes", { max: MAX_MINUTES });
				return;
			}

			this.saving = true;
			this.error = "";

			try {
				if (this.minutesChanged) {
					await api.post(`lmshedguard/${this.minutes}`);
				}
				for (const name of this.changedProtection) {
					const on = !!this.protectedNames[name];
					await api.post(`lmshedprotect/${encodeURIComponent(name)}/${on}`);
				}

				this.$emit("changed");
				this.$refs.modal.close();
			} catch (e) {
				this.error =
					e?.response?.data?.error || e.message || this.$t("config.lmshedguard.invalid");
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
