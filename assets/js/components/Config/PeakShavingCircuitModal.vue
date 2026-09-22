<template>
	<GenericModal
		id="peakShavingCircuitModal"
		ref="modal"
		:title="$t('config.peakshaving.circuitTitle')"
		data-testid="peakshaving-circuit-modal"
		config-modal-name="peakshavingcircuit"
		@open="open"
	>
		<p>{{ $t("config.peakshaving.circuitDescription") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>
		<p v-else-if="!circuits.length" class="text-muted">
			{{ $t("config.peakshaving.circuitNoneConfigured") }}
		</p>

		<form ref="form" class="container mx-0 px-0" @submit.prevent="save">
			<FormRow
				id="peakShavingCircuit"
				:label="$t('config.peakshaving.circuitLabel')"
				:help="$t('config.peakshaving.circuitHelp')"
			>
				<select
					id="peakShavingCircuit"
					v-model="circuit"
					class="form-select"
					data-testid="peakshaving-circuit"
				>
					<option value="">{{ $t("config.peakshaving.circuitNone") }}</option>
					<option v-for="c in circuits" :key="c.name" :value="c.name">
						{{ c.label }}
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
import store from "@/store";
import api from "@/api";

// Assigns the home battery to a circuit. That link is what puts the battery into
// load management, and what grid charging is checked against - the peak limit is
// an economic target for discharging, not a constraint on charging.
export default {
	name: "PeakShavingCircuitModal",
	components: { FormRow, GenericModal },
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			circuit: "",
			initialCircuit: "",
		};
	},
	computed: {
		changed() {
			return this.circuit !== this.initialCircuit;
		},
		circuits() {
			const all = store.state?.circuits || {};
			return Object.entries(all).map(([name, c]) => ({ name, label: c?.title || name }));
		},
	},
	methods: {
		reset() {
			const circuit = store?.state?.peakShavingCircuit || "";
			this.saving = false;
			this.error = "";
			this.circuit = circuit;
			this.initialCircuit = circuit;
		},
		open() {
			this.reset();
		},
		async save() {
			this.saving = true;
			this.error = "";

			try {
				if (this.circuit) {
					await api.post(`peakshavingcircuit/${encodeURIComponent(this.circuit)}`);
				} else {
					await api.delete("peakshavingcircuit");
				}

				this.$emit("changed");
				this.$refs.modal.close();
			} catch (e) {
				this.error =
					e?.response?.data?.error ||
					e.message ||
					this.$t("config.peakshaving.circuitInvalid");
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
