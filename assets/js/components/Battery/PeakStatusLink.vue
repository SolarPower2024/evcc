<template>
	<div
		v-if="active"
		class="peak-link"
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
	</div>
	<slot v-else />
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/info";
import { defineComponent } from "vue";
import Modal from "bootstrap/js/dist/modal";
import store from "@/store";

// Custom extension: wraps the soc of a battery card. With peak shaving set up it
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
	position: relative;
	min-width: 0;
	cursor: pointer;
	border-radius: 0.5rem;
}
.peak-link:focus-visible {
	outline: 2px solid var(--bs-primary);
	outline-offset: 2px;
}
/* the hint sits at the right of the label line */
.hint {
	position: absolute;
	right: 0.25rem;
	top: 0.3rem;
	width: 14px;
	height: 14px;
	color: var(--evcc-gray);
	pointer-events: none;
}
</style>
