<template>
	<!-- peak shaving and load management dialog, see core/site_peak_status.go -->
	<button
		v-if="hasPeakDialog"
		type="button"
		class="dropdown-item"
		data-testid="more-peak-status"
		@click="open('peakStatusModal')"
	>
		{{ $t(peakTitleKey) }}
	</button>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import Modal from "bootstrap/js/dist/modal";
import store from "@/store";
import { dialogTitleKey, dialogVisible } from "../Battery/peakDialog";

// Custom extension: the fork's entries in the "more" menu. The dialogs they
// open are mounted once by GlobalModals.vue.
export default defineComponent({
	name: "LmMoreMenuItems",
	computed: {
		hasPeakDialog() {
			// peak shaving set up, also while off, or loads in load management
			return dialogVisible(store.state);
		},
		peakTitleKey() {
			return dialogTitleKey(store.state);
		},
	},
	methods: {
		open(id: string) {
			Modal.getOrCreateInstance(document.getElementById(id) as HTMLElement).show();
		},
	},
});
</script>
