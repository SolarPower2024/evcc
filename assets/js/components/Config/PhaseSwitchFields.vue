<template>
	<div class="row mb-4" data-testid="loadpoint-currents-1p">
		<FormRow
			id="loadpointMinCurrent1p"
			:label="$t('config.loadpoint.minCurrent1pLabel')"
			class="col-sm-6 mb-sm-0"
			optional
		>
			<div class="d-flex align-items-center gap-2">
				<PropertyField
					id="loadpointMinCurrent1p"
					v-model="values.minCurrent1p"
					type="Float"
					unit="A"
					size="w-50 w-sm-100"
				/>
				<span v-if="values.minCurrent1p" class="evcc-gray text-nowrap power-hint">
					≈ {{ fmtPhasePower(values.minCurrent1p, 1) }}
				</span>
			</div>
		</FormRow>

		<FormRow
			id="loadpointMaxCurrent1p"
			:label="$t('config.loadpoint.maxCurrent1pLabel')"
			class="col-sm-6 mb-0"
			:warning="maxCurrent1pWarning"
			optional
		>
			<div class="d-flex align-items-center gap-2">
				<PropertyField
					id="loadpointMaxCurrent1p"
					v-model="values.maxCurrent1p"
					type="Float"
					unit="A"
					size="w-50 w-sm-100"
				/>
				<span v-if="values.maxCurrent1p" class="evcc-gray text-nowrap power-hint">
					≈ {{ fmtPhasePower(values.maxCurrent1p, 1) }}
				</span>
			</div>
		</FormRow>
		<div class="col-12 form-text evcc-gray hyphenate">
			{{ $t("config.loadpoint.current1pHelp") }}
		</div>
	</div>

	<div class="row mb-4" data-testid="loadpoint-phase-delays">
		<FormRow
			id="loadpointPhaseScale3pDelay"
			:label="$t('config.loadpoint.phaseScale3pDelayLabel')"
			class="col-sm-6 mb-sm-0"
			optional
		>
			<PropertyField
				id="loadpointPhaseScale3pDelay"
				v-model="values.phaseScale3pDelay"
				type="Duration"
				legacy-duration
				unit="minute"
				size="w-50 w-sm-100"
			/>
		</FormRow>

		<FormRow
			id="loadpointPhaseScale1pDelay"
			:label="$t('config.loadpoint.phaseScale1pDelayLabel')"
			class="col-sm-6 mb-0"
			optional
		>
			<PropertyField
				id="loadpointPhaseScale1pDelay"
				v-model="values.phaseScale1pDelay"
				type="Duration"
				legacy-duration
				unit="minute"
				size="w-50 w-sm-100"
			/>
		</FormRow>
		<div class="col-12 form-text evcc-gray hyphenate">
			{{
				$t("config.loadpoint.phaseDelayHelp", {
					enableDelay: fmtDurationNs(values.thresholds.enable.delay, true, "m"),
					disableDelay: fmtDurationNs(values.thresholds.disable.delay, true, "m"),
				})
			}}
		</div>
	</div>
</template>

<script lang="ts">
import type { PropType } from "vue";
import FormRow from "./FormRow.vue";
import PropertyField from "./PropertyField.vue";
import formatter from "@/mixins/formatter";
import type { ConfigLoadpoint } from "@/types/evcc";

// Custom extension: 1p current limits and phase switching delays in the
// loadpoint settings, see core/loadpoint_phasecurrents.go. Edits the fields of
// the loadpoint modal's values in place, like its own fields do.
export default {
	name: "PhaseSwitchFields",
	components: { FormRow, PropertyField },
	mixins: [formatter],
	props: {
		values: { type: Object as PropType<ConfigLoadpoint>, required: true },
	},
	computed: {
		maxCurrent1pWarning() {
			const { minCurrent, minCurrent1p, maxCurrent1p } = this.values;
			return maxCurrent1p && maxCurrent1p < (minCurrent1p || minCurrent)
				? this.$t("config.loadpoint.maxCurrent1pHelp")
				: undefined;
		},
	},
};

// emptyUnsetPhaseSwitch shows unset values (0) as empty fields
export function emptyUnsetPhaseSwitch(values: ConfigLoadpoint) {
	const keys = [
		"minCurrent1p",
		"maxCurrent1p",
		"phaseScale3pDelay",
		"phaseScale1pDelay",
	] as const;
	for (const key of keys) {
		if (!values[key]) {
			values[key] = undefined;
		}
	}
}
</script>

<style scoped>
.power-hint {
	/* fits "≈ 11.1 kW", keeps input width stable */
	min-width: 9ch;
}
</style>
