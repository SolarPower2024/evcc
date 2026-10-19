<template>
	<div class="feedin-eeg mt-2 d-flex gap-2 align-items-baseline" data-testid="feedineeg-summary">
		<div class="label flex-grow-1">{{ $t("config.feedineeg.counterLabel") }}</div>
		<!-- set or not set as a link to the dialog, the entity as tooltip -->
		<button
			type="button"
			class="btn btn-link p-0 value"
			:class="{ missing: !entity }"
			:title="entity || undefined"
			data-testid="feedineeg-open"
			@click.stop="openModal('feedineeg')"
		>
			{{ entity ? $t("config.feedineeg.isSet") : $t("config.feedineeg.none") }}
		</button>
	</div>
</template>

<script>
import store from "@/store";
import { openModal } from "@/configModal";

// Custom extension: the EEG counter inside the second feed-in tariff's card, see
// core/site_feedin_eeg.go
export default {
	name: "FeedInEegSummary",
	computed: {
		entity() {
			return store.state?.feedInEegEntity;
		},
	},
	methods: { openModal },
};
</script>

<style scoped>
.value {
	font-weight: bold;
	color: var(--bs-primary);
}
.value.missing {
	color: var(--bs-danger);
}
</style>
