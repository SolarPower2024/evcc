<template>
	<div class="group round-box p-4">
		<GeneralConfigEntry
			test-id="peakshaving-circuit-entry"
			:label="$t('config.peakshaving.circuitEntryLabel')"
			:text="circuitStatus"
			@edit="openModal('peakshavingcircuit')"
		/>

		<GeneralConfigEntry
			test-id="lmpriorities-entry"
			:label="$t('config.lmpriorities.entryLabel')"
			:text="prioritiesStatus"
			@edit="openModal('lmpriorities')"
		/>

		<GeneralConfigEntry
			test-id="lmshedguard-entry"
			:label="$t('config.lmshedguard.entryLabel')"
			:text="shedGuardStatus"
			@edit="openModal('lmshedguard')"
		/>

		<GeneralConfigEntry
			test-id="gridcharge-entry"
			:label="$t('config.gridcharge.entryLabel')"
			:text="gridChargeStatus"
			:text-class="gridChargeStatusClass"
			@edit="openModal('gridcharge')"
		/>

		<GeneralConfigEntry
			test-id="peakshaving-entry"
			:label="$t('config.peakshaving.entryLabel')"
			:text="peakShavingStatus"
			@edit="openModal('peakshaving')"
		/>

		<GeneralConfigEntry
			test-id="lmprofiles-entry"
			:label="$t('config.lmprofiles.entryLabel')"
			:text="profilesStatus"
			@edit="openModal('lmprofiles')"
		/>

		<GeneralConfigEntry
			test-id="lmadvanced-entry"
			:label="$t('config.lmadvanced.entryLabel')"
			:text="advancedStatus"
			@edit="openModal('lmadvanced')"
		/>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import store from "@/store";
import { openModal } from "@/configModal";
import GeneralConfigEntry from "./GeneralConfigEntry.vue";

// Entry rows of the load management details. The settings themselves are in
// PeakShavingCircuitModal, LmPrioritiesModal, LmShedGuardModal, GridChargeModal,
// PeakShavingModal, LmProfilesModal and LmAdvancedModal. The switches, the peak limit and the soc values live on the
// battery page.
export default defineComponent({
	name: "PeakShavingConfig",
	components: { GeneralConfigEntry },
	computed: {
		circuit(): string {
			return store.state.peakShavingCircuit ?? "";
		},
		circuitStatus(): string {
			if (!this.circuit) {
				return this.$t("config.peakshaving.circuitNotAssigned");
			}
			return store.state.circuits?.[this.circuit]?.title || this.circuit;
		},
		prioritiesStatus(): string {
			const count = store.state.lmPriorities?.length ?? 0;
			if (!count) {
				return this.$t("config.lmpriorities.none");
			}
			return this.$t("config.lmpriorities.loads", { count });
		},
		shedGuardStatus(): string {
			const minutes = store.state.lmShedGuard ?? 0;
			const count = store.state.lmShedProtected?.length ?? 0;
			if (!minutes || !count) {
				return this.$t("config.lmshedguard.off");
			}
			return this.$t("config.lmshedguard.status", { minutes, count });
		},
		profilesStatus(): string {
			const count = store.state.lmProfiles?.length ?? 0;
			if (!count) {
				return this.$t("config.lmprofiles.none");
			}
			return this.$t("config.lmprofiles.count", { count });
		},
		advancedStatus(): string {
			const adv = store.state.lmAdvanced;
			if (!adv) {
				return "";
			}
			return this.$t("config.lmadvanced.status", {
				hysteresis: adv.hysteresis,
				holdOff: adv.holdOff,
			});
		},
		dynamicCharge(): boolean {
			return !!store.state.peakShavingChargeEntity;
		},
		// the charge power only matters once something checks against it: the
		// circuit, or the dynamic setpoint it caps. Grid charging stays off without it.
		chargePowerMissing(): boolean {
			const needed = !!this.circuit || this.dynamicCharge;
			return needed && (store.state.peakShavingChargePowerEffective ?? 0) <= 0;
		},
		gridChargeStatus(): string {
			if (this.chargePowerMissing) {
				return this.$t("config.gridcharge.chargePowerMissing");
			}
			return this.$t(
				this.dynamicCharge
					? "config.gridcharge.modeDynamic"
					: "config.gridcharge.modeSwitched"
			);
		},
		gridChargeStatusClass(): string {
			return this.chargePowerMissing ? "text-danger" : "";
		},
		peakShavingStatus(): string {
			return this.$t(
				store.state.peakShavingEntity
					? "config.peakshaving.configured"
					: "config.peakshaving.notConfigured"
			);
		},
	},
	methods: { openModal },
});
</script>

<style scoped>
.group {
	display: grid;
	grid-template-columns: repeat(auto-fill, minmax(225px, 1fr));
	grid-gap: 2rem 5rem;
	margin-bottom: 5rem;
	align-items: start;
}
</style>
