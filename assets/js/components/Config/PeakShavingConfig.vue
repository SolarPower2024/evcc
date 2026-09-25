<template>
	<div class="p-0 config-list">
		<DeviceCard
			:title="$t('config.peakshaving.circuitEntryLabel')"
			editable
			data-testid="peakshaving-circuit-entry"
			@edit="openModal('peakshavingcircuit')"
		>
			<template #icon><CircuitsIcon /></template>
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
			:title="$t('config.gridcharge.entryLabel')"
			editable
			:error="chargePowerMissing"
			data-testid="gridcharge-entry"
			@edit="openModal('gridcharge')"
		>
			<template #icon
				><shopicon-regular-batterycharge></shopicon-regular-batterycharge
			></template>
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
import { defineComponent } from "vue";
import store from "@/store";
import { openModal } from "@/configModal";
import DeviceCard from "./DeviceCard.vue";
import CircuitsIcon from "../MaterialIcon/Circuits.vue";

// Tiles of the load management details, laid out like the services. The settings
// themselves are in PeakShavingCircuitModal, LmPrioritiesModal, LmShedGuardModal,
// GridChargeModal, PeakShavingModal, LmProfilesModal and LmAdvancedModal. The
// switches, the peak limit and the soc values live on the battery page.
export default defineComponent({
	name: "PeakShavingConfig",
	components: { DeviceCard, CircuitsIcon },
	computed: {
		// the charge power only matters once something checks against it: the
		// circuit, or the dynamic setpoint it caps. Grid charging stays off without it.
		chargePowerMissing(): boolean {
			const needed =
				!!store.state.peakShavingCircuit || !!store.state.peakShavingChargeEntity;
			return needed && (store.state.peakShavingChargePowerEffective ?? 0) <= 0;
		},
	},
	methods: { openModal },
});
</script>
