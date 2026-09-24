<template>
	<div v-if="state" class="feedin-final mt-2" data-testid="feedin-final-summary">
		<div class="d-flex gap-2">
			<div class="label flex-grow-1">{{ $t("config.feedinfinal.lastLabel") }}</div>
			<div class="value text-end">{{ lastText }}</div>
		</div>
		<div class="d-flex gap-2">
			<div class="label flex-grow-1">{{ $t("config.feedinfinal.nextLabel") }}</div>
			<div class="value text-end">{{ nextText }}</div>
		</div>
		<button
			type="button"
			class="btn btn-link btn-sm p-0 mt-1"
			data-testid="feedin-final-open"
			@click.stop="openModal('feedinfinal')"
		>
			{{ $t("config.feedinfinal.show") }}
		</button>
	</div>
</template>

<script>
import store from "@/store";
import { openModal } from "@/configModal";
import formatter from "@/mixins/formatter";
import { feedInNextFinalization, fmtMarketPrice, monthDate } from "./feedInFinal";

// Custom extension: the OeMAG finalization status inside the feed-in tariff's
// card, see core/site_feedin.go. Shown only for a tariff with a final price.
export default {
	name: "FeedInFinalSummary",
	mixins: [formatter],
	computed: {
		state() {
			return store.state?.feedInFinal;
		},
		last() {
			return this.state?.months?.[0];
		},
		lastText() {
			if (!this.last) {
				return this.$t("config.feedinfinal.none");
			}
			const month = this.fmtMonthYear(monthDate(this.last.month));
			if (!this.last.market) {
				return month;
			}
			return `${month} · ${fmtMarketPrice(this.last.market, this.$i18n?.locale)}`;
		},
		nextText() {
			const next = feedInNextFinalization(
				new Date(),
				this.state.finalizeDay,
				this.last?.month
			);
			return this.fmtDayMonth(next);
		},
	},
	methods: { openModal },
};
</script>

<style scoped>
.feedin-final {
	display: grid;
	grid-gap: 0.5rem;
}
.value {
	font-weight: bold;
	color: var(--bs-primary);
}
</style>
