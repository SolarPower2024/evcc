<template>
	<GenericModal
		id="batteryIdentModal"
		ref="modal"
		:title="$t('config.batteryident.title')"
		data-testid="batteryident-modal"
		config-modal-name="batteryident"
		@open="open"
	>
		<p>{{ $t("config.batteryident.description") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>

		<p v-if="!batteries.length" class="text-muted">{{ $t("config.batteryident.none") }}</p>
		<div v-for="b in batteries" :key="b.name" class="mb-3" data-testid="batteryident-battery">
			<h6 v-if="batteries.length > 1" class="mb-2">{{ b.title || b.name }}</h6>
			<div class="d-flex justify-content-between">
				<span class="evcc-gray">{{ $t("config.batteryident.configured") }}</span>
				<span>{{ fmtKWh(b.configured) }}</span>
			</div>
			<div class="d-flex justify-content-between">
				<span class="evcc-gray">{{ $t("config.batteryident.capacity") }}</span>
				<span data-testid="batteryident-capacity">{{
					b.capacity ? capacityText(b) : "–"
				}}</span>
			</div>
			<div class="d-flex justify-content-between">
				<span class="evcc-gray">{{ $t("config.batteryident.efficiency") }}</span>
				<span data-testid="batteryident-efficiency">{{
					b.efficiency ? fmtPercentage(b.efficiency * 100) : "–"
				}}</span>
			</div>
			<p class="small evcc-gray mt-1 mb-0" data-testid="batteryident-status">
				{{ statusText(b) }}
			</p>
		</div>

		<form class="container mx-0 px-0" @submit.prevent="save">
			<div class="form-check form-switch mt-3">
				<input
					id="batteryIdentUse"
					v-model="use"
					class="form-check-input"
					type="checkbox"
					role="switch"
					data-testid="batteryident-use"
				/>
				<label class="form-check-label" for="batteryIdentUse">
					{{ $t("config.batteryident.use") }}
				</label>
			</div>
			<p class="small evcc-gray">{{ $t("config.batteryident.useHelp") }}</p>

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
					:disabled="saving || use === initialUse"
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
import formatter from "@/mixins/formatter";
import store from "@/store";
import api from "@/api";

// Custom extension: capacity and efficiency learned from the stored battery
// slots, see core/site_battery_ident.go. Needs 3 charging and 3 discharging
// runs over 20 % soc within 60 days.
const MIN_RUNS = 3;

export default {
	name: "BatteryIdentModal",
	components: { GenericModal },
	mixins: [formatter],
	emits: ["changed"],
	data() {
		return { saving: false, error: "", use: false, initialUse: false };
	},
	computed: {
		state() {
			return store.state?.batteryIdent;
		},
		batteries() {
			return this.state?.batteries || [];
		},
	},
	methods: {
		open() {
			this.saving = false;
			this.error = "";
			this.use = !!this.state?.use;
			this.initialUse = this.use;
		},
		fmtKWh(v) {
			return v ? `${this.fmtNumber(v, 1)} kWh` : "–";
		},
		capacityText(b) {
			const share = b.configured
				? ` (${this.fmtPercentage((b.capacity / b.configured) * 100)})`
				: "";
			return `${this.fmtKWh(b.capacity)}${share}`;
		},
		statusText(b) {
			const runs = { charges: b.charges, discharges: b.discharges, min: MIN_RUNS };
			if (b.valid) return this.$t("config.batteryident.valid", runs);
			if (b.capacity) return this.$t("config.batteryident.implausible", runs);
			return this.$t("config.batteryident.learning", runs);
		},
		async save() {
			this.saving = true;
			this.error = "";
			try {
				await api.post(`batteryidentuse/${this.use}`);
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
