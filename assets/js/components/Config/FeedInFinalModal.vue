<template>
	<GenericModal
		id="feedInFinalModal"
		ref="modal"
		:title="$t('config.feedinfinal.title')"
		data-testid="feedinfinal-modal"
		config-modal-name="feedinfinal"
		size="lg"
		@open="open"
	>
		<p>{{ $t("config.feedinfinal.description", { day: finalizeDay }) }}</p>
		<p class="text-muted">
			<strong>{{ $t("config.feedinfinal.published") }}</strong>
			{{ publishedText }}
		</p>

		<h6 class="mt-4">{{ $t("config.feedinfinal.monthsLabel") }}</h6>
		<p v-if="!months.length" class="text-muted">{{ $t("config.feedinfinal.none") }}</p>
		<div v-else class="table-responsive">
			<table class="table table-sm align-middle">
				<thead>
					<tr>
						<th>{{ $t("config.feedinfinal.month") }}</th>
						<th class="text-end">{{ $t("config.feedinfinal.market") }}</th>
						<th class="text-end">{{ $t("config.feedinfinal.applied") }}</th>
						<th class="text-end">{{ $t("config.feedinfinal.countsLabel") }}</th>
						<th>{{ $t("config.feedinfinal.source") }}</th>
						<th></th>
					</tr>
				</thead>
				<tbody>
					<tr v-for="m in months" :key="m.month" :data-testid="`feedinfinal-${m.month}`">
						<td class="text-nowrap">{{ fmtMonthYear(monthDate(m.month)) }}</td>
						<td class="text-end text-nowrap">
							{{ m.market ? fmtMarket(m.market) : "–" }}
						</td>
						<td class="text-end text-nowrap">{{ fmtMarket(m.price) }}</td>
						<td class="text-end text-nowrap">
							{{
								m.at && !m.at.startsWith("0001")
									? $t("config.feedinfinal.counts", {
											slots: m.slots,
											sessions: m.sessions,
										})
									: "–"
							}}
						</td>
						<td class="text-nowrap">
							{{
								$t(
									m.manual
										? "config.feedinfinal.sourceManual"
										: "config.feedinfinal.sourceAuto"
								)
							}}
						</td>
						<td class="text-end">
							<button
								type="button"
								class="btn btn-link btn-sm p-0"
								@click="select(m.month)"
							>
								{{ $t("config.feedinfinal.edit") }}
							</button>
						</td>
					</tr>
				</tbody>
			</table>
		</div>

		<h6 class="mt-4">{{ $t("config.feedinfinal.recalcTitle") }}</h6>
		<p class="text-muted small">{{ $t("config.feedinfinal.recalcHelp") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>
		<p v-if="done" class="text-success" data-testid="feedinfinal-done">{{ done }}</p>

		<form class="container mx-0 px-0" @submit.prevent="recalculate">
			<FormRow id="feedInFinalMonth" :label="$t('config.feedinfinal.month')">
				<select
					id="feedInFinalMonth"
					v-model="month"
					class="form-select"
					data-testid="feedinfinal-month"
					@change="prefill"
				>
					<option v-for="o in monthOptions" :key="o" :value="o">
						{{ fmtMonthYear(monthDate(o)) }}
					</option>
				</select>
			</FormRow>
			<FormRow
				id="feedInFinalPrice"
				:label="$t('config.feedinfinal.priceLabel')"
				:help="$t('config.feedinfinal.priceHelp')"
			>
				<div class="input-group">
					<input
						id="feedInFinalPrice"
						v-model.number="priceCt"
						type="number"
						step="any"
						class="form-control"
						data-testid="feedinfinal-price"
					/>
					<span class="input-group-text">ct/kWh</span>
				</div>
			</FormRow>

			<div class="mt-4 d-flex justify-content-between gap-2 flex-column flex-sm-row">
				<button
					type="button"
					class="btn btn-link text-muted btn-cancel"
					data-bs-dismiss="modal"
				>
					{{ $t("config.general.close") }}
				</button>

				<button
					type="submit"
					class="btn btn-primary order-1 order-sm-2 flex-grow-1 flex-sm-grow-0 px-4"
					:disabled="saving"
					data-testid="feedinfinal-recalculate"
				>
					<span
						v-if="saving"
						class="spinner-border spinner-border-sm"
						role="status"
						aria-hidden="true"
					></span>
					{{ $t("config.feedinfinal.recalculate") }}
				</button>
			</div>
		</form>
	</GenericModal>
</template>

<script>
import GenericModal from "../Helper/GenericModal.vue";
import FormRow from "./FormRow.vue";
import store from "@/store";
import api from "@/api";
import formatter from "@/mixins/formatter";
import { fmtMarketPrice, monthDate, monthKey } from "./feedInFinal";

// months that can be recalculated, counting back from the previous one
const SELECTABLE_MONTHS = 12;

// Custom extension: the finalized OeMAG months and a recalculation by hand, see
// core/site_feedin.go. A month is recalculated at a market price: the one
// published, or a corrected one.
export default {
	name: "FeedInFinalModal",
	components: { FormRow, GenericModal },
	mixins: [formatter],
	data() {
		return {
			saving: false,
			error: "",
			done: "",
			month: "",
			priceCt: undefined,
		};
	},
	computed: {
		state() {
			return store.state?.feedInFinal;
		},
		months() {
			return this.state?.months || [];
		},
		finalizeDay() {
			return this.state?.finalizeDay ?? 15;
		},
		publishedText() {
			return this.state?.market
				? this.fmtMarket(this.state.market)
				: this.$t("config.feedinfinal.nonePublished");
		},
		monthOptions() {
			const now = new Date();
			return Array.from({ length: SELECTABLE_MONTHS }, (_, i) =>
				monthKey(new Date(now.getFullYear(), now.getMonth() - 1 - i, 1))
			);
		},
	},
	methods: {
		monthDate,
		fmtMarket(eur) {
			return fmtMarketPrice(eur, this.$i18n?.locale);
		},
		open() {
			this.saving = false;
			this.error = "";
			this.done = "";
			this.month = this.monthOptions[0];
			this.prefill();
		},
		select(month) {
			if (!this.monthOptions.includes(month)) return;
			this.month = month;
			this.done = "";
			this.prefill();
		},
		// the price the month was finalized at, or for the previous month the one
		// published now
		prefill() {
			const entry = this.months.find((m) => m.month === this.month);
			let eur = entry?.market;
			if (!eur && this.month === this.monthOptions[0]) {
				eur = this.state?.market;
			}
			this.priceCt = eur ? Math.round(eur * 100000) / 1000 : undefined;
		},
		async recalculate() {
			const ct = this.priceCt;
			if (typeof ct !== "number" || Number.isNaN(ct) || ct <= 0 || ct >= 100) {
				this.error = this.$t("config.feedinfinal.invalidPrice");
				return;
			}

			this.saving = true;
			this.error = "";
			this.done = "";

			try {
				const eur = Math.round(ct * 1000) / 100000;
				await api.post(`feedinfinalize/${this.month}/${eur}`);
				this.done = this.$t("config.feedinfinal.doneMessage", {
					month: this.fmtMonthYear(monthDate(this.month)),
				});
			} catch (e) {
				this.error = e?.response?.data?.error || e.message;
			}

			this.saving = false;
		},
	},
};
</script>
<style scoped>
.container {
	margin-left: calc(var(--bs-gutter-x) * -0.5);
	margin-right: calc(var(--bs-gutter-x) * -0.5);
	padding-right: 0;
}
</style>
