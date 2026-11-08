<template>
	<GenericModal
		id="peakStatusModal"
		:title="$t(titleKey)"
		size="md"
		data-testid="peak-status-modal"
		@open="onOpen"
		@closed="onClosed"
	>
		<div v-if="visible" class="d-flex flex-column gap-3">
			<PeakStatusHero v-if="status" :status="status" />

			<section v-if="status">
				<h6 class="section-title">{{ $t("battery.peakStatus.now") }}</h6>
				<p class="mb-2" data-testid="peak-status-detail">{{ detail }}</p>
				<div class="d-flex flex-wrap gap-2" data-testid="peak-status-notes">
					<span
						v-for="n in noteList"
						:key="n.text"
						class="pill"
						:class="`pill-${n.tone}`"
						>{{ n.text }}</span
					>
				</div>
			</section>

			<section v-if="status && windowData" data-testid="peak-status-window">
				<h6 class="section-title">
					{{ $t("battery.peakStatus.window.title") }}
					{{ windowRange }} ·
					{{ $t("battery.peakStatus.window.left", { minutes: windowData.minutesLeft }) }}
				</h6>
				<div class="bar" :class="{ 'bar-warn': windowData.warn }">
					<div class="bar-fill" :style="{ width: `${windowData.avgFill * 100}%` }"></div>
					<div class="bar-time" :style="{ left: `${windowData.elapsed * 100}%` }"></div>
				</div>
				<div class="d-flex justify-content-between small evcc-gray mt-1">
					<span
						>{{ $t("battery.peakStatus.window.avg") }}
						{{ fmt.w(status.window?.avg ?? 0) }}</span
					>
					<span
						>{{ $t("battery.peakStatus.window.limit") }} {{ fmt.w(status.limit) }}</span
					>
				</div>
				<div class="small mt-1" :class="{ 'text-warning': windowData.warn }">
					{{
						$t("battery.peakStatus.window.allowed", {
							power: fmt.w(windowData.allowed),
						})
					}}
				</div>
			</section>

			<section v-if="status" class="tiles" data-testid="peak-status-month">
				<div class="tile" data-testid="peak-status-month-peak">
					<div class="section-title">{{ $t("battery.peakStatus.month.title") }}</div>
					<div class="tile-value">{{ monthPeak.value }}</div>
					<div class="tile-sub">{{ monthPeak.sub }}</div>
				</div>
				<div class="tile" data-testid="peak-status-month-covered">
					<div class="section-title">{{ $t("battery.peakStatus.month.covered") }}</div>
					<div class="tile-value">{{ status.month?.interventions ?? 0 }}</div>
					<div class="tile-sub">{{ $t("battery.peakStatus.month.thisMonth") }}</div>
				</div>
			</section>

			<PeakStatusLoads v-if="showLoads" />

			<PeakStatusEventList
				v-if="actions.length"
				:events="actions"
				:title="$t('battery.peakStatus.actions')"
				:now="now"
				section="peak-status-actions"
				row="peak-status-action"
				more="peak-status-actions-more"
			/>

			<PeakStatusEventList
				v-if="status"
				:events="events"
				:title="$t('battery.peakStatus.recent')"
				:empty="$t('battery.peakStatus.noEvents')"
				:now="now"
				section="peak-status-events"
				row="peak-status-event"
				more="peak-status-more"
			/>
		</div>
	</GenericModal>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import api from "@/api";
import store from "@/store";
import formatter from "@/mixins/formatter";
import type { LmEvent, PeakStatus } from "@/types/evcc-lm";
import GenericModal from "../Helper/GenericModal.vue";
import PeakStatusHero from "./PeakStatusHero.vue";
import PeakStatusLoads from "./PeakStatusLoads.vue";
import PeakStatusEventList from "./PeakStatusEventList.vue";
import { dialogTitleKey, loadsShown } from "./peakDialog";
import { actionEvents, detailText, makeFmt, notes, peakEvents, windowInfo } from "./peakStatus";

const TICK = 15_000;

