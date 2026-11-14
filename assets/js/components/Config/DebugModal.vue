<template>
	<GenericModal
		id="debugModal"
		ref="modal"
		:title="$t('config.lmdebug.title')"
		data-testid="debug-modal"
		config-modal-name="debug"
		@open="open"
	>
		<p>{{ $t("config.lmdebug.description") }}</p>
		<p v-if="error" class="text-danger" data-testid="debug-error">{{ error }}</p>

		<div v-for="s in sections" :key="s.name" class="mb-4" :data-testid="`debug-${s.name}`">
			<h6>{{ $t(`config.lmdebug.${s.name}.title`) }}</h6>
			<p class="mb-2">{{ s.description }}</p>

			<div v-if="s.name === 'feedIn'" class="d-flex gap-2 mb-2">
				<div class="flex-fill min-w-0">
					<label class="form-label mb-1" for="debugFeedInFrom">
						{{ $t("config.lmdebug.feedIn.from") }}
					</label>
					<input
						id="debugFeedInFrom"
						v-model="from"
						type="date"
						class="form-control"
						:max="today"
						data-testid="debug-feedin-from"
						@change="rangeChanged"
					/>
				</div>
				<div class="flex-fill min-w-0">
					<label class="form-label mb-1" for="debugFeedInTo">
						{{ $t("config.lmdebug.feedIn.to") }}
					</label>
					<input
						id="debugFeedInTo"
						v-model="to"
						type="date"
						class="form-control"
						:min="from"
						:max="today"
						data-testid="debug-feedin-to"
						@change="rangeChanged"
					/>
				</div>
			</div>

			<p
				v-if="done === s.name"
				class="text-success mb-0"
				:data-testid="`debug-${s.name}-done`"
			>
				{{ $t("config.lmdebug.done") }}
			</p>

			<button
				v-else-if="confirming !== s.name"
				type="button"
				class="btn btn-outline-danger"
				:disabled="busy || !s.enabled"
				:data-testid="`debug-${s.name}-action`"
				@click="ask(s.name)"
			>
				{{ $t(`config.lmdebug.${s.name}.action`) }}
			</button>

			<form v-else class="border rounded p-3" @submit.prevent="submit(s.name)">
				<template v-if="s.name === 'feedIn'">
					<PasswordInput
						v-if="!authDisabled"
						v-model:password="password"
						:error="passwordError"
						:iframe-hint="iframeHint"
					/>
					<p v-if="preview" class="fw-bold" data-testid="debug-feedin-preview">
						{{
							$t("config.lmdebug.feedIn.preview", {
								energy: fmtKWh(preview.energy),
								price: fmtMoneyWithSymbol(preview.revenue),
							})
						}}
					</p>
				</template>
				<p v-if="s.name !== 'feedIn' || preview" class="mb-3">
					{{ $t("config.lmdebug.confirm") }}
				</p>

				<div class="d-flex justify-content-between gap-2 flex-wrap">
					<button
						type="button"
						class="btn btn-outline-secondary"
						:disabled="busy"
						@click="cancel"
					>
						{{ $t("config.general.cancel") }}
					</button>
					<button
						type="submit"
						class="btn btn-danger"
						:disabled="busy"
						:data-testid="`debug-${s.name}-confirm`"
					>
						<span
							v-if="busy"
							class="spinner-border spinner-border-sm"
							role="status"
							aria-hidden="true"
						></span>
						{{
							s.name === "feedIn" && !preview
								? $t("config.lmdebug.feedIn.check")
								: $t(`config.lmdebug.${s.name}.action`)
						}}
					</button>
				</div>
			</form>
		</div>
	</GenericModal>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import GenericModal from "../Helper/GenericModal.vue";
import PasswordInput from "../Auth/PasswordInput.vue";
import formatter from "@/mixins/formatter";
import store from "@/store";
import api from "@/api";
import { isLoggedIn } from "../Auth/auth";
import type { FeedInSummary } from "@/types/evcc-lm";

type Section = "peakReset" | "feedIn" | "events" | "guard";

// 200 and the errors answered here, so the global handler neither opens the login
// nor raises a message for a wrong password
const validateStatus = (code: number) => [200, 204, 400, 401, 403].includes(code);

const pad = (n: number) => String(n).padStart(2, "0");
const dayString = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;

// Local midnight of a YYYY-MM-DD day as the RFC 3339 time the api takes. The
// days are in local time, the end day is included: the range ends at the
// midnight after it.
const dayStart = (day: string, plus = 0) => {
	const [y, m, d] = day.split("-").map(Number) as [number, number, number];
	return new Date(y, m - 1, d + plus).toISOString();
};

