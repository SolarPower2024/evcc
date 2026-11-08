<template>
	<section data-testid="peak-status-loads">
		<h6 class="section-title">{{ $t("lmoverview.loads") }}</h6>
		<p v-if="optimizerLoads" class="small evcc-gray mb-2" data-testid="lm-optimizer-hint">
			{{ $t("lmoverview.optimizerHint") }}
		</p>
		<div class="table-responsive">
			<table class="table table-sm align-middle mb-0">
				<tbody>
					<tr v-for="l in loads" :key="l.name" :data-testid="`lm-load-${l.name}`">
						<td>
							<span class="d-inline-flex align-items-center text-break">
								{{ l.battery ? $t("lmoverview.battery") : l.title || l.name }}
								<shopicon-regular-lock
									v-if="l.protected && l.state === 'shed'"
									size="s"
									class="evcc-gray lock"
									:title="$t('lmoverview.protected')"
								></shopicon-regular-lock>
								<!-- custom: the optimizer decides, load management limits, see core/site_lm_status.go -->
								<span
									v-if="l.optimizer"
									class="badge rounded-pill text-bg-light ms-1"
									data-testid="lm-optimizer-badge"
									>{{ $t("lmoverview.optimizer") }}</span
								>
							</span>
						</td>
						<td class="text-end text-nowrap">{{ fmt.w(l.power) }}</td>
						<td class="text-end">
							<span class="pill" :class="`pill-${loadTone(l)}`">{{
								stateText(l)
							}}</span>
						</td>
					</tr>
				</tbody>
			</table>
		</div>
	</section>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import "@h2d2/shopicons/es/regular/lock";
import store from "@/store";
import formatter from "@/mixins/formatter";
import type { LmLoadStatus } from "@/types/evcc-lm";
import { makeFmt } from "./peakStatus";
import { loadStateText, loadTone, sortedLoads } from "./peakDialog";

// Custom extension: the loads of the load management with their state, in the
// peak shaving dialog (PeakStatusModal.vue), see core/site_lm_status.go
export default defineComponent({
	name: "PeakStatusLoads",
	mixins: [formatter],
	computed: {
		loads(): LmLoadStatus[] {
			return sortedLoads(store.state.lmStatus?.loads);
		},
		// the optimizer decides some of the loads, load management limits them
		optimizerLoads(): boolean {
			return this.loads.some((l) => l.optimizer);
		},
		fmt() {
			return makeFmt(
				(watt: number) => this.fmtW(watt),
				(percent: number) => this.fmtPercentage(percent),
				(watt: number) => this.fmtW(watt, this.POWER_UNIT.KW, false)
			);
		},
	},
	methods: {
		loadTone,
		stateText(l: LmLoadStatus): string {
			return loadStateText(
				l,
				(key, params) => this.$t(key, params as Record<string, string>),
				this.fmt,
				(iso) => this.fmtHourMinute(new Date(iso))
			);
		},
	},
});
</script>

<style scoped>
.section-title {
	font-size: 0.75rem;
	font-weight: normal;
	text-transform: uppercase;
	color: var(--evcc-gray);
	margin-bottom: 0.35rem;
}
.pill {
	display: inline-block;
	font-size: 0.8rem;
	padding: 0.15rem 0.6rem;
	border-radius: 1rem;
	white-space: normal;
}
.pill-success {
	color: var(--bs-success-text-emphasis);
	background: var(--bs-success-bg-subtle);
}
.pill-warning {
	color: var(--bs-warning-text-emphasis);
	background: var(--bs-warning-bg-subtle);
}
.pill-danger {
	color: var(--bs-danger-text-emphasis);
	background: var(--bs-danger-bg-subtle);
}
.pill-info {
	color: var(--bs-info-text-emphasis);
	background: var(--bs-info-bg-subtle);
}
.pill-muted {
	color: var(--evcc-gray);
	background: var(--bs-secondary-bg);
}
.lock {
	vertical-align: -2px;
	margin-left: 0.25rem;
}
</style>
