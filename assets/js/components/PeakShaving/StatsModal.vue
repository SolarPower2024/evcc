<template>
	<GenericModal
		id="peakStatsModal"
		ref="modal"
		size="lg"
		:title="$t('peakstats.title')"
		data-testid="peak-stats-modal"
		@open="visible = true"
		@closed="visible = false"
	>
		<div v-if="visible">
			<p class="text-gray">{{ $t("peakstats.description") }}</p>

			<p v-if="!months.length" class="text-muted mb-0">{{ $t("peakstats.empty") }}</p>
			<template v-else>
				<div class="tiles mb-4" data-testid="peak-stats-current">
					<div class="tile">
						<div class="tile-label">{{ $t("peakstats.withBattery") }}</div>
						<div class="tile-value" :class="{ 'text-danger': overLimit }">
							{{ power(current.peak, current.peakAt, false) }}
							<span class="tile-unit">/ {{ fmtW(limit) }}</span>
						</div>
						<div class="tile-sub">{{ when(current.peakAt) }}</div>
					</div>
					<div class="tile">
						<div class="tile-label">{{ $t("peakstats.withoutBattery") }}</div>
						<div class="tile-value">
							{{ power(current.demand, current.demandAt, false) }}
							<span v-if="recorded(current.demandAt)" class="tile-unit">kW</span>
						</div>
						<div class="tile-sub">{{ when(current.demandAt) }}</div>
					</div>
					<div v-if="currentCost" class="tile" data-testid="peak-stats-saving">
						<div class="tile-label">{{ $t("peakstats.saving") }}</div>
						<div class="tile-value" :class="{ 'text-danger': currentCost.saving < 0 }">
							{{ money(currentCost.saving) }}
						</div>
						<div class="tile-sub">
							{{ $t("peakstats.savingTotal", { total: money(totalSaving) }) }}
						</div>
					</div>
					<div class="tile">
						<div class="tile-label">{{ $t("peakstats.interventions") }}</div>
						<div class="tile-value">{{ current.interventions }}</div>
						<div class="tile-sub">{{ currentMonthName }}</div>
					</div>
				</div>

				<div class="table-responsive d-none d-sm-block">
					<table class="table table-sm align-middle mb-0">
						<thead>
							<tr class="evcc-gray small">
								<th>{{ $t("peakstats.month") }}</th>
								<th class="text-end">{{ $t("peakstats.withBattery") }}</th>
								<th class="text-end">{{ $t("peakstats.withoutBattery") }}</th>
								<th v-if="hasTariff" class="text-end">
									<span class="d-sm-none">{{ $t("peakstats.costShort") }}</span>
									<span class="d-none d-sm-inline">{{
										$t("peakstats.cost")
									}}</span>
								</th>
							</tr>
						</thead>
						<tbody>
							<tr v-for="m in months" :key="m.month" data-testid="peak-stats-month">
								<td class="text-nowrap">
									{{ monthName(m.month) }}
									<div class="evcc-gray small">{{ monthInfo(m) }}</div>
								</td>
								<td class="text-end text-nowrap">
									{{ power(m.peak, m.peakAt) }}
									<div class="evcc-gray small d-none d-sm-block">
										{{ when(m.peakAt) }}
									</div>
								</td>
								<td class="text-end text-nowrap">
									{{ power(m.demand, m.demandAt) }}
									<div class="evcc-gray small d-none d-sm-block">
										{{ when(m.demandAt) }}
									</div>
								</td>
								<td v-if="hasTariff" class="text-end">
									<template v-if="costOf(m.month)">
										{{ money(costOf(m.month).cost) }}
										<div
											class="small"
											:class="
												costOf(m.month).saving < 0
													? 'text-danger'
													: 'evcc-gray'
											"
										>
											{{ savingText(costOf(m.month).saving) }}
										</div>
									</template>
								</td>
							</tr>
						</tbody>
					</table>
				</div>

				<ul class="list-unstyled mb-0 d-sm-none" data-testid="peak-stats-list">
					<li
						v-for="m in months"
						:key="m.month"
						class="month-row py-2"
						data-testid="peak-stats-month-mobile"
					>
						<div class="d-flex justify-content-between align-items-baseline gap-3">
							<strong>{{ monthName(m.month) }}</strong>
							<div v-if="hasTariff && costOf(m.month)" class="text-end">
								{{ money(costOf(m.month).cost) }}
								<div
									class="small"
									:class="
										costOf(m.month).saving < 0 ? 'text-danger' : 'evcc-gray'
									"
								>
									{{ savingText(costOf(m.month).saving) }}
								</div>
							</div>
						</div>
						<div class="evcc-gray small">{{ monthSummary(m) }}</div>
					</li>
				</ul>
			</template>
		</div>
	</GenericModal>
