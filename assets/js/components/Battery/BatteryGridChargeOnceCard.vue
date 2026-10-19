<template>
	<Card data-testid="battery-grid-charge-once-card">
		<BatteryGridChargeOnce class="once-alone" />
	</Card>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import Card from "../Helper/Card.vue";
import BatteryGridChargeOnce from "./BatteryGridChargeOnce.vue";

// One-time grid charging as a card of its own: with grid charging by price only
// the soc card is hidden, see core/site_lm_grid_mode.go. Otherwise it is part of
// the soc card.
export default defineComponent({
	name: "BatteryGridChargeOnceCard",
	components: { Card, BatteryGridChargeOnce },
});
</script>

<style scoped>
/* the card has nothing above it: no divider and no gap */
.once-alone {
	border-top: 0 !important;
	margin-top: 0 !important;
	padding-top: 0 !important;
}
</style>
