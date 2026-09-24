<template>
	<GenericModal
		id="lmOverviewModal"
		ref="modal"
		size="lg"
		:title="$t('lmoverview.title')"
		data-testid="lm-overview-modal"
		@open="visible = true"
		@closed="visible = false"
	>
		<div v-if="visible">
			<div class="d-flex justify-content-between align-items-center mb-3">
				<p class="text-gray my-0">{{ $t("lmoverview.description") }}</p>
				<span class="pill ms-3 flex-shrink-0" :class="overall.class">{{
					overall.text
				}}</span>
			</div>

			<div class="tiles mb-4">
				<div v-for="c in circuits" :key="c.name" class="tile" data-testid="lm-circuit">
					<div class="tile-label">{{ c.title }}</div>
					<div class="tile-value">
						{{ fmtW(c.power, POWER_UNIT.KW, false) }}
						<span class="tile-unit">/ {{ fmtW(c.maxPower) }}</span>
					</div>
					<div class="bar">
						<div
							class="bar-fill"
							:class="c.barClass"
							:style="{ width: `${c.percent}%` }"
						></div>
					</div>
				</div>

				<div v-if="peak" class="tile" data-testid="lm-peak">
					<div class="tile-label">{{ $t("lmoverview.peak") }}</div>
					<div class="tile-value">
						{{ fmtW(peak.avg, POWER_UNIT.KW, false) }}
						<span class="tile-unit">/ {{ fmtW(peak.limit) }}</span>
					</div>
					<div class="tile-sub">{{ peak.text }}</div>
				</div>

				<div v-if="battery" class="tile" data-testid="lm-gridcharge">
					<div class="tile-label">{{ $t("lmoverview.gridCharge") }}</div>
					<div class="tile-value">{{ gridCharge.value }}</div>
					<div class="tile-sub">{{ gridCharge.sub }}</div>
				</div>
			</div>

			<h6 class="small evcc-gray mb-2">{{ $t("lmoverview.loads") }}</h6>
			<p v-if="!loads.length" class="text-muted">{{ $t("lmoverview.noLoads") }}</p>
			<div v-else class="table-responsive mb-4">
				<table class="table table-sm align-middle mb-0">
					<tbody>
						<tr v-for="l in loads" :key="l.name" :data-testid="`lm-load-${l.name}`">
							<td class="text-nowrap">
								{{ l.battery ? $t("lmoverview.battery") : l.title || l.name }}
								<shopicon-regular-lock
									v-if="l.protected"
									size="s"
									class="evcc-gray lock"
									:title="$t('lmoverview.protected')"
								></shopicon-regular-lock>
							</td>
							<td class="evcc-gray text-nowrap">
								{{ $t("lmoverview.priority", { priority: l.priority }) }}
							</td>
							<td class="text-end text-nowrap">{{ fmtW(l.power) }}</td>
							<td class="text-end">
								<span class="pill" :class="stateClass(l)">{{ stateText(l) }}</span>
							</td>
						</tr>
					</tbody>
				</table>
			</div>

			<h6 class="small evcc-gray mb-2">{{ $t("lmoverview.events") }}</h6>
			<p v-if="!events.length" class="text-muted mb-0">{{ $t("lmoverview.noEvents") }}</p>
			<div
				v-for="(e, i) in events"
				:key="i"
				class="d-flex gap-3 event"
				data-testid="lm-event"
			>
				<span class="evcc-gray event-time">{{ fmtHourMinute(new Date(e.at)) }}</span>
				<span>{{ eventText(e) }}</span>
			</div>
		</div>
	</GenericModal>
</template>

<script>
import "@h2d2/shopicons/es/regular/lock";
import GenericModal from "../Helper/GenericModal.vue";
import formatter from "@/mixins/formatter";
import store from "@/store";

