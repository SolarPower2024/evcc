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
				<template #when>
					<CustomSelect
						id="batteryGridChargeOnceWhen"
						:options="whenOptions"
						:selected="when"
						:aria-label="$t('battery.gridChargeOnce.whenLabel')"
						data-testid="battery-grid-charge-once-when"
						inline
						@change="when = ($event.target as HTMLSelectElement).value"
					>
						<span
							v-if="when === 'now'"
							class="text-decoration-underline fw-bold text-nowrap"
						>
							{{ $t("battery.gridChargeOnce.now") }}
						</span>
						<!-- "bis" stays with the time when the sentence wraps -->
						<i18n-t
							v-else
							keypath="battery.gridChargeOnce.until"
							tag="span"
							class="text-nowrap"
							scope="global"
						>
							<template #time>
								<span class="text-decoration-underline fw-bold">{{ when }}</span>
							</template>
						</i18n-t>
					</CustomSelect>
				</template>
			</i18n-t>
			<button
				type="submit"
				class="btn btn-sm btn-primary ms-auto"
				data-testid="battery-grid-charge-once-start"
				:disabled="busy"
			>
				{{ $t("battery.gridChargeOnce.start") }}
			</button>
		</form>

		<p v-if="error" class="text-danger small mt-2 mb-0">{{ error }}</p>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import formatter from "@/mixins/formatter";
import api from "@/api";
import store from "@/store";
import CustomSelect from "../Helper/CustomSelect.vue";
import InlineSocSelect from "./InlineSocSelect.vue";

interface GridChargeOnce {
	target: number;
	until?: string | null;
	active?: boolean;
}

// Custom extension: one-time grid charging up to a soc, right away or by a time
// of day at the cheapest slots, see core/site_lm_once.go
export default defineComponent({
	name: "BatteryGridChargeOnce",
	components: { CustomSelect, InlineSocSelect },
	mixins: [formatter],
	data() {
		// when: "now" or a time of day HH:MM
		return { target: 80, when: "now", busy: false, error: "" };
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
		// right away or by a time of day, every half hour
		whenOptions() {
			const options = [{ value: "now", name: this.$t("battery.gridChargeOnce.now") }];
			for (let m = 0; m < 24 * 60; m += 30) {
				const hh = String(Math.floor(m / 60)).padStart(2, "0");
				const time = `${hh}:${m % 60 ? "30" : "00"}`;
				options.push({
					value: time,
					name: this.$t("battery.gridChargeOnce.until", { time }),
				});
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
					this.when !== "now"
						? `batterygridchargeonce/${this.target}/${encodeURIComponent(this.when)}`
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
