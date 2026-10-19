<template>
	<GenericModal
		id="batterySettingsModal"
		ref="modal"
		:title="$t('config.batterysettings.title')"
		data-testid="batterysettings-modal"
		config-modal-name="batterysettings"
		@open="open"
	>
		<!-- the settings of the home battery in closed groups, like the advanced settings -->
		<section v-for="(g, i) in groups" :key="g" :class="{ 'border-top': i > 0 }">
			<button
				type="button"
				class="btn btn-link group-toggle w-100 px-0 d-flex justify-content-between align-items-center text-reset text-decoration-none fw-bold"
				:aria-expanded="!!openGroups[g]"
				:data-testid="`batterysettings-group-${g}`"
				@click="openGroups[g] = !openGroups[g]"
			>
				{{ $t(`config.batterysettings.group.${g}`) }}
				<DropdownIcon class="icon" :class="{ iconUp: openGroups[g] }" />
			</button>
			<div v-show="openGroups[g]" class="pt-2">
				<BatteryCircuitForm v-if="g === 'circuit'" ref="circuit" />
				<BatteryGridChargeForm v-else-if="g === 'gridCharge'" ref="gridCharge" />
				<BatteryIdentForm v-else ref="ident" />
			</div>
		</section>
	</GenericModal>
</template>

<script>
import GenericModal from "../Helper/GenericModal.vue";
import DropdownIcon from "../MaterialIcon/Dropdown.vue";
import BatteryCircuitForm from "./BatteryCircuitForm.vue";
import BatteryGridChargeForm from "./BatteryGridChargeForm.vue";
import BatteryIdentForm from "./BatteryIdentForm.vue";

const GROUPS = ["circuit", "gridCharge", "ident"];

// Custom extension: the settings of the home battery in one dialog, a group each
// for the circuit (with the battery phases), grid charging (by soc, price or both)
// and the identification. Each group saves on its own, see core/lm/README.md.
export default {
	name: "BatteryConfigModal",
	components: {
		GenericModal,
		DropdownIcon,
		BatteryCircuitForm,
		BatteryGridChargeForm,
		BatteryIdentForm,
	},
	data() {
		return { openGroups: {} };
	},
	computed: {
		groups() {
			return GROUPS;
		},
	},
	methods: {
		open() {
			this.openGroups = {};
			// the forms read the current values themselves
			["circuit", "gridCharge", "ident"].forEach((name) => this.$refs[name]?.[0]?.reset());
		},
	},
};
</script>
<style scoped>
.group-toggle {
	min-height: 3rem;
}
.icon {
	transition: transform var(--evcc-transition-medium) ease;
}
.iconUp {
	transform: rotate(-180deg);
}
</style>
