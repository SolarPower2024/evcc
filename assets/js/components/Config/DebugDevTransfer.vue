<template>
	<div v-if="status.enabled || status.applied" class="mb-4" data-testid="debug-devTransfer">
		<template v-if="status.enabled">
			<h6>{{ $t("config.lmdebug.devTransfer.title") }}</h6>
			<p class="mb-2">{{ $t("config.lmdebug.devTransfer.description") }}</p>
			<p v-if="status.notFound" class="text-danger" data-testid="debug-devtransfer-notfound">
				{{ $t("config.lmdebug.devTransfer.notFound") }}
			</p>
			<p
				v-else-if="!status.since"
				class="text-warning"
				data-testid="debug-devtransfer-nottaken"
			>
				{{ $t("config.lmdebug.devTransfer.notTaken") }}
			</p>
			<p v-if="error" class="text-danger" data-testid="debug-devtransfer-error">
				{{ error }}
			</p>
			<p v-if="done" class="text-success" data-testid="debug-devtransfer-done">
				{{ $t("config.lmdebug.done") }}
			</p>

			<div class="d-flex flex-column gap-3">
				<div>
					<button
						v-if="confirming !== 'takeover'"
						type="button"
						class="btn btn-outline-danger"
						:disabled="busy || !takeoverPossible"
						data-testid="debug-devtransfer-takeover"
						@click="ask('takeover')"
					>
						{{ $t("config.lmdebug.devTransfer.takeover") }}
					</button>
					<form
						v-else
						class="border rounded p-3"
						data-testid="debug-devtransfer-takeover-form"
						@submit.prevent="submit"
					>
						<p class="fw-bold" data-testid="debug-devtransfer-takeover-preview">
							{{
								$t("config.lmdebug.devTransfer.takeoverPreview", {
									date: fmtDate(status.liveChanged),
								})
							}}
						</p>
						<p data-testid="debug-devtransfer-restart">
							{{ $t(`config.lmdebug.devTransfer.restart_${status.restart}`) }}
						</p>
						<PasswordInput
							v-if="!authDisabled"
							v-model:password="password"
							:error="passwordError"
							:iframe-hint="iframeHint"
						/>
						<p class="mb-3">{{ $t("config.lmdebug.confirm") }}</p>
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
								data-testid="debug-devtransfer-takeover-confirm"
							>
								{{ $t("config.lmdebug.devTransfer.takeover") }}
							</button>
						</div>
					</form>
				</div>

				<div>
					<div class="mb-2" data-testid="debug-devtransfer-items">
						<div v-for="item in allItems" :key="item" class="form-check">
							<input
								:id="`debug-devtransfer-item-${item}`"
								v-model="selected"
								class="form-check-input"
								type="checkbox"
								:value="item"
								:disabled="busy || confirming === 'giveback'"
								:data-testid="`debug-devtransfer-item-${item}`"
								@change="cancel"
							/>
							<label class="form-check-label" :for="`debug-devtransfer-item-${item}`">
								{{ $t(`config.lmdebug.devTransfer.${item}`) }}
							</label>
						</div>
					</div>
					<button
						v-if="confirming !== 'giveback'"
						type="button"
						class="btn btn-outline-danger"
						:disabled="busy || !giveBackPossible"
						data-testid="debug-devtransfer-giveback"
						@click="ask('giveback')"
					>
						{{ $t("config.lmdebug.devTransfer.return") }}
					</button>
					<form
						v-else
						class="border rounded p-3"
						data-testid="debug-devtransfer-giveback-form"
						@submit.prevent="submit"
					>
						<PasswordInput
							v-if="!authDisabled"
							v-model:password="password"
							:error="passwordError"
							:iframe-hint="iframeHint"
						/>
						<template v-if="summary">
							<p class="fw-bold" data-testid="debug-devtransfer-giveback-preview">
								{{
									$t("config.lmdebug.devTransfer.returnPreview", {
										from: fmtDate(summary.since),
										to: fmtDate(summary.to),
										sessions: summary.sessions,
										slots: summary.slots,
										months: summary.months,
									})
								}}
							</p>
							<p
								v-if="warnOnlyOne"
								class="text-warning"
								data-testid="debug-devtransfer-onlyone"
							>
								{{ $t("config.lmdebug.devTransfer.onlyOne") }}
							</p>
							<p class="mb-3">{{ $t("config.lmdebug.confirm") }}</p>
						</template>
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
								data-testid="debug-devtransfer-giveback-confirm"
							>
								{{
									summary
										? $t("config.lmdebug.devTransfer.return")
										: $t("config.lmdebug.feedIn.check")
								}}
							</button>
						</div>
					</form>
				</div>
			</div>
		</template>

		<p
			v-if="status.applied"
			:class="{ 'mt-3': status.enabled, 'mb-0': true }"
			data-testid="debug-devtransfer-applied"
		>
			{{
				$t("config.lmdebug.devTransfer.applied", {
					date: fmtDate(status.applied.at),
					items: status.applied.items.map((i) => itemName(i)).join(", "),
				})
			}}
		</p>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import PasswordInput from "../Auth/PasswordInput.vue";
