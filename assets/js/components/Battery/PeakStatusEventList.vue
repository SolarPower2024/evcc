<template>
	<section :data-testid="section">
		<h6 class="section-title">{{ title }}</h6>
		<p v-if="!events.length && empty" class="text-muted mb-0">{{ empty }}</p>
		<div v-for="(e, i) in shown" :key="i" class="event" :data-testid="row">
			<span class="evcc-gray text-nowrap">{{ eventTime(e) }}</span>
			<span class="event-text">{{ eventLine(e) }}</span>
		</div>
		<button
			v-if="events.length > FEW"
			type="button"
			class="btn btn-link btn-sm p-0 mt-1"
			:data-testid="more"
			@click="all = !all"
		>
			{{ $t(all ? "battery.peakStatus.less" : "battery.peakStatus.more") }}
		</button>
	</section>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import formatter from "@/mixins/formatter";
import type { LmEvent } from "@/types/evcc-lm";
import { eventText, makeFmt } from "./peakStatus";

const FEW = 5;

// Custom extension: a list of events of the peak shaving dialog, the newest
// five and "more" for the rest (PeakStatusModal.vue)
export default defineComponent({
	name: "PeakStatusEventList",
	mixins: [formatter],
	props: {
		events: { type: Array as PropType<LmEvent[]>, required: true },
		title: { type: String, required: true },
		// data-testid of the section, a row and the "more" button
		section: { type: String, required: true },
		row: { type: String, required: true },
		more: { type: String, required: true },
		// shown while the list is empty; without it the section is only the title
		empty: { type: String, default: "" },
		now: { type: Number, default: () => Date.now() },
	},
	data() {
		return { all: false, FEW };
	},
	computed: {
		shown(): LmEvent[] {
			return this.all ? this.events : this.events.slice(0, FEW);
		},
		fmt() {
			return makeFmt(
				(watt: number) => this.fmtW(watt),
				(percent: number) => this.fmtPercentage(percent),
				(watt: number) => this.fmtW(watt, this.POWER_UNIT.KW, false)
			);
		},
	},
	methods: {
		tr(key: string, params?: Record<string, unknown>): string {
			return this.$t(key, params as Record<string, string>);
		},
		eventTime(e: LmEvent): string {
			const at = new Date(e.at);
			const today = new Date(this.now).toDateString() === at.toDateString();
			return today ? this.fmtHourMinute(at) : this.fmtWeekdayDayTime(at);
		},
		eventLine(e: LmEvent): string {
			return eventText(e, this.tr, this.fmt);
		},
	},
});
</script>

<style scoped>
.section-title {
	font-size: 0.75rem;
	font-weight: normal;
	text-transform: uppercase;
	color: var(--evcc-gray);
	margin-bottom: 0.35rem;
}
.event {
	display: grid;
	grid-template-columns: 5rem minmax(0, 1fr);
	column-gap: 0.75rem;
	font-size: 0.9rem;
	line-height: 1.6;
	padding: 0.1rem 0;
}
.event-text {
	min-width: 0;
	overflow-wrap: anywhere;
}
</style>
