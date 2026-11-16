<template>
	<div
		v-if="text"
		class="extraValue text-nowrap ms-sm-1"
		:title="$t('planPower.title')"
		data-testid="plan-power-hint"
	>
		{{ text }}
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";

// what limits the power a plan is made with, see core/lm/README.md (charge planner power)
export default defineComponent({
	name: "PlanPowerHint",
	props: {
		reason: String,
	},
	computed: {
		text(): string | null {
			if (this.reason === "peak" || this.reason === "circuit") {
				return this.$t(`planPower.${this.reason}`);
			}
			return null;
		},
	},
});
</script>

<style scoped>
.extraValue {
	color: var(--evcc-gray);
	font-size: 14px;
}
</style>
