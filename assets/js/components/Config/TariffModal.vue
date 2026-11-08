<template>
	<DeviceModalBase
		ref="lmBase"
		:id="id"
		:name="name"
		device-type="tariff"
		:hide-disable="hideDisable"
		:has-device-error="hasDeviceError"
		:modal-title="modalTitle"
		:provide-template-options="provideTemplateOptions"
		:initial-values="initialValues"
		:show-main-content="!!tariffType"
		:on-template-change="handleTemplateChange"
		:currency="currency"
		:preserve-on-template-change="preserveFields"
		:default-template="defaultTemplate"
		@added="handleAdded"
		@updated="handleUpdated"
		@removed="handleRemoved"
		@disable="$emit('disable', $event)"
		@close="handleClose"
	>
		<template #pre-content>
			<div v-if="!tariffType" class="d-flex flex-column gap-4">
				<NewDeviceButton
					v-for="t in typeChoices"
					:key="t"
					:title="$t(`config.tariff.option.${t}`)"
					class="addButton"
					@click="selectType(t)"
				/>
			</div>
		</template>

		<template #description>
			<p v-if="hasDescription" class="mt-0 mb-4">
				{{ $t(`config.tariff.${tariffType}.description`) }}
			</p>
			<!-- custom: the optimizer plans with the planner price, see core/site_optimizer_lm.go -->
			<p
				v-if="tariffType === 'planner'"
				class="mt-0 mb-4"
				data-testid="tariff-planner-optimizer"
			>
				{{ $t("config.tariff.planner.optimizer") }}
			</p>
		</template>

		<template #before-template="{ values }">
			<FormRow
				v-if="hasDeviceTitle"
				id="tariffParamDeviceTitle"
				:label="$t('config.general.title')"
				:example="$t('config.tariff.solar.titleExample')"
			>
				<PropertyField
					id="tariffParamDeviceTitle"
					v-model.trim="values.deviceTitle"
					type="String"
					size="w-100"
					class="me-2"
					required
				/>
			</FormRow>
		</template>
	</DeviceModalBase>
</template>

<script lang="ts">
import { computed, defineComponent, markRaw, type PropType } from "vue";
import DeviceModalBase from "./DeviceModal/DeviceModalBase.vue";
import NewDeviceButton from "./NewDeviceButton.vue";
import FormRow from "./FormRow.vue";
import PropertyField from "./PropertyField.vue";
import { ConfigType, CURRENCY, type DeviceType, type TariffType } from "@/types/evcc";
import { customTemplateOption, type TemplateGroup } from "./DeviceModal/TemplateSelector.vue";
import type { Product, DeviceValues } from "./DeviceModal";
import { getModal, replaceModal } from "@/configModal";
import tariffPriceYaml from "./defaultYaml/tariffPrice.yaml?raw";
import tariffCo2Yaml from "./defaultYaml/tariffCo2.yaml?raw";
import tariffSolarYaml from "./defaultYaml/tariffSolar.yaml?raw";
import tariffTemperatureYaml from "./defaultYaml/tariffTemperature.yaml?raw";
import { defaultPriceTemplate, priceTemplatesFor } from "@/utils/lmGridTariff"; // custom

const initialValues = {
	type: ConfigType.Template,
	deviceTitle: "",
	deviceProduct: undefined,
	yaml: undefined,
	template: null,
};

