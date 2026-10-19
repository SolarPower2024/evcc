<template>
	<!-- load management overview, see core/site_lm_status.go -->
	<button
		v-if="hasLoadManagement"
		type="button"
		class="dropdown-item"
		data-testid="more-lm-overview"
		@click="open('lmOverviewModal')"
	>
		{{ $t("lmoverview.menu") }}
		<span v-if="lmOff" class="badge bg-warning text-dark ms-1" data-testid="more-lm-off">
			{{ $t("lmoverview.menuOff") }}
		</span>
	</button>
	<!-- monthly peak statistics, see core/site_peak_stats.go -->
	<button
		v-if="hasPeakShaving"
		type="button"
		class="dropdown-item"
		data-testid="more-peak-stats"
		@click="open('peakStatsModal')"
	>
		{{ $t("peakstats.menu") }}
	</button>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import Modal from "bootstrap/js/dist/modal";
import store from "@/store";
import { peakShavingSetUp } from "@/utils/peakShaving";
import { lmOffBadge } from "@/utils/lmOff";

// Custom extension: the fork's entries in the "more" menu. The dialogs they
// open are mounted once by GlobalModals.vue.
export default defineComponent({
	name: "LmMoreMenuItems",
	computed: {
		hasLoadManagement() {
			return !!store.state?.lmStatus;
		},
		lmOff() {
			return lmOffBadge(store.state);
		},
		hasPeakShaving() {
			return peakShavingSetUp(store.state) || !!store.state?.peakMonths?.length;
		},
	},
	methods: {
		open(id: string) {
			Modal.getOrCreateInstance(document.getElementById(id) as HTMLElement).show();
		},
	},
});
</script>