</template>

<script>
import GenericModal from "../Helper/GenericModal.vue";
import formatter from "@/mixins/formatter";
import store from "@/store";

// Custom extension: the highest quarter hour of each month with and without the
// battery, see core/site_peak_stats.go. Opened from the more menu.
export default {
	name: "PeakStatsModal",
	components: { GenericModal },
	mixins: [formatter],
	data() {
		return { visible: false };
	},
	computed: {
		months() {
			return store.state?.peakMonths || [];
		},
		current() {
			return this.months[0];
		},
		currentMonthName() {
			const [y, m] = this.current.month.split("-").map(Number);
			return this.fmtMonthYear(new Date(y, m - 1, 1));
		},
		// the month's baseline, the highest limit set by hand in it, see
		// core/site_peak_stats.go; before the first one: the limit now
		limit() {
			if (this.current?.baseline) return this.current.baseline;
			const follow = store.state?.peakFollow;
			if (follow?.enabled && follow.base) return follow.base;
			return store.state?.peakShavingLimit || 0;
		},
		// custom: capacity tariff, see core/site_peak_tariff.go
		costs() {
			return store.state?.peakTariff?.months || [];
		},
		hasTariff() {
			return this.costs.length > 0;
		},
		currentCost() {
			return this.costOf(this.current?.month);
		},
		totalSaving() {
			return this.costs.reduce((sum, c) => sum + c.saving, 0);
		},
		overLimit() {
			return this.limit > 0 && this.current.peak > this.limit;
		},
	},
	methods: {
		limitText(m) {
			return m.baseline > 0
				? this.$t("peakstats.limitShort", { limit: this.fmtW(m.baseline) })
				: "";
		},
		// second line of the month in the table
		monthInfo(m) {
			return [
				this.$t("peakstats.interventionsCount", { count: m.interventions }),
				this.limitText(m),
			]
				.filter(Boolean)
				.join(" · ");
		},
		// second line of the month on a phone, where the table does not fit
		monthSummary(m) {
			return [
				this.$t("peakstats.withShort", { power: this.power(m.peak, m.peakAt) }),
				this.$t("peakstats.withoutShort", { power: this.power(m.demand, m.demandAt) }),
				this.$t("peakstats.interventionsCount", { count: m.interventions }),
				this.limitText(m),
			]
				.filter(Boolean)
				.join(" · ");
		},
		costOf(month) {
			return this.costs.find((c) => c.month === month);
		},
		savingText(saving) {
			return saving < 0
				? this.$t("peakstats.extraCost", { cost: this.money(-saving) })
				: this.$t("peakstats.savingShort", { saving: this.money(saving) });
		},
		money(amount) {
			return this.fmtMoney(amount, store.state?.currency, true, true);
		},
		monthName(month) {
			const [y, m] = month.split("-").map(Number);
			return new Intl.DateTimeFormat(this.$i18n?.locale, {
				month: "short",
				year: "numeric",
			}).format(new Date(y, m - 1, 1));
		},
		recorded(at) {
			return !!at && !at.startsWith("0001");
		},
		// no complete quarter hour yet this month
		power(watt, at, withUnit = true) {
			return this.recorded(at) ? this.fmtW(watt, this.POWER_UNIT.KW, withUnit) : "–";
		},
		when(at) {
			if (!this.recorded(at)) return "";
			const d = new Date(at);
			return `${this.fmtDayMonthShort(d)} ${this.fmtHourMinute(d)}`;
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
.month-row + .month-row {
	border-top: 1px solid var(--bs-border-color);
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
</style>
