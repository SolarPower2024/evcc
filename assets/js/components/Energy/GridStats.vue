<template>
	<StatCards :stats="stats" />
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import StatCards from "./StatCards.vue";
import formatter from "@/mixins/formatter";
import { CURRENCY } from "@/types/evcc";
import type { FlowCost, StatItem } from "./types";
import type { FeedInSplitTotals } from "./feedInEeg";

// what import cost and export earned, pointing at the tariff settings without one
export default defineComponent({
	name: "GridStats",
	components: { StatCards },
	mixins: [formatter],
	props: {
		cost: { type: Object as PropType<FlowCost> },
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
		// custom: export split by feed-in tariff, see feedInEeg.ts
		feedInEeg: { type: Object as PropType<FeedInSplitTotals | null>, default: null },
	},
	computed: {
		stats(): StatItem[] {
			if (this.feedInEeg) return this.feedInEegStats(this.feedInEeg); // custom
			const cost = this.cost;
			if (!cost) {
				const empty = this.$t("energy.stat.noPrice");
				return [
					{ key: "gridImport", label: this.$t("energy.grid.cost"), empty },
					{ key: "gridExport", label: this.$t("energy.grid.revenue"), empty },
				];
			}
			const tile = (key: string, label: string, money: number, energy: number): StatItem => ({
				key,
				label,
				number: money,
				format: (v: number) => this.fmtMoneyWithSymbol(v, this.currency),
				sub: energy ? `ø ${this.fmtPricePerKWh(money / energy, this.currency)}` : "",
			});
			return [
				tile("gridImport", this.$t("energy.grid.cost"), cost.import, cost.importEnergy),
				{
					...tile(
						"gridExport",
						this.$t("energy.grid.revenue"),
						cost.export,
						cost.exportEnergy
					),
					accent: "text-accent1",
				},
			];
		},
	},
	methods: {
		// custom: revenue per feed-in tariff, priced slot by slot on its own and so
		// also shown without a grid price, see feedInEeg.ts
		feedInEegStats(e: FeedInSplitTotals): StatItem[] {
			const empty = this.$t("energy.stat.noPrice");
			const money = (
				key: string,
				label: string,
				amount: number,
				energy: number
			): StatItem => ({
				key,
				label,
				number: amount,
				format: (v: number) => this.fmtMoneyWithSymbol(v, this.currency),
				sub: energy ? `ø ${this.fmtPricePerKWh(amount / energy, this.currency)}` : "",
			});
			const revenue = (
				key: string,
				label: string,
				amount: number,
				energy: number,
				priced: number
			): StatItem =>
				energy > 0 && !priced
					? { key, label, empty }
					: { ...money(key, label, amount, priced), accent: "text-accent1" };
			const cost = this.cost;
			return [
				cost
					? money(
							"gridImport",
							this.$t("energy.grid.cost"),
							cost.import,
							cost.importEnergy
						)
					: { key: "gridImport", label: this.$t("energy.grid.cost"), empty },
				revenue(
					"gridExport",
					this.$t("energy.grid.revenue"),
					e.standardRevenue,
					e.standard,
					e.standardPriced
				),
				revenue(
					"gridExportEeg",
					this.$t("energy.feedInEeg.revenue"),
					e.eegRevenue,
					e.eeg,
					e.eegPriced
				),
			];
		},
	},
});
</script>