import formatter from "@/mixins/formatter";
import store from "@/store";
import api from "@/api";
import { isLoggedIn } from "../Auth/auth";
import type { DevTransferItem, DevTransferStatus, DevTransferSummary } from "@/types/evcc-lm";
import { feedInAnswer, loginMissing } from "@/utils/debugFeedIn";
import {
	DEV_TRANSFER_DEFAULT,
	DEV_TRANSFER_ITEMS,
	canGiveBack,
	canTakeover,
	itemsQuery,
	onlyOne,
} from "@/utils/debugDevTransfer";

// 200 and the errors answered here, so the global handler neither opens the login
// nor raises a message for a wrong password
const validateStatus = (code: number) => [200, 204, 400, 401, 403, 409].includes(code);

// Custom extension: the test version's exchange with evcc custom, under
// Konfiguration → System → Debug, see core/devtransfer and core/lm/README.md.
// On evcc custom only the line of the last give back shows.
export default defineComponent({
	name: "DebugDevTransfer",
	components: { PasswordInput },
	mixins: [formatter],
	data() {
		return {
			status: { enabled: false } as DevTransferStatus,
			allItems: DEV_TRANSFER_ITEMS,
			selected: [...DEV_TRANSFER_DEFAULT] as DevTransferItem[],
			confirming: "" as "takeover" | "giveback" | "",
			summary: null as DevTransferSummary | null,
			busy: false,
			done: false,
			error: "",
			passwordError: "",
			iframeHint: false,
			password: "",
		};
	},
	computed: {
		authDisabled(): boolean {
			return store.state?.authDisabled || false;
		},
		takeoverPossible(): boolean {
			return canTakeover(this.status);
		},
		giveBackPossible(): boolean {
			return canGiveBack(this.status, this.selected);
		},
		warnOnlyOne(): boolean {
			return onlyOne(this.selected);
		},
	},
	methods: {
		async load() {
			this.reset();
			this.done = false;
			try {
				this.status = (await api.get("devtransfer")).data;
			} catch (e) {
				console.error(e);
				this.status = { enabled: false };
			}
		},
		reset() {
			this.confirming = "";
			this.summary = null;
			this.busy = false;
			this.error = "";
			this.passwordError = "";
			this.iframeHint = false;
			this.password = "";
		},
		cancel() {
			this.reset();
		},
		// the short name of an item for the line of the last give back
		itemName(item: DevTransferItem): string {
			const short = item === "devices" || item === "settings";
			return this.$t(`config.lmdebug.devTransfer.${short ? `${item}Name` : item}`);
		},
		fmtDate(iso?: string): string {
			if (!iso) return "";
			const d = new Date(iso);
			return `${this.fmtDayMonth(d)} ${this.fmtHourMinute(d)}`;
		},
		ask(what: "takeover" | "giveback") {
			this.reset();
			this.done = false;
			this.confirming = what;

			// the preview of the give back needs the call, without a password there is nothing to ask first
			if (what === "giveback" && this.authDisabled) this.submit();
		},
		async submit() {
			if (loginMissing(isLoggedIn(), this.authDisabled)) {
				this.iframeHint = true;
				return;
			}

			this.busy = true;
			this.error = "";
			this.passwordError = "";

			try {
				const headers = { "X-Admin-Password": this.password };
				const params = { items: itemsQuery(this.selected) };
				const giveBack = this.confirming === "giveback";
				const confirmed = !giveBack || !!this.summary;

				const res = await api.request({
					method: confirmed ? "post" : "get",
					url: giveBack ? "/db/devtransfer/giveback" : "/db/devtransfer/takeover",
					params: giveBack ? params : undefined,
					headers,
					validateStatus,
				});

				const answer = feedInAnswer(res.status);
				if (answer === "password") {
					this.passwordError = this.$t("loginModal.invalid");
				} else if (answer === "error") {
					this.error = res.data?.error || "";
				} else if (!confirmed) {
					this.summary = res.data;
				} else {
					this.reset();
					this.done = true;
					await this.load2();
				}
			} catch (e) {
				const err = e as { response?: { data?: { error?: string } }; message?: string };
				this.error = err?.response?.data?.error || err?.message || "";
			}

			this.busy = false;
		},
		// refresh the state without clearing the answer
		async load2() {
			try {
				this.status = (await api.get("devtransfer")).data;
			} catch (e) {
				console.error(e);
			}
		},
	},
});
</script>