export default defineComponent({
	name: "TariffModal",
	components: {
		DeviceModalBase,
		NewDeviceButton,
		FormRow,
		PropertyField,
	},
	props: {
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
		name: { type: String, default: "tariff" },
		hideDisable: Boolean,
		hasDeviceError: Function as PropType<(type: DeviceType, name: string) => boolean>,
	},
	emits: ["changed", "disable", "close"],
	// custom: grid fee for the split tariff zones (GridFeeZoneForm/Summary)
	provide() {
		return { lmGridFee: computed(() => (this.lmBaseRef as any)?.values?.gridfee) };
	},
	data() {
		return {
			lmBaseRef: null as object | null, // custom
			initialValues,
			preserveFields: ["deviceTitle"],
		};
	},
	mounted() {
		this.lmBaseRef = markRaw(this.$refs["lmBase"] as object); // custom
	},
	computed: {
		id(): number | undefined {
			return getModal(this.name)?.id;
		},
		type(): TariffType | undefined {
			return getModal(this.name)?.type as TariffType | undefined;
		},
		typeChoices(): TariffType[] {
			return (getModal(this.name)?.choices as TariffType[]) || [];
		},
		isNew(): boolean {
			return this.id === undefined;
		},
		tariffType(): TariffType | null {
			return this.type || null;
		},
		// a fixed price is the common case for grid import and export
		defaultTemplate(): string | undefined {
			return defaultPriceTemplate(this.tariffType); // custom: split grid tariff
		},
		modalTitle(): string {
			if (this.name !== "tariff") {
				return this.$t("config.tariff.titleSub");
			}
			if (this.isNew) {
				if (this.tariffType) {
					return this.$t(`config.tariff.${this.tariffType}.titleAdd`);
				} else {
					return this.$t("config.tariff.titleChoice");
				}
			}
			return this.$t(`config.tariff.${this.tariffType}.titleEdit`);
		},
		hasDeviceTitle(): boolean {
			return this.tariffType === "solar";
		},
		hasDescription(): boolean {
			return !!this.tariffType;
		},
	},
	methods: {
		provideTemplateOptions(products: Product[]): TemplateGroup[] {
			// Use different custom option text for tariffs vs forecasts
			const isForecast = ["co2", "planner", "solar", "temperature"].includes(
				this.tariffType || ""
			);
			const customLabel = isForecast
				? this.$t("config.tariff.customForecast")
				: this.$t("config.tariff.customTariff");

			// Separate demo/generic templates from real services
			const genericTemplates = [
				"fixed",
				"fixed-netz", // custom: split grid tariff
				"fixed-zones",
				"merged",
				"demo-co2-forecast",
				"demo-dynamic-grid",
				"demo-solar-forecast",
				"demo-temperature-forecast",
				"energy-charts-api",
			];

			// Filter products by group
			const filterByGroup = (group: string) =>
				products.filter((p: Product) => {
					const isGeneric = genericTemplates.includes(p.template);
					return p.group === group && !isGeneric;
				});

			// Extract generic templates in order from genericTemplates array
			const extractGeneric = (group: string) =>
				genericTemplates
					.map((template) =>
						products.find((p: Product) => p.template === template && p.group === group)
					)
					.filter((p): p is Product => p !== undefined);

			const priceProducts = filterByGroup("price");
			const co2Products = filterByGroup("co2");
			const solarProducts = filterByGroup("solar");
			const temperatureProducts = filterByGroup("temperature");
			const priceGeneric = priceTemplatesFor(
				extractGeneric("price"),
				this.tariffType,
				this.isNew
			); // custom: split grid tariff
			const co2Generic = extractGeneric("co2");
			const solarGeneric = extractGeneric("solar");
			const temperatureGeneric = extractGeneric("temperature");

			// Special handling for planner: show price + co2 services
			if (this.tariffType === "planner") {
				return [
					{
						label: "generic",
						options: [
							...priceGeneric,
							...co2Generic,
							customTemplateOption(customLabel),
						],
					},
					{
						label: "services",
						options: priceProducts,
					},
					{
						label: "co2Services",
						options: co2Products,
					},
				];
			}

			// Map tariff types to product groups
			const groupMap: Record<string, { service: Product[]; generic: Product[] }> = {
				grid: { service: priceProducts, generic: priceGeneric },
				feedIn: { service: priceProducts, generic: priceGeneric },
				feedInEeg: { service: [], generic: priceGeneric }, // custom: fixed price
				co2: { service: co2Products, generic: co2Generic },
				solar: { service: solarProducts, generic: solarGeneric },
				temperature: { service: temperatureProducts, generic: temperatureGeneric },
			};
			const mapped = (this.tariffType && groupMap[this.tariffType]) || {
				service: [],
				generic: [],
			};

			return [
				{
					label: "generic",
					options: [...mapped.generic, customTemplateOption(customLabel)],
				},
				{
					label: "services",
					options: mapped.service,
				},
			];
		},
		handleTemplateChange(value: string, values: DeviceValues) {
			if (value === ConfigType.Custom) {
				values.type = ConfigType.Custom;
				// Select appropriate YAML template based on tariff type
				if (
					this.tariffType === "grid" ||
					this.tariffType === "feedIn" ||
					this.tariffType === "feedInEeg" || // custom
					this.tariffType === "planner"
				) {
					values.yaml = tariffPriceYaml;
				} else if (this.tariffType === "co2") {
					values.yaml = tariffCo2Yaml;
				} else if (this.tariffType === "solar") {
					values.yaml = tariffSolarYaml;
				} else if (this.tariffType === "temperature") {
					values.yaml = tariffTemperatureYaml;
				}
			}
		},
		selectType(type: TariffType) {
			// the stack entry is the source of truth, nested forms read the type from it
			replaceModal(this.name, { type });
		},
		handleAdded(name: string) {
			this.$emit("changed", { action: "added", type: this.tariffType, name });
		},
		handleUpdated() {
			this.$emit("changed", { action: "updated", type: this.tariffType });
		},
		handleRemoved() {
			this.$emit("changed", { action: "removed", type: this.tariffType });
		},
		handleClose() {
			this.$emit("close");
		},
	},
});
</script>
<style scoped>
.addButton {
	min-height: 6rem;
}
</style>