// Custom extension: what peak shaving does with the battery and why, see
// core/site_peak_status.go, and the loads and events of the load management.
// Without peak shaving only the loads and events show. Opened from the "more"
// menu (MoreMenuItems.vue).
export default defineComponent({
	name: "PeakStatusModal",
	components: { GenericModal, PeakStatusHero, PeakStatusLoads, PeakStatusEventList },
	mixins: [formatter],
	data() {
		return {
			visible: false,
			now: Date.now(),
			fetched: null as LmEvent[] | null,
			timer: undefined as ReturnType<typeof setInterval> | undefined,
		};
	},
	computed: {
		status(): PeakStatus | undefined {
			const st = store.state.peakStatus;
			return st?.state ? st : undefined;
		},
		titleKey(): string {
			return dialogTitleKey(store.state);
		},
		showLoads(): boolean {
			return loadsShown(store.state);
		},
		fmt() {
			return makeFmt(
				(watt: number) => this.fmtW(watt),
				(percent: number) => this.fmtPercentage(percent),
				(watt: number) => this.fmtW(watt, this.POWER_UNIT.KW, false)
			);
		},
		detail(): string {
			return this.status ? detailText(this.status, this.tr, this.fmt) : "";
		},
		noteList() {
			return this.status ? notes(this.status, this.tr, this.fmt) : [];
		},
		windowData() {
			return this.status ? windowInfo(this.status, this.now) : null;
		},
		windowRange(): string {
			const w = this.status?.window;
			if (!w) return "";
			return `${this.fmtHourMinute(new Date(w.start))}–${this.fmtHourMinute(new Date(w.end))}`;
		},
		monthPeak(): { value: string; sub: string } {
			const m = this.status?.month;
			if (!m || !m.peak) return { value: this.$t("battery.peakStatus.month.none"), sub: "" };
			return {
				value: this.fmt.w(m.peak),
				sub: this.fmtWeekdayDayTime(new Date(m.peakAt)),
			};
		},
		// the newest event the status reports, changes with every new line
		newestEvent(): string {
			const e = store.state.lmStatus?.events?.[0];
			return e ? `${e.at}${e.type}` : "";
		},
		// the whole list from the api, until it is there what the status publishes
		allEvents(): LmEvent[] {
			return this.fetched ?? store.state.lmStatus?.events ?? [];
		},
		// peak shaving: "Zuletzt"
		events(): LmEvent[] {
			return peakEvents(this.allEvents);
		},
		// load management: "Letzte Eingriffe"
		actions(): LmEvent[] {
			return actionEvents(this.allEvents);
		},
	},
	watch: {
		// a change of the state: the list has a new line
		"status.state"() {
			if (this.visible) this.load();
		},
		// a new event while the dialog is open (also one of the load management)
		newestEvent() {
			if (this.visible) this.load();
		},
	},
	beforeUnmount() {
		clearInterval(this.timer);
	},
	methods: {
		tr(key: string, params?: Record<string, unknown>): string {
			return this.$t(key, params as Record<string, string>);
		},
		async load() {
			try {
				const res = await api.get<LmEvent[]>("peakshaving/events");
				this.fetched = res.data;
			} catch {
				// the status publishes the newest ones meanwhile
			}
		},
		onOpen() {
			this.visible = true;
			this.now = Date.now();
			this.timer = setInterval(() => (this.now = Date.now()), TICK);
			this.load();
		},
		onClosed() {
			this.visible = false;
			clearInterval(this.timer);
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
.tiles {
	display: grid;
	grid-template-columns: repeat(2, minmax(0, 1fr));
	gap: 0.75rem;
}
.tile {
	background: var(--evcc-background);
	border-radius: 0.75rem;
	padding: 0.6rem 0.8rem;
	min-width: 0;
}
.tile .section-title {
	margin-bottom: 0.1rem;
}
.tile-value {
	font-size: 1.35rem;
	font-weight: bold;
	line-height: 1.3;
	overflow-wrap: anywhere;
}
.tile-sub {
	min-height: 1.2em;
	font-size: 0.8rem;
	color: var(--evcc-gray);
}
.pill {
	display: inline-block;
	font-size: 0.8rem;
	padding: 0.15rem 0.6rem;
	border-radius: 1rem;
	white-space: normal;
}
.pill-plain {
	color: var(--evcc-gray);
	background: var(--bs-secondary-bg);
}
.pill-ok {
	color: var(--bs-success-text-emphasis);
	background: var(--bs-success-bg-subtle);
}
.pill-bad {
	color: var(--bs-danger-text-emphasis);
	background: var(--bs-danger-bg-subtle);
}
.bar {
	position: relative;
	height: 8px;
	background: var(--bs-border-color);
	border-radius: 4px;
}
.bar-fill {
	height: 8px;
	border-radius: 4px;
	background: var(--evcc-darker-green);
}
.bar-warn .bar-fill {
	background: var(--evcc-orange);
}
.bar-time {
	position: absolute;
	top: -3px;
	width: 2px;
	height: 14px;
	margin-left: -1px;
	background: var(--evcc-default-text);
}
</style>
