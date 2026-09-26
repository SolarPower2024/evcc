<template>
	<div class="once border-top pt-3 mt-3" data-testid="battery-grid-charge-once">
		<h6 class="mb-2">{{ $t("battery.gridChargeOnce.title") }}</h6>

		<div v-if="running" class="d-flex flex-wrap align-items-center gap-2">
			<span data-testid="battery-grid-charge-once-status">
				{{ $t("battery.gridChargeOnce.running", { soc: fmtSoc(once.target) }) }} ·
				{{ whenText }} ·
				<span :class="once.active ? 'text-primary' : 'evcc-gray'">{{
					once.active
						? $t("battery.gridChargeOnce.active")
						: $t("battery.gridChargeOnce.waiting")
				}}</span>
			</span>
			<button
				type="button"
				class="btn btn-sm btn-outline-secondary ms-auto"
				data-testid="battery-grid-charge-once-cancel"
				:disabled="busy"
				@click="cancel"
			>
				{{ $t("battery.gridChargeOnce.cancel") }}
			</button>
		</div>

		<form v-else class="d-flex flex-wrap align-items-center gap-2" @submit.prevent="start">
			<i18n-t keypath="battery.gridChargeOnce.sentence" tag="span" scope="global">
				<template #soc>
					<InlineSocSelect
						id="batteryGridChargeOnceSoc"
						:options="socOptions"
						:selected="target"
						:label="fmtSoc(target)"
						nowrap
						@change="target = parseInt(($event.target as HTMLInputElement).value, 10)"
					/>
				</template>
			</i18n-t>
			<select
				id="batteryGridChargeOnceMode"
				v-model="mode"
				class="form-select form-select-sm w-auto"
				data-testid="battery-grid-charge-once-mode"
				:aria-label="$t('battery.gridChargeOnce.title')"
			>
				<option value="now">{{ $t("battery.gridChargeOnce.now") }}</option>
				<option value="until">{{ $t("battery.gridChargeOnce.until") }}</option>
			</select>
			<input
				v-if="mode === 'until'"
				id="batteryGridChargeOnceTime"
				v-model="time"
				type="time"
				class="form-control form-control-sm w-auto"
				data-testid="battery-grid-charge-once-time"
				:aria-label="$t('battery.gridChargeOnce.timeLabel')"
			/>
			<button
				type="submit"
				class="btn btn-sm btn-primary ms-auto"
				data-testid="battery-grid-charge-once-start"
				:disabled="busy || (mode === 'until' && !time)"
			>
				{{ $t("battery.gridChargeOnce.start") }}
			</button>
		</form>

		<p v-if="error" class="text-danger small mt-2 mb-0">{{ error }}</p>
		<p v-else class="small text-muted mt-2 mb-0">{{ $t("battery.gridChargeOnce.help") }}</p>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import formatter from "@/mixins/formatter";
import api from "@/api";
import store from "@/store";
import InlineSocSelect from "./InlineSocSelect.vue";

interface GridChargeOnce {
	target: number;
	until?: string;
	active?: boolean;
}

// Custom extension: one-time grid charging up to a soc, right away or by a time
// of day at the cheapest slots, see core/site_lm_once.go
export default defineComponent({
	name: "BatteryGridChargeOnce",
	components: { InlineSocSelect },
	mixins: [formatter],
	data() {
		return { target: 80, mode: "now", time: "06:00", busy: false, error: "" };
	},
	computed: {
		once(): GridChargeOnce {
			return store.state?.batteryGridChargeOnce || { target: 0 };
		},
		running(): boolean {
			return (this.once.target || 0) > 0;
		},
		soc(): number {
			return store.state?.battery?.soc || 0;
		},
		// only targets above the current soc make sense
		socOptions() {
			const options = [];
			for (let i = 100; i >= 10; i -= 5) {
				options.push({ value: i, name: this.fmtSoc(i), disabled: i <= this.soc });
			}
			return options;
		},
		whenText(): string {
			if (!this.once.until) return this.$t("battery.gridChargeOnce.now");
			return this.$t("battery.gridChargeOnce.byTime", {
				time: this.fmtHourMinute(new Date(this.once.until)),
			});
		},
	},
	methods: {
		fmtSoc(soc: number) {
			return this.fmtPercentage(soc);
		},
		async start() {
			this.busy = true;
			this.error = "";
			try {
				const path =
					this.mode === "until"
						? `batterygridchargeonce/${this.target}/${encodeURIComponent(this.time)}`
						: `batterygridchargeonce/${this.target}`;
				await api.post(path);
			} catch (e: any) {
				this.error = e?.response?.data?.error || e?.message || String(e);
			}
			this.busy = false;
		},
		async cancel() {
			this.busy = true;
			this.error = "";
			try {
				await api.delete("batterygridchargeonce");
			} catch (e: any) {
				this.error = e?.response?.data?.error || e?.message || String(e);
			}
			this.busy = false;
		},
	},
});
</script>
