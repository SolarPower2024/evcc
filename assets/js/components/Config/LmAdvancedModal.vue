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
			<!-- custom: the load management (peak) circuit, see core/site_lm_switch.go -->
			<FormRow
				id="lmAdvanced-circuit"
				:label="$t('config.lmadvanced.circuitLabel')"
				:help="$t('config.lmadvanced.circuitHelp')"
			>
				<select
					id="lmAdvanced-circuit"
					v-model="circuit"
					class="form-select"
					data-testid="lmadvanced-circuit"
				>
					<option value="">{{ $t("config.lmadvanced.circuitAll") }}</option>
					<option v-for="c in circuitOptions" :key="c.name" :value="c.name">
						{{ c.title }}
					</option>
				</select>
			</FormRow>

			<FormRow
				v-for="field in numberFields"
				:id="`lmAdvanced-${field.name}`"
				:key="field.name"
				:label="$t(`config.lmadvanced.${field.name}Label`)"
				:help="$t(`config.lmadvanced.${field.name}Help`)"
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

			<!-- custom: home consumption forecast for the optimizer, see core/site_load_weekday.go and core/site_load_manual.go -->
			<FormRow
				id="lmAdvanced-homeForecast"
				:label="$t('config.lmadvanced.homeForecastLabel')"
				:help="$t('config.lmadvanced.homeForecastHelp')"
			>
				<select
					id="lmAdvanced-homeForecast"
					v-model.number="values.homeForecast"
					class="form-select"
					data-testid="lmadvanced-homeForecast"
				>
					<option :value="0">{{ $t("config.lmadvanced.homeForecastEvcc") }}</option>
					<option :value="1">{{ $t("config.lmadvanced.homeForecastWeekday") }}</option>
					<option :value="2">{{ $t("config.lmadvanced.homeForecastManual") }}</option>
				</select>
			</FormRow>
			<FormRow
				v-if="values.homeForecast === 2"
				id="lmAdvanced-homeProfile"
				:label="$t('config.lmadvanced.homeProfileLabel')"
				:help="$t('config.lmadvanced.homeProfileHelp')"
			>
				<p v-if="homeProfile" class="small mb-2" data-testid="lmadvanced-homeProfile-info">
					{{ homeProfileInfo }}
					<a :href="homeProfileUrl" download="lastprofil.csv">{{
						$t("config.lmadvanced.homeProfileDownload")
					}}</a>
					·
					<button
						type="button"
						class="btn btn-link btn-sm p-0 align-baseline text-danger"
						data-testid="lmadvanced-homeProfile-delete"
						@click="deleteHomeProfile"
					>
						{{ $t("config.lmadvanced.homeProfileDelete") }}
					</button>
				</p>
				<p v-else class="small text-warning mb-2" data-testid="lmadvanced-homeProfile-none">
					{{ $t("config.lmadvanced.homeProfileNone") }}
				</p>
				<input
					id="lmAdvanced-homeProfile"
					ref="homeProfileFile"
					type="file"
					accept=".csv,.txt,text/csv,text/plain"
					class="form-control"
					data-testid="lmadvanced-homeProfile"
					:disabled="uploading"
					@change="uploadHomeProfile"
				/>
			</FormRow>
			<FormRow
				id="lmAdvanced-percentile"
				:label="$t('config.lmadvanced.percentileLabel')"
				:help="$t('config.lmadvanced.percentileHelp')"
			>
				<select
					id="lmAdvanced-percentile"
					v-model.number="percentile"
					class="form-select"
					data-testid="lmadvanced-percentile"
				>
					<option v-for="p in percentiles" :key="p" :value="p">
						{{
							p
								? $t("config.lmadvanced.percentileValue", { percentile: p })
								: $t("config.lmadvanced.percentileAverage")
						}}
					</option>
				</select>
			</FormRow>

			<!-- custom: export forecast of the optimizer to Home Assistant, see core/site_lm_export_forecast.go -->
			<FormRow
				id="lmAdvanced-exportForecast"
				:label="$t('config.lmadvanced.exportForecastLabel')"
				:help="$t('config.lmadvanced.exportForecastHelp')"
			>
				<input
					id="lmAdvanced-exportForecast"
					v-model.trim="exportForecast"
					type="text"
					class="form-control"
					placeholder="sensor.evcc_einspeiseprognose"
					autocomplete="off"
					data-testid="lmadvanced-exportForecast"
					@input="exportForecastError = ''"
				/>
				<p
					v-if="exportForecastError"
					class="text-danger small mt-1 mb-0"
					data-testid="lmadvanced-exportForecast-error"
				>
					{{ exportForecastError }}
				</p>
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
					:disabled="
						saving ||
						(!changed.length &&
							!circuitChanged &&
							!percentileChanged &&
							!exportForecastChanged)
					"
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

