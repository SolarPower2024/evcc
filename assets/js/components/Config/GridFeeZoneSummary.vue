<template>
	<div class="d-flex align-items-center justify-content-between py-2 ps-3 pe-2 border rounded">
		<div class="flex-grow-1">
			<span class="fw-semibold">{{ formattedValue }}</span>
			<small class="text-muted ms-2">{{ formattedConstraints }}</small>
		</div>
		<div class="d-flex">
			<button
				type="button"
				class="btn btn-sm btn-outline-secondary border-0"
				:aria-label="$t('config.tariff.zones.edit')"
				@click="$emit('edit')"
			>
				<shopicon-regular-edit size="s" class="flex-shrink-0"></shopicon-regular-edit>
			</button>
			<button
				type="button"
				class="btn btn-sm btn-outline-secondary border-0"
				:aria-label="$t('config.tariff.zones.remove')"
				@click="$emit('remove')"
			>
				<shopicon-regular-trash size="s" class="flex-shrink-0"></shopicon-regular-trash>
			</button>
		</div>
	</div>
</template>

<script lang="ts">
import { type PropType } from "vue";
import "@h2d2/shopicons/es/regular/trash";
import "@h2d2/shopicons/es/regular/edit";
import zoneUtils from "@/mixins/zoneUtils";
import { CURRENCY } from "@/types/evcc";
import type { GridFeeZone } from "./GridFeeZoneForm.vue";

export default {
	name: "GridFeeZoneSummary",
	mixins: [zoneUtils],
	props: {
		zone: { type: Object as PropType<GridFeeZone>, required: true },
		currency: { type: String as PropType<CURRENCY>, required: true },
	},
	emits: ["edit", "remove"],
	computed: {
		formattedValue() {
			if (this.zone.value == null) return "—";
			if (this.zone.mode === "pct") {
				return this.$t("config.lmgridfee.summaryPct", { value: this.zone.value });
			}
			return this.fmtPricePerKWh(this.zone.value, this.currency, true);
		},
		formattedConstraints() {
			const parts = [];
			if (this.zone.days) {
				const weekdays = this.parseWeekdaysString(this.zone.days);
				if (weekdays.length > 0 && weekdays.length < 7) {
					parts.push(this.weekdaysLabel(weekdays));
				}
			}
			if (this.zone.months) {
				const months = this.parseMonthsString(this.zone.months);
				if (months.length > 0 && months.length < 12) {
					parts.push(this.monthsLabel(months));
				}
			}
			if (this.zone.hours) {
				parts.push(this.fmtTimeRange(this.zone.hours));
			}
			return parts.length > 0 ? parts.join(", ") : this.$t("config.tariff.zones.allTimes");
		},
	},
};
</script>
