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
			<!-- custom: switch load management off, see core/site_lm_switch.go -->
			<div class="d-flex justify-content-between align-items-center mb-1">
				<div class="form-check form-switch mb-0">
					<input
						id="lmEnabled"
						:checked="lmEnabled"
						class="form-check-input"
						type="checkbox"
						role="switch"
						data-testid="lm-enabled"
						:disabled="switching"
						@change="changeEnabled"
					/>
					<label class="form-check-label" for="lmEnabled">
						{{ $t("lmoverview.enabled") }}
					</label>
				</div>
				<span
					class="pill ms-3 flex-shrink-0"
					:class="overall.class"
					data-testid="lm-overall"
					>{{ overall.text }}</span
				>
			</div>
			<p class="small evcc-gray mb-3" data-testid="lm-enabled-help">
				{{ enabledHelp }}
			</p>
			<p v-if="switchError" class="text-danger small">{{ switchError }}</p>

			<div class="tiles mb-4">
				<div v-for="c in circuits" :key="c.name" class="tile" data-testid="lm-circuit">
					<div class="tile-label">{{ c.title }}</div>
					<div class="tile-value">
						{{ fmtW(c.power, POWER_UNIT.KW, false) }}
						<span v-if="c.lifted" class="tile-unit">kW</span>
						<span v-else class="tile-unit">/ {{ fmtW(c.maxPower) }}</span>
					</div>
					<div v-if="c.note" class="tile-sub" data-testid="lm-circuit-note">
						{{ c.note }}
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
					<div v-if="peak.allowed" class="tile-sub" data-testid="lm-peak-allowed">
						{{ peak.allowed }}
					</div>
					<div v-if="peak.follow" class="tile-sub" data-testid="lm-peak-follow">
						{{ peak.follow }}
					</div>
				</div>
			</div>

			<h6 class="small evcc-gray mb-2">{{ $t("lmoverview.loads") }}</h6>
			<p v-if="!loads.length" class="text-muted">{{ $t("lmoverview.noLoads") }}</p>
			<div v-else class="table-responsive mb-4">
				<table class="table table-sm align-middle mb-0">
					<tbody>
						<tr v-for="l in loads" :key="l.name" :data-testid="`lm-load-${l.name}`">
							<td>
								<span class="d-inline-flex align-items-center text-nowrap">
									{{ l.battery ? $t("lmoverview.battery") : l.title || l.name }}
									<shopicon-regular-lock
										v-if="l.protected && l.state === 'shed'"
										size="s"
										class="evcc-gray lock"
										:title="$t('lmoverview.protected')"
									></shopicon-regular-lock>
								</span>
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
import api from "@/api";

// Custom extension: what load management is doing right now, see
// core/site_lm_status.go. Opened from the more menu, like the vehicle settings.
export default {
	name: "LmOverviewModal",
	components: { GenericModal },
	mixins: [formatter],
	data() {
		return { visible: false, switching: false, switchError: "" };
	},
	computed: {
		state() {
			return store.state;
		},
		status() {
			return this.state?.lmStatus;
		},
		lmEnabled() {
			return this.state?.lmOff?.enabled ?? true;
		},
		// the load management (peak) circuit, chosen under Erweitert, empty = all
		lmCircuit() {
			const name = this.state?.lmOff?.circuit;
			return name && this.state?.circuits?.[name] ? name : "";
		},
		enabledHelp() {
			const c = this.lmCircuit;
			const circuit = c ? this.state.circuits[c].title || c : "";
			if (this.lmEnabled) {
				return circuit
					? this.$t("lmoverview.enabledHelpCircuit", { circuit })
					: this.$t("lmoverview.enabledHelp");
			}
			return circuit
				? this.$t("lmoverview.disabledHelpCircuit", { circuit })
				: this.$t("lmoverview.disabledHelp");
		},
		circuits() {
			// configured limits of circuits changed at runtime (switched off, following the peak)
			const configured = this.state?.lmOff?.limits || {};
			return Object.entries(this.state?.circuits || {})
				.filter(([name]) => !this.lmCircuit || name === this.lmCircuit)
				.filter(([name, c]) => c.maxPower > 0 || configured[name] > 0)
				.map(([name, c]) => {
					const lifted = !(c.maxPower > 0);
					const limit = lifted ? configured[name] : c.maxPower;
					const ratio = c.power / limit;
					let barClass = lifted ? "bg-secondary" : "bg-success";
					if (!lifted && ratio > 1) barClass = "bg-danger";
					else if (!lifted && ratio >= 0.9) barClass = "bg-warning";
					let note = "";
					if (lifted) {
						note = this.$t("lmoverview.circuitLifted", { limit: this.fmtW(limit) });
					} else if (configured[name] > 0 && configured[name] !== c.maxPower) {
						note = this.$t("lmoverview.circuitFollows", {
							limit: this.fmtW(configured[name]),
						});
					}
					return {
						name,
						title: c.title || name,
						power: c.power || 0,
						maxPower: limit,
						lifted,
						note,
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
			const end = this.state.peakShavingWindowEnd;
			const allowed =
				this.state.peakShaving && end
					? this.$t("lmoverview.peakAllowed", {
							power: this.fmtW(this.state.peakShavingAllowed || 0),
							time: this.fmtHourMinute(new Date(end)),
						})
					: "";
			// follow the peak raised the limit this month, see core/site_peak_follow.go
			const f = this.state.peakFollow;
			const limit = this.state.peakShavingLimit || 0;
			const follow =
				f?.enabled && limit > f.base
					? this.$t("lmoverview.peakFollow", { limit: this.fmtW(f.base) })
					: "";
			return {
				avg: this.state.peakShavingWindowAvg || 0,
				limit,
				text,
				allowed,
				follow,
			};
		},
		loads() {
			return [...(this.status?.loads || [])].sort((a, b) => b.priority - a.priority);
		},
		overall() {
			if (!this.lmEnabled) {
				return { text: this.$t("lmoverview.overall.off"), class: "pill-muted" };
			}
			if (this.circuits.some((c) => !c.lifted && c.power > c.maxPower)) {
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
		async changeEnabled(e) {
			const target = e.target;
			this.switching = true;
			this.switchError = "";
			try {
				await api.post(`lmenabled/${target.checked}`);
			} catch (err) {
				target.checked = this.lmEnabled;
				this.switchError = err?.response?.data?.error || err.message;
			}
			this.switching = false;
		},
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
				// the power stands left of it
				case "running":
					return l.battery ? t("charging") : this.$t("lmoverview.state.running");
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
				case "notFollowing":
					return this.$t("lmoverview.event.notFollowing", {
						load: e.load === "battery" ? this.$t("lmoverview.battery") : e.load,
						power: kw(e.a),
						allowed: kw(e.b),
					});
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
