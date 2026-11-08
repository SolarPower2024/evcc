<template>
	<div class="border rounded p-3">
		<!-- Mode -->
		<div class="mb-3">
			<SelectGroup
				:id="formId('mode')"
				v-model="uiZone.mode"
				class="w-100"
				equal-width
				transparent
				:aria-label="$t('config.lmgridfee.mode')"
				:options="[
					{ value: 'abs', name: $t('config.lmgridfee.modeAbs') },
					{ value: 'pct', name: $t('config.lmgridfee.modePct') },
				]"
			/>
		</div>

		<!-- Value input -->
		<div class="mb-3">
			<label :for="formId('value')" class="form-label">{{ valueLabel }}</label>
			<div class="d-flex w-50 w-min-200">
				<input
					:id="formId('value')"
					v-model.number.lazy="uiZone.value"
					type="number"
					step="any"
					class="form-control text-end"
					:class="{ 'is-invalid': isValueInvalid }"
					style="border-top-right-radius: 0; border-bottom-right-radius: 0"
				/>
				<span
					class="input-group-text"
					:class="{ 'border-danger': isValueInvalid }"
					style="border-top-left-radius: 0; border-bottom-left-radius: 0"
					>{{ valueUnit }}</span
				>
			</div>
			<div v-if="pctResult" class="form-text evcc-gray" data-testid="gridfee-pct-result">
				{{ pctResult }}
			</div>
			<div v-if="isValueInvalid" class="invalid-feedback d-block">
				{{ isPct ? $t("config.lmgridfee.pctRange") : $t("config.tariff.zones.priceRequired") }}
			</div>
		</div>

		<!-- Month selector -->
		<div class="mb-3">
			<label :for="formId('months')" class="form-label">
				{{ $t("config.tariff.zones.months") }}
				<small class="evcc-gray">{{ $t("config.form.optional") }}</small>
			</label>
			<MultiSelect
				:id="formId('months')"
				v-model="uiZone.months"
				:options="monthOptions"
				:selectAllLabel="$t('main.chargingPlan.selectAll')"
			>
				{{ monthsLabel(uiZone.months) }}
			</MultiSelect>
		</div>

		<!-- Weekday selector -->
		<div class="mb-3">
			<label :for="formId('weekdays')" class="form-label">
				{{ $t("config.tariff.zones.weekdays") }}
				<small class="evcc-gray">{{ $t("config.form.optional") }}</small>
			</label>
			<MultiSelect
				:id="formId('weekdays')"
				v-model="uiZone.weekdays"
				:options="dayOptions"
				:selectAllLabel="$t('main.chargingPlan.selectAll')"
			>
				{{ weekdaysLabel(uiZone.weekdays) }}
			</MultiSelect>
		</div>

		<!-- Time range -->
		<div class="mb-3">
			<label class="form-label">
				{{ $t("config.tariff.zones.hours") }}
				<small class="evcc-gray">{{ $t("config.form.optional") }}</small>
			</label>
			<div class="d-flex gap-2 align-items-center">
				<input
					:id="formId('time-from')"
					v-model="uiZone.timeFrom"
					type="time"
					class="form-control"
					:class="{ 'is-invalid': isTimeRangeInvalid }"
					:aria-label="$t('config.tariff.zones.timeFrom')"
				/>
				<span>–</span>
				<input
					:id="formId('time-to')"
					v-model="uiZone.timeTo"
					type="time"
					class="form-control"
					:class="{ 'is-invalid': isTimeRangeInvalid }"
					:aria-label="$t('config.tariff.zones.timeTo')"
				/>
			</div>
			<div v-if="isTimeRangeInvalid" class="invalid-feedback d-block">
				{{ $t("config.tariff.zones.timeRangeError") }}
			</div>
		</div>

		<!-- Actions -->
		<div class="d-flex justify-content-between align-items-center">
			<button type="button" class="btn btn-link text-muted" @click="$emit('cancel')">
				{{ $t("config.tariff.zones.cancel") }}
			</button>
			<button type="button" class="btn btn-primary" @click="handleSave">
				{{ $t("config.tariff.zones.save") }}
			</button>
		</div>
	</div>
</template>

<script lang="ts">
import { type PropType } from "vue";
import zoneUtils from "@/mixins/zoneUtils";
import MultiSelect from "../Helper/MultiSelect.vue";
import SelectGroup from "../Helper/SelectGroup.vue";
import { CURRENCY } from "@/types/evcc";
import { gridFeeAfterPct } from "@/utils/lmGridTariff";