// Custom extension: what load management is doing right now, see
// core/site_lm_status.go. Opened from the more menu, like the vehicle settings.
export default {
	name: "LmOverviewModal",
	components: { GenericModal },
	mixins: [formatter],
	data() {
		return { visible: false };
	},
	computed: {
		state() {
			return store.state;
		},
		status() {
			return this.state?.lmStatus;
		},
		circuits() {
			return Object.entries(this.state?.circuits || {})
				.filter(([, c]) => c.maxPower > 0)
				.map(([name, c]) => {
					const ratio = c.power / c.maxPower;
					let barClass = "bg-success";
					if (ratio > 1) barClass = "bg-danger";
					else if (ratio >= 0.9) barClass = "bg-warning";
					return {
						name,
						title: c.title || name,
						power: c.power || 0,
						maxPower: c.maxPower,
						percent: Math.min(100, Math.round(ratio * 100)),
						barClass,
					};
				});
		},
		peak() {
			if (!this.state?.peakShavingEntity) return null;
			let text = this.$t("lmoverview.peakOff");
			if (this.state.peakShaving) {
				text = this.state.peakShavingActive
					? this.$t("lmoverview.peakReserve", {
							setpoint: this.fmtW(this.state.peakShavingPower || 0),
						})
					: this.$t("lmoverview.peakFree");
			}
			return {
				avg: this.state.peakShavingWindowAvg || 0,
				limit: this.state.peakShavingLimit || 0,
				text,
			};
		},
		loads() {
			return [...(this.status?.loads || [])].sort((a, b) => b.priority - a.priority);
		},
		battery() {
			return this.loads.find((l) => l.battery);
		},
		gridCharge() {
			const b = this.battery;
			switch (b.state) {
				case "running":
					return {
						value: this.fmtW(b.power),
						sub: b.allowed
							? this.$t("lmoverview.setpoint", { power: this.fmtW(b.allowed) })
							: this.$t("lmoverview.state.running"),
					};
				case "paused":
					return {
						value: this.$t("lmoverview.state.paused"),
						sub: this.$t("lmoverview.pausedPeak", { time: this.until(b) }),
					};
				case "shed":
					return {
						value: this.$t("lmoverview.state.blocked"),
						sub: this.$t("lmoverview.blockedCircuit", { time: this.until(b) }),
					};
			}
			return { value: this.$t("lmoverview.state.off"), sub: "" };
		},
		overall() {
			if (this.circuits.some((c) => c.power > c.maxPower)) {
				return { text: this.$t("lmoverview.overall.overload"), class: "pill-danger" };
			}
			const limited = ["throttled", "shed", "waiting", "paused"];
			if (this.loads.some((l) => limited.includes(l.state))) {
				return { text: this.$t("lmoverview.overall.limited"), class: "pill-warning" };
			}
			return { text: this.$t("lmoverview.overall.normal"), class: "pill-success" };
		},
		events() {
			return this.status?.events || [];
		},
	},
	methods: {
		until(l) {
			return l.until ? this.fmtHourMinute(new Date(l.until)) : "–";
		},
		stateClass(l) {
			return {
				running: "pill-success",
				throttled: "pill-warning",
				shed: "pill-danger",
				waiting: "pill-info",
				paused: "pill-muted",
				off: "pill-muted",
			}[l.state];
		},
		stateText(l) {
			const t = (key, params) => this.$t(`lmoverview.stateDetail.${key}`, params);
			switch (l.state) {
				case "running":
					return l.battery && l.allowed
						? t("charging", { power: this.fmtW(l.allowed) })
						: this.$t("lmoverview.state.running");
				case "throttled":
					return t("throttled", {
						allowed: this.fmtW(l.allowed, this.POWER_UNIT.KW, false),
						requested: this.fmtW(l.requested),
					});
				case "shed":
					return l.battery
						? t("blocked", { time: this.until(l) })
						: t("shed", { time: this.until(l) });
				case "waiting":
					return t("waiting", {
						requested: this.fmtW(l.requested),
						allowed: this.fmtW(Math.max(0, l.allowed)),
					});
				case "paused":
					return t("paused", { time: this.until(l) });
			}
			return this.$t("lmoverview.state.off");
		},
		eventText(e) {
			const kw = (w) => this.fmtW(w);
			switch (e.type) {
				case "shed":
					return e.a > 0
						? this.$t("lmoverview.event.shedGuard", {
								load: e.load,
								power: kw(e.b),
								minutes: Math.round(e.a),
							})
						: this.$t("lmoverview.event.shed", { load: e.load, power: kw(e.b) });
				case "throttled":
					return this.$t("lmoverview.event.throttled", {
						load: e.load,
						requested: this.fmtW(e.a, this.POWER_UNIT.KW, false),
						allowed: kw(e.b),
					});
				case "gridChargePaused":
					return this.$t("lmoverview.event.gridChargePaused", {
						demand: kw(e.a),
						limit: kw(e.b),
					});
				case "gridChargeDenied":
					return this.$t("lmoverview.event.gridChargeDenied", {
						allowed: kw(e.a),
						wanted: kw(e.b),
					});
				case "peak":
					return this.$t("lmoverview.event.peak", { demand: kw(e.a), limit: kw(e.b) });
			}
			return e.type;
		},
	},
};
</script>

<style scoped>
.tiles {
	display: grid;
	grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
	gap: 0.75rem;
}
.tile {
	background: var(--evcc-box);
	border: 1px solid var(--bs-border-color);
	border-radius: 0.75rem;
	padding: 0.75rem 1rem;
}
.tile-label,
.tile-sub {
	font-size: 0.8rem;
	color: var(--evcc-gray);
}
.tile-value {
	font-size: 1.35rem;
	font-weight: bold;
}
.tile-unit {
	font-size: 0.85rem;
	font-weight: normal;
	color: var(--evcc-gray);
}
.bar {
	height: 6px;
	background: var(--bs-border-color);
	border-radius: 3px;
	margin-top: 0.4rem;
	overflow: hidden;
}
.bar-fill {
	height: 6px;
	border-radius: 3px;
}
.pill {
	display: inline-block;
	font-size: 0.8rem;
	padding: 0.15rem 0.6rem;
	border-radius: 1rem;
	white-space: nowrap;
}
.pill-success {
	color: var(--bs-success-text-emphasis);
	background: var(--bs-success-bg-subtle);
}
.pill-warning {
	color: var(--bs-warning-text-emphasis);
	background: var(--bs-warning-bg-subtle);
}
.pill-danger {
	color: var(--bs-danger-text-emphasis);
	background: var(--bs-danger-bg-subtle);
}
.pill-info {
	color: var(--bs-info-text-emphasis);
	background: var(--bs-info-bg-subtle);
}
.pill-muted {
	color: var(--evcc-gray);
	background: var(--bs-secondary-bg);
}
.lock {
	vertical-align: -2px;
	margin-left: 0.25rem;
}
.event {
	font-size: 0.9rem;
	line-height: 1.8;
}
.event-time {
	min-width: 3rem;
}
</style>
