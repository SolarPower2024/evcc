<template>
	<OverviewModal />
	<StatsModal />
</template>

<script lang="ts">
import { defineComponent } from "vue";
import OverviewModal from "./OverviewModal.vue";
import StatsModal from "../PeakShaving/StatsModal.vue";

// Custom extension: the fork's dialogs available on every page, opened from the
// "more" menu (MoreMenuItems.vue)
export default defineComponent({
	name: "LmGlobalModals",
	components: { OverviewModal, StatsModal },
});
</script>
