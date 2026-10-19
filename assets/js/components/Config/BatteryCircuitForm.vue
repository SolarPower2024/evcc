<template>
	<form class="container mx-0 px-0" data-testid="peakshaving-circuit-form" @submit.prevent="save">
		<p>{{ $t("config.peakshaving.circuitDescription") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>
		<p v-else-if="!circuits.length" class="text-muted">
			{{ $t("config.peakshaving.circuitNoneConfigured") }}
		</p>

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

		<!-- the battery phases, moved here from the advanced settings, see core/site_lm_phases.go -->
		<FormRow
			id="lmAdvanced-phases"
			:label="$t('config.lmadvanced.phasesLabel')"
			:help="$t('config.lmadvanced.phasesHelp')"
		>
			<select
				id="lmAdvanced-phases"
				v-model.number="phases"
				class="form-select"
				data-testid="lmadvanced-phases"
			>
				<option v-if="phases === null" :value="null" disabled>–</option>
				<option :value="1">{{ $t("config.lmadvanced.phases1") }}</option>
				<option :value="3">{{ $t("config.lmadvanced.phases3") }}</option>
			</select>
			<div
				v-if="phases === null"
				class="text-danger small mt-1"
				data-testid="lmadvanced-phasesMissing"
			>
				{{ $t("config.lmadvanced.phasesMissing") }}
			</div>
		</FormRow>

		<div class="mt-4 d-flex justify-content-end">
			<button
				type="submit"
				class="btn btn-primary flex-grow-1 flex-sm-grow-0 px-4"
				data-testid="peakshaving-circuit-save"
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
</template>

<script>
import FormRow from "./FormRow.vue";
import store from "@/store";
import api from "@/api";

// Assigns the home battery to a circuit. That link is what puts the battery into
// load management, and what grid charging is checked against - the peak limit is
// an economic target for discharging, not a constraint on charging. The battery
// phases belong to it: a circuit with a current limit needs them.
export default {
	name: "BatteryCircuitForm",
	components: { FormRow },
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			circuit: "",
			initialCircuit: "",
			phases: 3,
			initialPhases: 3,
		};
	},
	computed: {
		circuitChanged() {
			return this.circuit !== this.initialCircuit;
		},
		phasesChanged() {
			return this.phases !== this.initialPhases;
		},
		changed() {
			return this.circuitChanged || this.phasesChanged;
		},
		circuits() {
			const all = store.state?.circuits || {};
			return Object.entries(all).map(([name, c]) => ({ name, label: c?.title || name }));
		},
	},
	methods: {
		reset() {
			// a circuit that was deleted counts as none
			const name = store?.state?.peakShavingCircuit || "";
			const circuit = store?.state?.circuits?.[name] ? name : "";
			const adv = store?.state?.lmAdvanced || {};
			// not set but needed: empty until chosen
			const phases = adv.phasesMissing ? null : (adv.phases ?? 3);
			this.saving = false;
			this.error = "";
			this.circuit = circuit;
			this.initialCircuit = circuit;
			this.phases = phases;
			this.initialPhases = phases;
		},
		async save() {
			this.saving = true;
			this.error = "";

			try {
				if (this.circuitChanged) {
					if (this.circuit) {
						await api.post(`peakshavingcircuit/${encodeURIComponent(this.circuit)}`);
					} else {
						await api.delete("peakshavingcircuit");
					}
					this.initialCircuit = this.circuit;
				}
				if (this.phasesChanged && this.phases !== null) {
					await api.post(`lmadvanced/phases/${this.phases}`);
					this.initialPhases = this.phases;
				}

				this.$emit("changed");
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
