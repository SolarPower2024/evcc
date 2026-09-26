<template>
	<Card :title="$t('batterySettings.socGridChargeTab')" :subtitle="subtitle">
		<div class="form-check form-switch mb-3">
			<input
				id="batterySocGridCharge"
				:checked="enabled"
				class="form-check-input"
				type="checkbox"
				role="switch"
				data-testid="battery-soc-grid-charge-switch"
				@change="changeEnabled"
			/>
			<label class="form-check-label" for="batterySocGridCharge">
				{{ $t("battery.socGridCharge.enable") }}
			</label>
		</div>

		<div class="d-flex gap-3">
			<shopicon-regular-batterythreequarters
				size="s"
				class="text-primary flex-shrink-0 mt-1"
			></shopicon-regular-batterythreequarters>
			<i18n-t keypath="battery.socGridCharge.range" tag="p" class="mb-0" scope="global">
				<template #start>
					<InlineSocSelect
						id="batterySocGridChargeStart"
						:options="startOptions"
						:selected="selectedStart"
						:label="fmtSoc(selectedStart)"
						nowrap
						@change="changeStart"
					/>
				</template>
				<template #stop>
					<InlineSocSelect
						id="batterySocGridChargeStop"
						:options="stopOptions"
						:selected="selectedStop"
						:label="fmtSoc(selectedStop)"
						nowrap
						@change="changeStop"
					/>
				</template>
			</i18n-t>
		</div>
		<!-- custom: one-time grid charging, see core/site_lm_once.go -->
		<BatteryGridChargeOnce />
		<p
			v-if="optimizer"
			class="small text-muted mt-3 mb-0"
			data-testid="battery-soc-grid-charge-optimizer"
		>
			{{ $t("battery.socGridCharge.optimizer") }}
		</p>
	</Card>
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/batterythreequarters";
import { defineComponent } from "vue";
import formatter from "@/mixins/formatter";
import api from "@/api";
import Card from "../Helper/Card.vue";
import InlineSocSelect from "./InlineSocSelect.vue";
import BatteryGridChargeOnce from "./BatteryGridChargeOnce.vue";

// Soc-based grid charging: charge the home battery from the grid between a start
// and a stop soc, independent of the price-based grid charge limit.
export default defineComponent({
	name: "BatterySocGridChargeCard",
	components: { Card, InlineSocSelect, BatteryGridChargeOnce },
	mixins: [formatter],
	props: {
		enabled: Boolean,
		active: Boolean,
		startSoc: { type: Number, default: 20 },
		stopSoc: { type: Number, default: 80 },
		// the optimizer in automatic mode plans the charging, see core/site_optimizer_lm.go
		optimizer: Boolean,
	},
	data() {
		return {
			selectedStart: 20,
			selectedStop: 80,
		};
	},
	computed: {
		subtitle(): string {
			const key = this.active ? "active" : this.enabled ? "waiting" : "off";
			return this.$t(`battery.socGridCharge.${key}`);
		},
		// the backend rejects a start at or above the stop, so those values are not offered
		startOptions() {
			const options = [];
			for (let i = 95; i >= 0; i -= 5) {
				options.push({ value: i, name: this.fmtSoc(i), disabled: i >= this.selectedStop });
			}
			return options;
		},
		stopOptions() {
			const options = [];
			for (let i = 100; i >= 5; i -= 5) {
				options.push({ value: i, name: this.fmtSoc(i), disabled: i <= this.selectedStart });
			}
			return options;
		},
	},
	watch: {
		startSoc: {
			handler(soc) {
				this.selectedStart = soc;
			},
			immediate: true,
		},
		stopSoc: {
			handler(soc) {
				this.selectedStop = soc;
			},
			immediate: true,
		},
	},
	methods: {
		async changeEnabled(e: Event) {
			const target = e.target as HTMLInputElement;
			try {
				await api.post(`batterysocgridcharge/${target.checked}`);
			} catch (err) {
				target.checked = this.enabled; // revert to stay in sync with state
				console.error(err);
			}
		},
		async changeStart($event: Event) {
			const soc = parseInt(($event.target as HTMLInputElement).value, 10);
			const previous = this.selectedStart;
			this.selectedStart = soc;
			try {
				await api.post(`batterysocgridchargestart/${encodeURIComponent(soc)}`);
			} catch (err) {
				this.selectedStart = previous;
				console.error(err);
			}
		},
		async changeStop($event: Event) {
			const soc = parseInt(($event.target as HTMLInputElement).value, 10);
			const previous = this.selectedStop;
			this.selectedStop = soc;
			try {
				await api.post(`batterysocgridchargestop/${encodeURIComponent(soc)}`);
			} catch (err) {
				this.selectedStop = previous;
				console.error(err);
			}
		},
		fmtSoc(soc: number) {
			return this.fmtPercentage(soc);
		},
	},
});
</script>
