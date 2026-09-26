<template>
	<div class="feedin-eeg mt-2" data-testid="feedineeg-summary">
		<div class="d-flex gap-2">
			<div class="label flex-grow-1">{{ $t("config.feedineeg.counterLabel") }}</div>
			<div class="value text-end" :class="{ missing: !entity }">
				{{ entity || $t("config.feedineeg.none") }}
			</div>
		</div>
		<button
			type="button"
			class="btn btn-link btn-sm p-0 mt-1"
			data-testid="feedineeg-open"
			@click.stop="openModal('feedineeg')"
		>
			{{ $t("config.feedineeg.set") }}
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
.feedin-eeg {
	display: grid;
	grid-gap: 0.5rem;
}
.value {
	font-weight: bold;
	color: var(--bs-primary);
	word-break: break-all;
}
.value.missing {
	color: var(--bs-danger);
}
</style>