// Custom extension: corrections for faulty readings and tests, under
// Konfiguration → System → Debug, see core/lm/README.md. Every function asks
// before it runs; the feed-in cannot be undone and asks for the admin password
// as the backup dialog does (api/db).
export default defineComponent({
	name: "DebugModal",
	components: { GenericModal, PasswordInput },
	mixins: [formatter],
	data() {
		return {
			today: dayString(new Date()),
			from: dayString(new Date()),
			to: dayString(new Date()),
			confirming: "" as Section | "",
			done: "" as Section | "",
			busy: false,
			error: "",
			passwordError: "",
			iframeHint: false,
			password: "",
			preview: null as FeedInSummary | null,
			guardCount: 0,
		};
	},
	computed: {
		authDisabled(): boolean {
			return store.state?.authDisabled || false;
		},
		currency() {
			return store.state?.currency;
		},
		limit(): string {
			return this.fmtW(store.state?.gridPeakBaseline ?? 0);
		},
		rangeValid(): boolean {
			return !!this.from && !!this.to && this.from <= this.to && this.to <= this.today;
		},
		sections(): { name: Section; description: string; enabled: boolean }[] {
			const t = (key: string, p: Record<string, unknown> = {}) =>
				this.$t(`config.lmdebug.${key}.description`, p);
			return [
				{
					name: "peakReset",
					description: t("peakReset", { limit: this.limit }),
					enabled: true,
				},
				{ name: "feedIn", description: t("feedIn"), enabled: this.rangeValid },
				{ name: "events", description: t("events"), enabled: true },
				{
					name: "guard",
					description: t("guard", { count: this.guardCount }),
					enabled: this.guardCount > 0,
				},
			];
		},
	},
	methods: {
		fmtMoneyWithSymbol(amount: number): string {
			return `${this.fmtMoney(amount, this.currency)} ${this.fmtCurrencySymbol(this.currency)}`;
		},
		open() {
			this.today = dayString(new Date());
			this.from = this.to = this.today;
			this.reset();
			this.done = "";
			this.loadGuardCount();
		},
		reset() {
			this.confirming = "";
			this.busy = false;
			this.error = "";
			this.passwordError = "";
			this.iframeHint = false;
			this.password = "";
			this.preview = null;
		},
		async loadGuardCount() {
			try {
				this.guardCount = (await api.get("lmguardcount")).data;
			} catch (e) {
				console.error(e);
				this.guardCount = 0;
			}
		},
		rangeChanged() {
			// another range, another preview
			if (this.confirming === "feedIn") this.reset();
		},
		ask(name: Section) {
			this.reset();
			this.done = "";
			this.confirming = name;

			// without a password there is nothing to ask first
			if (name === "feedIn" && this.authDisabled) this.submit(name);
		},
		cancel() {
			this.reset();
		},
		async submit(name: Section) {
			this.busy = true;
			this.error = "";

			try {
				if (name === "feedIn") {
					await this.feedIn();
				} else {
					await this.run(name);
				}
			} catch (e) {
				const err = e as { response?: { data?: { error?: string } }; message?: string };
				this.error = err?.response?.data?.error || err?.message || "";
			}

			this.busy = false;
		},
		async run(name: Exclude<Section, "feedIn">) {
			if (name === "peakReset") {
				await api.post("peakreset");
			} else if (name === "events") {
				await api.delete("lmevents");
			} else {
				await api.post("lmguardrelease");
				await this.loadGuardCount();
			}

			this.finish(name);
		},
		// the preview first, the second submit sets the feed-in to 0
		async feedIn() {
			const params = { from: dayStart(this.from), to: dayStart(this.to, 1) };
			const headers = { "X-Admin-Password": this.password };
			const config = { params, headers, validateStatus };

			const res = this.preview
				? await api.delete("/db/metrics/feedin", config)
				: await api.get("/db/metrics/feedin", config);

			if (res.status === 401 || res.status === 403) {
				this.passwordError = this.$t("loginModal.invalid");
				return;
			}
			if (res.status === 400) {
				this.error = res.data?.error || "";
				return;
			}
			if (!isLoggedIn() && !this.authDisabled) {
				this.iframeHint = true;
				return;
			}

			if (this.preview) {
				this.finish("feedIn");
			} else {
				this.preview = res.data;
			}
		},
		finish(name: Section) {
			this.reset();
			this.done = name;
		},
	},
});
</script>
<style scoped>
/* two date fields side by side must be able to shrink below their natural width on a phone */
.min-w-0 {
	min-width: 0;
}
</style>