// Ranges as checked by the server, see core/site_lm_advanced.go. The inputs have
// no min, max or step: the browser would block the submit without a message.
const FIELDS = [
	{ name: "hysteresis", unit: "%", min: 0, max: 20, integer: false, default: 2 },
	{ name: "freeValue", unit: "W", min: 1, max: 100000, integer: true, default: 10000 },
	{ name: "holdOff", unit: "min", min: 1, max: 60, integer: true, default: 5 },
	{ name: "timeout", unit: "min", min: 1, max: 60, integer: true, default: 10 },
	{ name: "peakFreeze", unit: "min", min: 1, max: 14, integer: true, default: 12 },
	{ name: "peakCap", unit: "×", min: 1, max: 10, integer: false, default: 2 },
	{ name: "gridChargeWindow", unit: "h", min: 1, max: 24, integer: true, default: 3 },
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
	mixins: [formatter],
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			values: {},
			initial: {},
			circuit: "",
			initialCircuit: "",
			percentile: 0,
			initialPercentile: 0,
			exportForecast: "",
			initialExportForecast: "",
			exportForecastError: "",
			uploading: false,
		};
	},
	computed: {
		numberFields() {
			return FIELDS;
		},
		percentiles() {
			// upstream profilePercentile in %, 0 = average
			const res = [0, 60, 70, 80, 90];
			return res.includes(this.initialPercentile)
				? res
				: [...res, this.initialPercentile].sort((a, b) => a - b);
		},
		// uploaded load profile, see core/site_load_manual.go
		homeProfile() {
			return store.state?.lmHomeProfile || null;
		},
		homeProfileInfo() {
			const p = this.homeProfile;
			const months =
				p.months?.length === 12
					? this.$t("config.lmadvanced.homeProfileAllMonths")
					: (p.months || []).join(", ");
			return this.$t("config.lmadvanced.homeProfileInfo", {
				name: p.name,
				date: this.fmtDayMonthYear(new Date(p.uploaded)),
				months,
			});
		},
		homeProfileUrl() {
			return `${api.defaults.baseURL}lmhomeprofile`;
		},
		percentileChanged() {
			return this.percentile !== this.initialPercentile;
		},
		exportForecastChanged() {
			return this.exportForecast !== this.initialExportForecast;
		},
		circuitChanged() {
			return this.circuit !== this.initialCircuit;
		},
		// circuits with a power limit, configured or changed at runtime
		circuitOptions() {
			const configured = store.state?.lmOff?.limits || {};
			return Object.entries(store.state?.circuits || {})
				.filter(([name, c]) => c.maxPower > 0 || configured[name] > 0)
				.map(([name, c]) => ({
					name,
					title: `${c.title || name} (${this.fmtW(configured[name] || c.maxPower)})`,
				}));
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
			values.homeForecast = state.homeForecast ?? (state.homeWeekday ? 1 : 0);

			this.saving = false;
			this.error = "";
			this.values = { ...values };
			this.initial = { ...values };
			this.circuit = store.state?.lmOff?.circuit || "";
			this.initialCircuit = this.circuit;
			this.percentile = store.state?.profilePercentile ?? 0;
			this.initialPercentile = this.percentile;
			this.exportForecast = state.exportForecastEntity || "";
			this.initialExportForecast = this.exportForecast;
			this.exportForecastError = "";
		},
		async uploadHomeProfile(event) {
			const file = event.target.files?.[0];
			if (!file) return;

			this.uploading = true;
			this.error = "";
			try {
				await api.post("lmhomeprofile", await file.text(), {
					params: { name: file.name },
					headers: { "Content-Type": "text/csv" },
				});
			} catch (e) {
				this.error = this.$t("config.lmadvanced.homeProfileInvalid", {
					error: e?.response?.data?.error || e.message,
				});
			}
			this.uploading = false;
			event.target.value = "";
		},
		async deleteHomeProfile() {
			this.error = "";
			try {
				await api.delete("lmhomeprofile");
			} catch (e) {
				this.error = e?.response?.data?.error || e.message;
			}
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

			this.exportForecastError = "";
			if (this.exportForecast && !/^sensor\.[a-z0-9_]+$/.test(this.exportForecast)) {
				this.exportForecastError = this.$t("config.lmadvanced.exportForecastInvalid");
				return;
			}

			this.saving = true;
			this.error = "";

			try {
				if (this.circuitChanged) {
					if (this.circuit) {
						await api.post(`lmcircuit/${encodeURIComponent(this.circuit)}`);
					} else {
						await api.delete("lmcircuit");
					}
				}
				if (this.percentileChanged) {
					if (this.percentile) {
						await api.post(`profilepercentile/${this.percentile}`);
					} else {
						await api.delete("profilepercentile");
					}
				}
				for (const name of this.changed) {
					await api.post(`lmadvanced/${name}/${this.values[name]}`);
				}
			} catch (e) {
				this.error = e?.response?.data?.error || e.message;
				this.saving = false;
				return;
			}

			// the others are saved: only the export field is left to save
			this.initial = { ...this.values };
			this.initialCircuit = this.circuit;
			this.initialPercentile = this.percentile;
			this.$emit("changed");

			// last, as only this one can fail on the connection to Home Assistant
			try {
				if (this.exportForecastChanged) {
					if (this.exportForecast) {
						await api.post(
							`lmexportforecast/${encodeURIComponent(this.exportForecast)}`
						);
					} else {
						await api.delete("lmexportforecast");
					}
					this.initialExportForecast = this.exportForecast;
				}
				this.$refs.modal.close();
			} catch (e) {
				this.exportForecastError = e?.response?.data?.error || e.message;
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
