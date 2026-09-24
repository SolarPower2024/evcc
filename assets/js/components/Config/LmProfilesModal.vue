<template>
	<GenericModal
		id="lmProfilesModal"
		ref="modal"
		size="lg"
		:title="editing ? editTitle : $t('config.lmprofiles.title')"
		data-testid="lmprofiles-modal"
		config-modal-name="lmprofiles"
		@open="open"
	>
		<p v-if="error" class="text-danger" data-testid="lmprofiles-error">{{ error }}</p>

		<!-- list -->
		<template v-if="!editing">
			<p>{{ $t("config.lmprofiles.description") }}</p>
			<p v-if="!profiles.length" class="text-muted">{{ $t("config.lmprofiles.none") }}</p>
			<div
				v-for="p in profiles"
				:key="p.id"
				class="d-flex align-items-center gap-2 py-2 border-bottom"
				:data-testid="`lmprofile-row-${p.id}`"
			>
				<ProfileIcon :icon="p.icon" />
				<strong class="flex-grow-1">{{ p.name }}</strong>
				<span class="small evcc-gray me-2">
					{{ $t("config.lmprofiles.valueCount", { count: valueCount(p) }) }}
				</span>
				<button type="button" class="btn btn-sm btn-outline-secondary" @click="edit(p)">
					{{ $t("config.lmprofiles.edit") }}
				</button>
			</div>
			<div class="mt-4 d-flex justify-content-between gap-2 flex-column flex-sm-row">
				<button
					type="button"
					class="btn btn-link text-muted btn-cancel"
					data-bs-dismiss="modal"
				>
					{{ $t("config.general.close") }}
				</button>
				<button
					type="button"
					class="btn btn-primary px-4"
					data-testid="lmprofile-new"
					:disabled="profiles.length >= maxProfiles"
					@click="edit(null)"
				>
					{{ $t("config.lmprofiles.new") }}
				</button>
			</div>
		</template>

		<!-- editor -->
		<form v-else class="container mx-0 px-0" @submit.prevent="save">
			<div class="row g-3 mb-2">
				<div class="col-sm-7">
					<label for="lmProfileName" class="form-label">{{
						$t("config.lmprofiles.name")
					}}</label>
					<input
						id="lmProfileName"
						v-model="form.name"
						type="text"
						maxlength="24"
						class="form-control"
						data-testid="lmprofile-name"
					/>
				</div>
				<div class="col-sm-5">
					<div class="form-label">{{ $t("config.lmprofiles.icon") }}</div>
					<div class="d-flex flex-wrap gap-1">
						<button
							v-for="icon in icons"
							:key="icon"
							type="button"
							class="btn btn-sm icon-btn"
							:class="form.icon === icon ? 'btn-primary' : 'btn-outline-secondary'"
							:aria-label="icon"
							:aria-pressed="form.icon === icon"
							@click="form.icon = icon"
						>
							<ProfileIcon :icon="icon" />
						</button>
					</div>
				</div>
			</div>

			<p class="small text-muted mb-2">{{ $t("config.lmprofiles.hint") }}</p>

			<section v-for="group in groups" :key="group.key" class="group">
				<h6 class="mt-3 mb-2">{{ $t(`config.lmprofiles.group.${group.key}`) }}</h6>
				<div
					v-for="f in group.fields"
					:key="f.key"
					class="field d-flex align-items-center gap-2"
				>
					<input
						:id="`lmProfileUse-${f.key}`"
						v-model="form.use[f.key]"
						class="form-check-input flex-shrink-0"
						type="checkbox"
						:data-testid="`lmprofile-use-${f.key}`"
					/>
					<label
						:for="`lmProfileUse-${f.key}`"
						class="flex-grow-1 mb-0"
						:class="{ 'evcc-gray': !form.use[f.key] }"
					>
						{{ f.label }}
					</label>
					<div class="value">
						<select
							v-if="f.type === 'bool'"
							v-model="form.values[f.key]"
							class="form-select form-select-sm"
							:disabled="!form.use[f.key]"
							:data-testid="`lmprofile-${f.key}`"
						>
							<option :value="true">{{ $t(`config.lmprofiles.${f.on}`) }}</option>
							<option :value="false">{{ $t(`config.lmprofiles.${f.off}`) }}</option>
						</select>
						<div v-else class="input-group input-group-sm">
							<input
								v-model.number="form.values[f.key]"
								type="number"
								step="any"
								class="form-control"
								:disabled="!form.use[f.key]"
								:placeholder="$t('config.lmprofiles.unchanged')"
								:data-testid="`lmprofile-${f.key}`"
							/>
							<span class="input-group-text">{{ f.unit }}</span>
						</div>
					</div>
				</div>
			</section>

			<div class="mt-4 d-flex flex-wrap justify-content-between gap-2">
				<div class="d-flex gap-2">
					<button type="button" class="btn btn-link text-muted px-0" @click="back">
						{{ $t("config.lmprofiles.back") }}
					</button>
					<button
						v-if="form.id"
						type="button"
						class="btn btn-link text-danger"
						data-testid="lmprofile-delete"
						@click="remove"
					>
						{{ $t("config.lmprofiles.delete") }}
					</button>
				</div>
				<div class="d-flex gap-2">
					<button
						type="button"
						class="btn btn-outline-secondary"
						data-testid="lmprofile-current"
						@click="takeCurrent"
					>
						{{ $t("config.lmprofiles.takeCurrent") }}
					</button>
					<button
						type="submit"
						class="btn btn-primary px-4"
						:disabled="saving"
						data-testid="lmprofile-save"
					>
						<span
							v-if="saving"
							class="spinner-border spinner-border-sm"
							role="status"
							aria-hidden="true"
						></span>
						{{ $t("config.general.save") }}
					</button>
				</div>
			</div>
		</form>
	</GenericModal>
