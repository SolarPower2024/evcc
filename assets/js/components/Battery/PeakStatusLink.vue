<template>
	<span
		v-if="active"
		class="peak-link d-inline-flex align-items-center gap-2"
		role="button"
		tabindex="0"
		:aria-label="$t('battery.peakStatus.open')"
		data-testid="peak-status-link"
		@click="open"
		@keydown.enter.prevent="open"
		@keydown.space.prevent="open"
	>
		<slot />
		<shopicon-regular-info size="s" class="hint" aria-hidden="true"></shopicon-regular-info>
	</span>
	<slot v-else />
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/info";
import { defineComponent } from "vue";
import Modal from "bootstrap/js/dist/modal";
import store from "@/store";

// Custom extension: wraps the ring and the percentage of a battery card. With peak shaving set up it
// opens the peak status dialog (PeakStatusModal, mounted in the battery page);
// without it the content passes through unchanged. See core/site_peak_status.go.
export default defineComponent({
	name: "PeakStatusLink",
	computed: {
		active(): boolean {
			return !!store.state.peakStatus?.state;
		},
	},
	methods: {
		open() {
			const modal = document.getElementById("peakStatusModal");
			if (modal) Modal.getOrCreateInstance(modal).show();
		},
	},
});
</script>

<style scoped>
.peak-link {
	cursor: pointer;
	border-radius: 0.5rem;
}
.peak-link:focus-visible {
	outline: 2px solid var(--bs-primary);
	outline-offset: 2px;
}
/* right next to the percentage, on its line */
.hint {
	flex-shrink: 0;
	width: 14px;
	height: 14px;
	color: var(--evcc-gray);
	pointer-events: none;
}
</style>
