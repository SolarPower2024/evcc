<template>
	<svg
		width="20"
		height="20"
		viewBox="0 0 24 24"
		fill="none"
		stroke="currentColor"
		stroke-width="2.4"
		stroke-linecap="round"
		aria-hidden="true"
	>
		<path d="M12 3v8" />
		<path d="M7.05 6.05a7 7 0 1 0 9.9 0" />
	</svg>
</template>

<script lang="ts">
import { defineComponent } from "vue";

// Custom extension: power symbol for the one-time grid charge button, evcc has none
export default defineComponent({
	name: "PowerIcon",
});
</script>
