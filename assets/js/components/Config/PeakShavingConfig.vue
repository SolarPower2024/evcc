<template>
	<div class="group round-box p-4">
		<p class="text-muted mb-3">{{ $t("config.peakshaving.description") }}</p>

		<label class="form-label" for="peakShavingEntity">
			{{ $t("config.peakshaving.entityLabel") }}
		</label>
		<input
			id="peakShavingEntity"
			v-model="entity"
			type="text"
			class="form-control"
			:class="{ 'is-invalid': error }"
			placeholder="input_number.battery_peak_power"
			data-testid="peakshaving-entity"
			@change="save"
		/>
		<div v-if="error" class="invalid-feedback d-block">{{ error }}</div>
		<div v-else-if="saved" class="form-text text-success">
			{{ $t("config.peakshaving.saved") }}
		</div>
		<div v-else class="form-text">{{ $t("config.peakshaving.entityHelp") }}</div>

		<p class="text-muted mt-4 mb-0 small">{{ $t("config.peakshaving.hint") }}</p>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import store from "@/store";
import api from "@/api";

// The peak shaving target entity. The switch, the peak limit and the reserve soc
// are operating controls and live on the battery page instead.
export default defineComponent({
	name: "PeakShavingConfig",
	data() {
		return {
			entity: "",
			error: "",
			saved: false,
		};
	},
	computed: {
		configured(): string {
			return store.state.peakShavingEntity ?? "";
		},
	},
	watch: {
		configured: {
			handler(v: string) {
				this.entity = v;
			},
			immediate: true,
		},
	},
	methods: {
		async save() {
			const value = this.entity.trim();
			this.error = "";
			this.saved = false;

			try {
				if (value) {
					await api.post(`peakshavingentity/${encodeURIComponent(value)}`);
				} else {
					await api.delete("peakshavingentity");
				}
				this.saved = true;
			} catch (err: any) {
				// the backend rejects a non-number entity and an unreachable
				// target, so surface that instead of keeping a dead setting
				this.error =
					err?.response?.data?.error || this.$t("config.peakshaving.entityInvalid");
				this.entity = this.configured;
				console.error(err);
			}
		},
	},
});
</script>