// abs: grid fee in currency per kWh, pct: percent deducted from the grid fee
export type GridFeeZone = {
	mode: "abs" | "pct";
	value: number | null;
	days: string;
	months: string;
	hours: string;
};

type UiZone = {
	mode: "abs" | "pct";
	value: number | null;
	weekdays: number[];
	months: number[];
	timeFrom: string;
	timeTo: string;
};

export default {
	name: "GridFeeZoneForm",
	components: { MultiSelect, SelectGroup },
	mixins: [zoneUtils],
	props: {
		zone: { type: Object as PropType<GridFeeZone>, required: true },
		currency: { type: String as PropType<CURRENCY>, required: true },
		index: { type: Number, required: true },
	},
	inject: { lmGridFee: { default: undefined } },
	emits: ["save", "cancel"],
	data() {
		return {
			uiZone: {} as UiZone,
			saveAttempted: false,
		};
	},
	computed: {
		isPct() {
			return this.uiZone.mode === "pct";
		},
		displayFactor() {
			return this.pricePerKWhDisplayFactor(this.currency);
		},
		valueUnit() {
			return this.isPct ? "%" : this.pricePerKWhUnit(this.currency);
		},
		valueLabel() {
			return this.$t(this.isPct ? "config.lmgridfee.valuePct" : "config.lmgridfee.valueAbs");
		},
		pctResult(): string {
			const fee = Number(this.lmGridFee);
			if (!this.isPct || this.lmGridFee == null || isNaN(fee) || !this.isValidValue)
				return "";
			return this.$t("config.lmgridfee.pctResult", {
				fee: this.fmtPricePerKWh(fee, this.currency, true),
				value: this.uiZone.value,
				result: this.fmtPricePerKWh(
					gridFeeAfterPct(fee, this.uiZone.value as number),
					this.currency,
					true
				),
			});
		},
		dayOptions() {
			return this.getWeekdaysList("long");
		},
		monthOptions() {
			return this.getMonthsList("long");
		},
		isValidValue() {
			const value = this.uiZone.value;
			if (value === null || (value as unknown) === "" || isNaN(value)) return false;
			return !this.isPct || (value >= 0 && value <= 100);
		},
		isValidTimeRange() {
			const { timeFrom, timeTo } = this.uiZone;
			if (!timeFrom || !timeTo) return false;
			if (timeTo === "00:00") return true;
			return timeFrom < timeTo;
		},
		isTimeRangeInvalid() {
			return this.saveAttempted && !this.isValidTimeRange;
		},
		isValueInvalid() {
			return this.saveAttempted && !this.isValidValue;
		},
	},
	watch: {
		zone: {
			handler(newZone: GridFeeZone) {
				this.uiZone = this.convertToUiZone(newZone);
			},
			immediate: true,
		},
	},
	methods: {
		formId(field: string) {
			return `gridfee-zone-${field}-${this.index}`;
		},
		convertToUiZone(zone: GridFeeZone): UiZone {
			const [timeFrom, timeTo] = (zone.hours || "").split("-");
			const mode = zone.mode === "pct" ? "pct" : "abs";
			let value = zone.value;
			if (value != null && mode === "abs") value = value * this.displayFactor;
			return {
				mode,
				value,
				weekdays: this.parseWeekdaysString(zone.days || ""),
				months: this.parseMonthsString(zone.months || ""),
				timeFrom: timeFrom || "00:00",
				timeTo: timeTo || "00:00",
			};
		},
		convertFromUiZone(uiZone: UiZone): GridFeeZone {
			const { timeFrom, timeTo } = uiZone;
			const isAllDay = (!timeFrom && !timeTo) || (timeFrom === "00:00" && timeTo === "00:00");
			let value = uiZone.value;
			if (value != null && uiZone.mode === "abs") value = value / this.displayFactor;
			return {
				mode: uiZone.mode,
				value,
				days: this.formatWeekdaysToString(uiZone.weekdays),
				months: this.formatMonthsToString(uiZone.months),
				hours: isAllDay ? "" : `${timeFrom}-${timeTo}`,
			};
		},
		handleSave() {
			this.saveAttempted = true;
			if (this.isValidValue && this.isValidTimeRange) {
				this.$emit("save", this.convertFromUiZone(this.uiZone));
			}
		},
	},
};
</script>

<style scoped>
.w-min-200 {
	min-width: min(200px, 100%);
}
</style>
