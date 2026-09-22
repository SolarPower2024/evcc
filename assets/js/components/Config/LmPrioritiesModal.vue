<template>
	<GenericModal
		id="lmPrioritiesModal"
		ref="modal"
		:title="$t('config.lmpriorities.title')"
		data-testid="lmpriorities-modal"
		config-modal-name="lmpriorities"
		@open="open"
	>
		<p>{{ $t("config.lmpriorities.description") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>
		<p v-else-if="!loads.length" class="text-muted">
			{{ $t("config.lmpriorities.noLoads") }}
		</p>

		<form ref="form" class="container mx-0 px-0" @submit.prevent="save">
			<FormRow
				v-for="load in loads"
				:id="rowId(load.name)"
				:key="load.name"
				:label="load.label"
			>
				<select
					:id="rowId(load.name)"
					v-model.number="values[load.name]"
					class="form-select"
					:data-testid="`lmpriority-${load.name}`"
				>
					<option v-for="o in options" :key="o.value" :value="o.value">
						{{ o.name }}
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
					:disabled="saving || !changed.length"
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

const MAX_PRIORITY = 10;

// Shed priorities of all loads in load management: the loadpoints on a circuit
// and the home battery once it is assigned to one. Lower is shed first. Unlike
// the loadpoints' own settings, these take effect immediately.
export default {
	name: "LmPrioritiesModal",
	components: { FormRow, GenericModal },
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			values: {},
			initial: {},
		};
	},
	computed: {
		loads() {
			return (store.state?.lmPriorities || []).map((l) => ({
				name: l.name,
				priority: l.priority,
				label: l.battery ? this.$t("config.lmpriorities.battery") : l.title || l.name,
			}));
		},
		options() {
			return Array.from({ length: MAX_PRIORITY + 1 }, (_, i) => {
				let name = `${i}`;
				if (i === 0) name = this.$t("config.lmpriorities.first", { priority: i });
				if (i === MAX_PRIORITY) name = this.$t("config.lmpriorities.last", { priority: i });
				return { value: i, name };
			});
		},
		changed() {
			return this.loads
				.map((l) => l.name)
				.filter((name) => this.values[name] !== this.initial[name]);
		},
	},
	methods: {
		rowId(name) {
			return `lmPriority-${name.replace(/[^a-zA-Z0-9_-]/g, "_")}`;
		},
		reset() {
			const values = {};
			this.loads.forEach((l) => (values[l.name] = l.priority));
			this.saving = false;
			this.error = "";
			this.values = { ...values };
			this.initial = { ...values };
		},
		open() {
			this.reset();
		},
		async save() {
			this.saving = true;
			this.error = "";

			try {
				for (const name of this.changed) {
					await api.post(
						`lmpriority/${encodeURIComponent(name)}/${encodeURIComponent(this.values[name])}`
					);
				}

				this.$emit("changed");
				this.$refs.modal.close();
			} catch (e) {
				this.error =
					e?.response?.data?.error || e.message || this.$t("config.lmpriorities.invalid");
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
