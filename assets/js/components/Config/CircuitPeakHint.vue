<template>
	<p v-if="limit !== undefined" class="small evcc-gray" data-testid="circuit-from-peak-hint">
		{{ $t("config.circuit.fromPeakHint", { limit: fmtW(limit) }) }}
	</p>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import type { ConfigCircuit } from "@/types/evcc";
import formatter from "@/mixins/formatter";
import store from "@/store";
import { circuitPeakHintLimit } from "@/utils/circuitFromPeak";

// Custom extension: hint in the dialog of the load management (peak) circuit
// while its power limit is replaced by the peak limit, see core/site_lm_switch.go.
// Mounted in CircuitModal.vue; shows nothing for any other circuit.
export default defineComponent({
	name: "CircuitPeakHint",
	mixins: [formatter],
	props: {
		id: { type: Number },
		circuits: { type: Array as PropType<ConfigCircuit[]>, default: () => [] },
	},
	computed: {
		limit(): number | undefined {
			const name = this.circuits.find((c) => c.id === this.id)?.name;
			return circuitPeakHintLimit(store.state, name);
		},
	},
});
</script>
