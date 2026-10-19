<template>
	<div class="p-0 config-list">
		<DeviceCard
			:title="$t('config.batterysettings.entryLabel')"
			editable
			:error="chargePowerMissing || phasesMissing"
			data-testid="batterysettings-entry"
			@edit="openModal('batterysettings')"
		>
			<template #icon
				><shopicon-regular-batterycharge></shopicon-regular-batterycharge
			></template>
			<template #tags>
				<div class="tags" data-testid="batterysettings-summary">
					<div v-for="row in summary" :key="row.key" class="d-flex gap-2 overflow-hidden">
						<div class="label flex-grow-1 text-truncate">{{ row.label }}</div>
						<div
							class="value text-truncate"
							:data-testid="`batterysettings-${row.key}`"
						>
							{{ row.value }}
						</div>
					</div>
				</div>
			</template>
		</DeviceCard>

		<DeviceCard
			:title="$t('config.lmpriorities.entryLabel')"
			editable
			data-testid="lmpriorities-entry"
			@edit="openModal('lmpriorities')"
		>
			<template #icon><shopicon-regular-checklist></shopicon-regular-checklist></template>
		</DeviceCard>

		<DeviceCard
			:title="$t('config.lmshedguard.entryLabel')"
			editable
			data-testid="lmshedguard-entry"
			@edit="openModal('lmshedguard')"
		>
			<template #icon><shopicon-regular-shield></shopicon-regular-shield></template>
		</DeviceCard>

		<DeviceCard
			:title="$t('config.peakshaving.entryLabel')"
			editable
			data-testid="peakshaving-entry"
			@edit="openModal('peakshaving')"
		>
			<template #icon><shopicon-regular-lightning></shopicon-regular-lightning></template>
		</DeviceCard>

		<DeviceCard
			:title="$t('config.peaktariff.entryLabel')"
			editable
			data-testid="peaktariff-entry"
			@edit="openModal('peaktariff')"
		>
			<template #icon
				><shopicon-regular-receivepayment></shopicon-regular-receivepayment
			></template>
		</DeviceCard>

		<DeviceCard
			:title="$t('config.lmprofiles.entryLabel')"
			editable
			data-testid="lmprofiles-entry"
			@edit="openModal('lmprofiles')"
		>
			<template #icon><shopicon-regular-cloudsun></shopicon-regular-cloudsun></template>
		</DeviceCard>

		<DeviceCard
			:title="$t('config.lmadvanced.entryLabel')"
			editable
			data-testid="lmadvanced-entry"
			@edit="openModal('lmadvanced')"
		>
			<template #icon><shopicon-regular-settings></shopicon-regular-settings></template>
		</DeviceCard>
	</div>
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/checklist";
import "@h2d2/shopicons/es/regular/shield";
import "@h2d2/shopicons/es/regular/batterycharge";
import "@h2d2/shopicons/es/regular/lightning";
import "@h2d2/shopicons/es/regular/cloudsun";
import "@h2d2/shopicons/es/regular/settings";
import "@h2d2/shopicons/es/regular/receivepayment";
import { defineComponent } from "vue";
import store from "@/store";
import { openModal } from "@/configModal";
import DeviceCard from "./DeviceCard.vue";
import { gridChargeMode } from "@/utils/gridChargeMode";

// Tiles of the load management details, laid out like the services. The settings
// themselves are in BatteryConfigModal (circuit, grid charging, identification),
// LmPrioritiesModal, LmShedGuardModal, PeakShavingModal, PeakTariffModal, LmProfilesModal and
// LmAdvancedModal. The
// switches, the peak limit and the soc values live on the battery page.
export default defineComponent({
	name: "PeakShavingConfig",
	components: { DeviceCard },
	computed: {
		// the battery card shows its circuit and what grid charging goes by
		summary(): { key: string; label: string; value: string }[] {
			const name = store.state.peakShavingCircuit || "";
			const circuit = store.state.circuits?.[name];
			const mode = gridChargeMode(store.state.batteryGridChargeMode);
			return [
				{
					key: "circuit",
					label: this.$t("config.batterysettings.group.circuit"),
					value: circuit ? circuit.title || name : "–",
				},
				{
					key: "mode",
					label: this.$t("config.batterysettings.gridChargeMode"),
					value: this.$t(`config.batterysettings.mode.${mode}`),
				},
			];
		},
		// the charge power only matters once something checks against it: the
		// circuit, or the dynamic setpoint it caps. Grid charging stays off without it.
		// the battery phases are needed for a circuit with a current limit, see core/site_lm_phases.go
		phasesMissing(): boolean {
			return !!store.state.lmAdvanced?.phasesMissing;
		},
		chargePowerMissing(): boolean {
			const needed =
				!!store.state.peakShavingCircuit || !!store.state.peakShavingChargeEntity;
			return needed && (store.state.peakShavingChargePowerEffective ?? 0) <= 0;
		},
	},
	methods: { openModal },
});
</script>
<style scoped>
.tags {
	display: grid;
	grid-template-columns: 1fr;
	grid-gap: 0.5rem;
}
.value {
	font-weight: bold;
	color: var(--bs-primary);
}
</style>
