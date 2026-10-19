<template>
	<PeakShavingModal />
	<BatteryConfigModal />
	<LmPrioritiesModal />
	<LmShedGuardModal />
	<LmAdvancedModal />
	<PeakTariffModal />
	<LmProfilesModal />
	<FeedInEegModal />
</template>

<script lang="ts">
import { defineComponent } from "vue";
import PeakShavingModal from "./PeakShavingModal.vue";
import BatteryConfigModal from "./BatteryConfigModal.vue";
import LmPrioritiesModal from "./LmPrioritiesModal.vue";
import LmShedGuardModal from "./LmShedGuardModal.vue";
import LmAdvancedModal from "./LmAdvancedModal.vue";
import PeakTariffModal from "./PeakTariffModal.vue";
import LmProfilesModal from "./LmProfilesModal.vue";
import FeedInEegModal from "./FeedInEegModal.vue";

// Custom extension: the dialogs of the load management details section and the
// EEG tariff, mounted once on the config page, see core/lm/README.md
export default defineComponent({
	name: "LmConfigModals",
	components: {
		PeakShavingModal,
		BatteryConfigModal,
		LmPrioritiesModal,
		LmShedGuardModal,
		LmAdvancedModal,
		PeakTariffModal,
		LmProfilesModal,
		FeedInEegModal,
	},
});
</script>