</template>

<script>
import GenericModal from "../Helper/GenericModal.vue";
import ProfileIcon, { PROFILE_ICONS } from "../Battery/ProfileIcon.vue";
import store from "@/store";
import api from "@/api";

const MAX_PROFILES = 10;

// Custom extension: battery profiles, see core/site_lm_profiles.go. Every value
// has a checkbox: values not ticked are left as they are when the profile is
// applied. The inputs have no min, max or step, the server checks the ranges and
// the dialog shows its message.
export default {
	name: "LmProfilesModal",
	components: { GenericModal, ProfileIcon },
	data() {
		return {
			editing: false,
			saving: false,
			error: "",
			form: { id: "", name: "", icon: "sun", use: {}, values: {} },
		};
	},
	computed: {
		profiles() {
			return store.state?.lmProfiles || [];
		},
		wallboxes() {
			return store.state?.lmProfileWallboxes || [];
		},
		maxProfiles() {
			return MAX_PROFILES;
		},
		icons() {
			return Object.keys(PROFILE_ICONS);
		},
		editTitle() {
			return this.form.id ? this.form.name : this.$t("config.lmprofiles.new");
		},
		groups() {
			const t = (key) => this.$t(`config.lmprofiles.field.${key}`);
			return [
				{
					key: "gridCharge",
					fields: [
						{
							key: "gridCharge",
							label: t("gridCharge"),
							type: "bool",
							on: "on",
							off: "off",
						},
						{ key: "gridChargeStart", label: t("gridChargeStart"), unit: "%" },
						{ key: "gridChargeStop", label: t("gridChargeStop"), unit: "%" },
					],
				},
				{
					key: "battery",
					fields: [
						{ key: "prioritySoc", label: t("prioritySoc"), unit: "%" },
						{ key: "bufferSoc", label: t("bufferSoc"), unit: "%" },
						{ key: "bufferStartSoc", label: t("bufferStartSoc"), unit: "%" },
						{
							key: "dischargeControl",
							label: t("dischargeControl"),
							type: "bool",
							on: "yes",
							off: "no",
						},
					],
				},
				{
					key: "peakShaving",
					fields: [
						{
							key: "peakShaving",
							label: t("peakShaving"),
							type: "bool",
							on: "active",
							off: "inactive",
						},
						{ key: "peakReserve", label: t("peakReserve"), unit: "%" },
						{ key: "peakLimit", label: t("peakLimit"), unit: "kW" },
					],
				},
				...(this.wallboxes.length
					? [
							{
								key: "wallbox",
								fields: this.wallboxes.map((w) => ({
									key: `solarShare:${w.name}`,
									label: this.$t("config.lmprofiles.field.solarShare", {
										title: w.title || w.name,
									}),
									unit: "%",
								})),
							},
						]
					: []),
			];
		},
		fieldKeys() {
			return this.groups.flatMap((g) => g.fields.map((f) => f.key));
		},
	},
	methods: {
		open() {
			this.editing = false;
			this.error = "";
		},
		valueCount(p) {
			const keys = [
				"gridCharge",
				"gridChargeStart",
				"gridChargeStop",
				"prioritySoc",
				"bufferSoc",
				"bufferStartSoc",
				"dischargeControl",
				"peakShaving",
				"peakReserve",
				"peakLimit",
			];
			return (
				keys.filter((k) => p[k] !== undefined && p[k] !== null).length +
				Object.keys(p.solarShare || {}).length
			);
		},
		// profile → form: kW for the peak limit, one entry per wallbox
		edit(p) {
			const use = {};
			const values = {};
			this.fieldKeys.forEach((key) => {
				let v;
				if (key.startsWith("solarShare:")) {
					v = p?.solarShare?.[key.slice("solarShare:".length)];
				} else {
					v = p?.[key];
				}
				if (key === "peakLimit" && typeof v === "number") v = v / 1000;
				use[key] = v !== undefined && v !== null;
				values[key] =
					v ??
					(["gridCharge", "dischargeControl", "peakShaving"].includes(key)
						? true
						: undefined);
			});

			this.form = {
				id: p?.id || "",
				name: p?.name || "",
				icon: p?.icon || "sun",
				use,
				values,
			};
			this.error = "";
			this.editing = true;
		},
		back() {
			this.editing = false;
			this.error = "";
		},
		// fills every field with what is set right now and ticks it
		takeCurrent() {
			const s = store.state || {};
			const current = {
				gridCharge: !!s.batterySocGridCharge,
				gridChargeStart: s.batterySocGridChargeStart,
				gridChargeStop: s.batterySocGridChargeStop,
				prioritySoc: s.prioritySoc,
				bufferSoc: s.bufferSoc,
				bufferStartSoc: s.bufferStartSoc,
				dischargeControl: !!s.batteryDischargeControl,
				peakShaving: !!s.peakShaving,
				peakReserve: s.peakShavingReserve,
				peakLimit:
					typeof s.peakShavingLimit === "number" ? s.peakShavingLimit / 1000 : undefined,
			};
			this.wallboxes.forEach((w) => (current[`solarShare:${w.name}`] = w.solarShare));

			Object.entries(current).forEach(([key, v]) => {
				if (v === undefined || v === null) return;
				this.form.values[key] = v;
				this.form.use[key] = true;
			});
		},
		// form → profile: only ticked values, W for the peak limit
		profile() {
			const p = { id: this.form.id, name: this.form.name, icon: this.form.icon };
			const solarShare = {};

			for (const key of this.fieldKeys) {
				if (!this.form.use[key]) continue;
				const v = this.form.values[key];
				if (v === undefined || v === "" || (typeof v === "number" && Number.isNaN(v))) {
					throw new Error(this.$t("config.lmprofiles.missingValue"));
				}
				if (key.startsWith("solarShare:")) {
					solarShare[key.slice("solarShare:".length)] = v;
				} else if (key === "peakLimit") {
					p.peakLimit = Math.round(v * 1000);
				} else {
					p[key] = v;
				}
			}

			if (Object.keys(solarShare).length) p.solarShare = solarShare;
			return p;
		},
		async save() {
			this.error = "";
			let p;
			try {
				p = this.profile();
			} catch (e) {
				this.error = e.message;
				return;
			}

			this.saving = true;
			try {
				await api.post("lmprofile", p);
				this.editing = false;
			} catch (e) {
				this.error = e?.response?.data?.error || e.message;
			}
			this.saving = false;
		},
		async remove() {
			if (
				!window.confirm(
					this.$t("config.lmprofiles.confirmDelete", { name: this.form.name })
				)
			) {
				return;
			}
			try {
				await api.delete(`lmprofile/${encodeURIComponent(this.form.id)}`);
				this.editing = false;
			} catch (e) {
				this.error = e?.response?.data?.error || e.message;
			}
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
.group {
	border-top: 1px solid var(--bs-border-color);
}
.field {
	padding: 0.3rem 0;
}
.value {
	width: 9rem;
	flex-shrink: 0;
}
.icon-btn {
	padding: 0.25rem 0.45rem;
}
</style>
