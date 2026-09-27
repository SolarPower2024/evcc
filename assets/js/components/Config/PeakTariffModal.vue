<template>
	<GenericModal
		id="peakTariffModal"
		ref="modal"
		:title="$t('config.peaktariff.title')"
		data-testid="peaktariff-modal"
		config-modal-name="peaktariff"
		@open="open"
	>
		<p>{{ $t("config.peaktariff.description") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>

		<form ref="form" class="container mx-0 px-0" @submit.prevent="save">
			<div class="form-check form-switch mb-3">
				<input
					id="peakTariffEnabled"
					v-model="enabled"
					class="form-check-input"
					type="checkbox"
					role="switch"
					data-testid="peaktariff-enabled"
				/>
				<label class="form-check-label" for="peakTariffEnabled">
					{{ $t("config.peaktariff.enable") }}
				</label>
			</div>

			<template v-if="enabled">
				<FormRow
					v-for="field in fields"
					:id="`peakTariff-${field.name}`"
					:key="field.name"
					:label="$t(`config.peaktariff.${field.name}Label`)"
					:help="$t(`config.peaktariff.${field.name}Help`)"
				>
					<div class="input-group">
						<input
							:id="`peakTariff-${field.name}`"
							v-model.number="values[field.name]"
							type="number"
							step="any"
							class="form-control"
							:data-testid="`peaktariff-${field.name}`"
						/>
						<span class="input-group-text">{{ unit(field) }}</span>
					</div>
				</FormRow>
			</template>

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
import formatter from "@/mixins/formatter";
import store from "@/store";
import api from "@/api";

// Ranges as checked by the server, see core/site_peak_tariff.go. Defaults: the
// Austrian draft for 2027 (33.82 per kW and year up to 10 kW, double above, at
// least 20% of the agreed power and 2 kW).
const FIELDS = [
	{ name: "price", unit: "year", min: 0, max: 1000, default: 33.82 },
	{ name: "threshold", unit: "kW", min: 0, max: 1000, default: 10 },
	{ name: "priceAbove", unit: "year", min: 0, max: 2000, default: 67.64 },
	{ name: "agreed", unit: "kW", min: 0, max: 1000, default: 0 },
	{ name: "minShare", unit: "%", min: 0, max: 100, default: 20 },
	{ name: "minimum", unit: "kW", min: 0, max: 1000, default: 2 },
];

// Custom extension: the capacity tariff that values the month's peak, see
// core/site_peak_tariff.go. Off is a zero price.
export default {
	name: "PeakTariffModal",
	components: { FormRow, GenericModal },
	mixins: [formatter],
	emits: ["changed"],
	data() {
		return { saving: false, error: "", enabled: false, values: {}, initial: {} };
	},
	computed: {
		fields() {
			return FIELDS;
		},
		// everything to send: all values when switched on, the prices when off
		changed() {
			if (!this.enabled) {
				return this.initial.enabled ? ["price", "priceAbove"] : [];
			}
			if (!this.initial.enabled) return FIELDS.map((f) => f.name);
			return FIELDS.map((f) => f.name).filter((n) => this.values[n] !== this.initial[n]);
		},
	},
	methods: {
		unit(field) {
			if (field.unit !== "year") return field.unit;
			return this.$t("config.peaktariff.perKwYear", {
				currency: this.fmtCurrencySymbol(store.state?.currency),
			});
		},
		open() {
			const t = store.state?.peakTariff || {};
			const enabled = (t.price || 0) > 0 || (t.priceAbove || 0) > 0;
			const values = {};
			FIELDS.forEach((f) => (values[f.name] = enabled ? (t[f.name] ?? 0) : f.default));

			this.saving = false;
			this.error = "";
			this.enabled = enabled;
			this.values = { ...values };
			this.initial = { ...values, enabled };
		},
		invalidField() {
			return FIELDS.find((f) => {
				const v = this.values[f.name];
				return typeof v !== "number" || Number.isNaN(v) || v < f.min || v > f.max;
			});
		},
		async save() {
			const invalid = this.enabled && this.invalidField();
			if (invalid) {
				this.error = this.$t("config.peaktariff.invalid", {
					label: this.$t(`config.peaktariff.${invalid.name}Label`),
					min: invalid.min,
					max: invalid.max,
				});
				return;
			}

			this.saving = true;
			this.error = "";

			try {
				for (const name of this.changed) {
					const value = this.enabled ? this.values[name] : 0;
					await api.post(`peaktariff/${name}/${value}`);
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
