<template>
	<PeakShavingModal />
	<PeakShavingCircuitModal />
	<LmPrioritiesModal />
	<LmShedGuardModal />
	<LmAdvancedModal />
	<PeakTariffModal />
	<BatteryIdentModal />
	<LmProfilesModal />
	<FeedInEegModal />
	<GridChargeModal />
</template>

<script lang="ts">
import { defineComponent } from "vue";
import PeakShavingModal from "./PeakShavingModal.vue";
import PeakShavingCircuitModal from "./PeakShavingCircuitModal.vue";
import LmPrioritiesModal from "./LmPrioritiesModal.vue";
import LmShedGuardModal from "./LmShedGuardModal.vue";
import LmAdvancedModal from "./LmAdvancedModal.vue";
import PeakTariffModal from "./PeakTariffModal.vue";
import BatteryIdentModal from "./BatteryIdentModal.vue";
import LmProfilesModal from "./LmProfilesModal.vue";
import FeedInEegModal from "./FeedInEegModal.vue";
import GridChargeModal from "./GridChargeModal.vue";

// Custom extension: the dialogs of the load management details section and the
// EEG tariff, mounted once on the config page, see core/lm/README.md
export default defineComponent({
	name: "LmConfigModals",
	components: {
		PeakShavingModal,
		PeakShavingCircuitModal,
		LmPrioritiesModal,
		LmShedGuardModal,
		LmAdvancedModal,
		PeakTariffModal,
		BatteryIdentModal,
		LmProfilesModal,
		FeedInEegModal,
		GridChargeModal,
	},
});
</script>
