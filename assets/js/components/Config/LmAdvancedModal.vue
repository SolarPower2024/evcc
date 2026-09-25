<template>
	<GenericModal
		id="lmAdvancedModal"
		ref="modal"
		:title="$t('config.lmadvanced.title')"
		data-testid="lmadvanced-modal"
		config-modal-name="lmadvanced"
		@open="open"
	>
		<p>{{ $t("config.lmadvanced.description") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>

		<form ref="form" class="container mx-0 px-0" @submit.prevent="save">
			<FormRow
				v-for="field in numberFields"
				:id="`lmAdvanced-${field.name}`"
				:key="field.name"
				:label="$t(`config.lmadvanced.${field.name}Label`)"
				:help="$t(`config.lmadvanced.${field.name}Help`, { std: field.default })"
			>
				<div class="input-group">
					<input
						:id="`lmAdvanced-${field.name}`"
						v-model.number="values[field.name]"
						type="number"
						step="any"
						class="form-control"
						:data-testid="`lmadvanced-${field.name}`"
					/>
					<span class="input-group-text">{{
						field.unitKey ? $t(field.unitKey) : field.unit
					}}</span>
				</div>
			</FormRow>

			<FormRow
				id="lmAdvanced-phases"
				:label="$t('config.lmadvanced.phasesLabel')"
				:help="$t('config.lmadvanced.phasesHelp')"
			>
				<select
					id="lmAdvanced-phases"
					v-model.number="values.phases"
					class="form-select"
					data-testid="lmadvanced-phases"
				>
					<option :value="1">{{ $t("config.lmadvanced.phases1") }}</option>
					<option :value="3">{{ $t("config.lmadvanced.phases3") }}</option>
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

// Ranges as checked by the server, see core/site_lm_advanced.go. The inputs have
// no min, max or step: the browser would block the submit without a message.
const FIELDS = [
	{ name: "hysteresis", unit: "%", min: 0, max: 20, integer: false, default: 2 },
	{ name: "freeValue", unit: "W", min: 1, max: 100000, integer: true, default: 10000 },
	{ name: "holdOff", unit: "min", min: 1, max: 60, integer: true, default: 5 },
	{ name: "timeout", unit: "min", min: 1, max: 60, integer: true, default: 10 },
	{ name: "peakFreeze", unit: "min", min: 1, max: 14, integer: true, default: 12 },
	{ name: "peakCap", unit: "×", min: 1, max: 10, integer: false, default: 2 },
	{
		name: "followCycles",
		unitKey: "config.lmadvanced.cycles",
		min: 0,
		max: 20,
		integer: true,
		default: 3,
	},
];

// Advanced load management settings. Each value overrides the default; the
// dialog shows the values in effect.
export default {
	name: "LmAdvancedModal",
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
		numberFields() {
			return FIELDS;
		},
		changed() {
			return Object.keys(this.values).filter(
				(name) => this.values[name] !== this.initial[name]
			);
		},
	},
	methods: {
		open() {
			const state = store.state?.lmAdvanced || {};
			const values = {};
			FIELDS.forEach((f) => (values[f.name] = state[f.name] ?? f.default));
			values.phases = state.phases ?? 3;

			this.saving = false;
			this.error = "";
			this.values = { ...values };
			this.initial = { ...values };
		},
		invalidField() {
			return FIELDS.find((f) => {
				const v = this.values[f.name];
				return (
					typeof v !== "number" ||
					Number.isNaN(v) ||
					v < f.min ||
					v > f.max ||
					(f.integer && !Number.isInteger(v))
				);
			});
		},
		async save() {
			const invalid = this.invalidField();
			if (invalid) {
				this.error = this.$t("config.lmadvanced.invalid", {
					label: this.$t(`config.lmadvanced.${invalid.name}Label`),
					min: invalid.min,
					max: invalid.max,
				});
				return;
			}

			this.saving = true;
			this.error = "";

			try {
				for (const name of this.changed) {
					await api.post(`lmadvanced/${name}/${this.values[name]}`);
				}

				this.$emit("changed");
				this.$refs.modal.close();
			} catch (e) {
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
