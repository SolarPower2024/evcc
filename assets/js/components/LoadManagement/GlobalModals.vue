<template>
	<OverviewModal />
	<PeakStatusModal />
</template>

<script lang="ts">
import { defineComponent } from "vue";
import OverviewModal from "./OverviewModal.vue";
import PeakStatusModal from "../Battery/PeakStatusModal.vue";

// Custom extension: the fork's dialogs available on every page, opened from the
// "more" menu (MoreMenuItems.vue)
export default defineComponent({
	name: "LmGlobalModals",
	components: { OverviewModal, PeakStatusModal },
});
</script>
