<template>
	<GenericModal
		id="peakStatusModal"
		:title="$t('battery.peakStatus.title')"
		size="md"
		data-testid="peak-status-modal"
		@open="onOpen"
		@closed="onClosed"
	>
		<div v-if="visible && status" class="d-flex flex-column gap-4">
			<PeakStatusHero :status="status" />

			<section>
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

			<section v-if="windowData" data-testid="peak-status-window">
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
						{{ power(status.window?.avg ?? 0) }}</span
					>
					<span
						>{{ $t("battery.peakStatus.window.limit") }} {{ power(status.limit) }}</span
					>
				</div>
				<div class="small mt-1" :class="{ 'text-warning': windowData.warn }">
					{{
						$t("battery.peakStatus.window.allowed", {
							power: power(windowData.allowed),
						})
					}}
				</div>
			</section>

			<section data-testid="peak-status-month">
				<h6 class="section-title">{{ $t("battery.peakStatus.month.title") }}</h6>
				<div>{{ monthPeak }}</div>
				<div class="small evcc-gray">{{ monthInterventions }}</div>
			</section>

			<section data-testid="peak-status-events">
				<h6 class="section-title">{{ $t("battery.peakStatus.recent") }}</h6>
				<p v-if="!events.length" class="text-muted mb-0">
					{{ $t("battery.peakStatus.noEvents") }}
				</p>
				<div
					v-for="(e, i) in shownEvents"
					:key="i"
					class="d-flex gap-3 event"
					data-testid="peak-status-event"
				>
					<span class="evcc-gray event-time text-nowrap">{{ eventTime(e) }}</span>
					<span>{{ eventLine(e) }}</span>
				</div>
				<button
					v-if="events.length > FEW"
					type="button"
					class="btn btn-link btn-sm p-0 mt-1"
					data-testid="peak-status-more"
					@click="all = !all"
				>
					{{ $t(all ? "battery.peakStatus.less" : "battery.peakStatus.more") }}
				</button>
			</section>
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
import { detailText, eventText, notes, peakEvents, windowInfo } from "./peakStatus";

const FEW = 5;
const TICK = 15_000;

// Custom extension: the dialog of the battery page, what peak shaving does with
// the battery and why, see core/site_peak_status.go. Opened from PeakStatusLink.
export default defineComponent({
	name: "PeakStatusModal",
	components: { GenericModal, PeakStatusHero },
	mixins: [formatter],
	data() {
		return {
			visible: false,
			all: false,
			now: Date.now(),
			fetched: null as LmEvent[] | null,
			timer: undefined as ReturnType<typeof setInterval> | undefined,
			FEW,
		};
	},
	computed: {
		status(): PeakStatus | undefined {
			const st = store.state.peakStatus;
			return st?.state ? st : undefined;
		},
		detail(): string {
			return this.status ? detailText(this.status, this.tr, this.power) : "";
		},
		noteList() {
			return this.status ? notes(this.status, this.tr, this.power) : [];
		},
		windowData() {
			return this.status ? windowInfo(this.status, this.now) : null;
		},
		windowRange(): string {
			const w = this.status?.window;
			if (!w) return "";
			return `${this.fmtHourMinute(new Date(w.start))}–${this.fmtHourMinute(new Date(w.end))}`;
		},
		monthPeak(): string {
			const m = this.status?.month;
			if (!m || !m.peak) return this.$t("battery.peakStatus.month.none");
			return this.$t("battery.peakStatus.month.peak", {
				power: this.power(m.peak),
				date: this.fmtWeekdayDayTime(new Date(m.peakAt)),
			});
		},
		monthInterventions(): string {
			return this.$t("battery.peakStatus.month.interventions", {
				count: this.status?.month?.interventions ?? 0,
			});
		},
		events(): LmEvent[] {
			// the whole list from the api, until it is there what the status publishes
			return this.fetched ?? peakEvents(store.state.lmStatus?.events);
		},
		shownEvents(): LmEvent[] {
			return this.all ? this.events : this.events.slice(0, FEW);
		},
	},
	watch: {
		// a change of the state: the list has a new line
		"status.state"() {
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
		power(watt: number): string {
			return this.fmtW(watt);
		},
		eventTime(e: LmEvent): string {
			const at = new Date(e.at);
			const today = new Date(this.now).toDateString() === at.toDateString();
			return today ? this.fmtHourMinute(at) : this.fmtWeekdayDayTime(at);
		},
		eventLine(e: LmEvent): string {
			return eventText(e, this.tr, this.power) ?? this.lmEventText(e);
		},
		// the older types: the same texts as the load management overview
		lmEventText(e: LmEvent): string {
			const k = (name: string, p: Record<string, string>) =>
				this.$t(`lmoverview.event.${name}`, p);
			if (e.type === "peak") {
				return k("peak", { demand: this.power(e.a), limit: this.power(e.b) });
			}
			if (e.type === "gridChargePaused") {
				return k("gridChargePaused", { demand: this.power(e.a), limit: this.power(e.b) });
			}
			return e.type;
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
			this.all = false;
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
	font-size: 0.8rem;
	text-transform: uppercase;
	color: var(--evcc-gray);
	margin-bottom: 0.5rem;
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
.event {
	font-size: 0.9rem;
	line-height: 1.8;
}
.event-time {
	min-width: 4.5rem;
}
</style>
