<template>
	<div class="zones-field" data-testid="gridfee-zones">
		<div
			v-for="(zone, index) in modelValue"
			:key="index"
			data-testid="property-zone"
			class="mb-2"
		>
			<GridFeeZoneForm
				v-if="editIndex === index"
				:zone="zone"
				:currency="currency"
				:index="index"
				@save="saveEdit"
				@cancel="cancelEdit"
			/>
			<GridFeeZoneSummary
				v-else
				:zone="zone"
				:currency="currency"
				@edit="startEdit(index)"
				@remove="removeZone(index)"
			/>
		</div>

		<div class="d-flex align-items-center">
			<button
				type="button"
				class="d-flex btn btn-sm btn-outline-secondary border-0 align-items-center gap-2 evcc-gray"
				@click="addZone"
			>
				<shopicon-regular-plus size="s" class="flex-shrink-0"></shopicon-regular-plus>
				{{ $t("config.tariff.zones.add") }}
			</button>
		</div>
	</div>
</template>

<script lang="ts">
import { type PropType } from "vue";
import "@h2d2/shopicons/es/regular/plus";
import GridFeeZoneForm, { type GridFeeZone } from "./GridFeeZoneForm.vue";
import GridFeeZoneSummary from "./GridFeeZoneSummary.vue";
import { CURRENCY } from "@/types/evcc";

export default {
	name: "GridFeeZonesField",
	components: { GridFeeZoneForm, GridFeeZoneSummary },
	props: {
		modelValue: {
			type: Array as PropType<GridFeeZone[]>,
			default: () => [],
		},
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
	},
	emits: ["update:modelValue"],
	data() {
		return {
			editIndex: null as number | null,
		};
	},
	methods: {
		addZone() {
			const zone: GridFeeZone = { mode: "abs", value: null, hours: "", days: "", months: "" };
			const newZones = [...(this.modelValue || []), zone];
			this.$emit("update:modelValue", newZones);
			this.$nextTick(() => this.startEdit(newZones.length - 1));
		},
		removeZone(index: number) {
			this.$emit(
				"update:modelValue",
				this.modelValue.filter((_, i) => i !== index)
			);
		},
		startEdit(index: number) {
			this.editIndex = index;
		},
		saveEdit(zone: GridFeeZone) {
			const index = this.editIndex;
			if (index === null) return;
			const newZones = [...this.modelValue];
			newZones[index] = zone;
			this.$emit("update:modelValue", newZones);
			this.editIndex = null;
		},
		cancelEdit() {
			const index = this.editIndex;
			if (index !== null && this.modelValue[index]?.value === null) {
				this.$emit(
					"update:modelValue",
					this.modelValue.filter((_, i) => i !== index)
				);
			}
			this.editIndex = null;
		},
	},
};
</script>

<style scoped>
.zones-field {
	width: 100%;
}
</style>
