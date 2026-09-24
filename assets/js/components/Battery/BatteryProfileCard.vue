<template>
	<Card :title="$t('batterySettings.profiles.title')" data-testid="battery-profiles">
		<div class="d-flex flex-wrap gap-2">
			<button
				v-for="p in profiles"
				:key="p.id"
				type="button"
				class="btn btn-sm profile d-flex align-items-center gap-2"
				:class="p.id === active ? 'btn-primary' : 'btn-outline-secondary'"
				:disabled="applying"
				:aria-pressed="p.id === active"
				:data-testid="`battery-profile-${p.id}`"
				@click="apply(p)"
			>
				<ProfileIcon :icon="p.icon" />
				{{ p.name }}
			</button>
		</div>
		<p v-if="error" class="text-danger small mt-3 mb-0" data-testid="battery-profile-error">
			{{ error }}
		</p>
	</Card>
</template>

<script>
import Card from "../Helper/Card.vue";
import ProfileIcon from "./ProfileIcon.vue";
import store from "@/store";
import api from "@/api";

// Custom extension: pick a battery profile, see core/site_lm_profiles.go. The
// profiles are set up under Lastmanagement-Details → Profile.
export default {
	name: "BatteryProfileCard",
	components: { Card, ProfileIcon },
	data() {
		return { applying: false, error: "" };
	},
	computed: {
		profiles() {
			return store.state?.lmProfiles || [];
		},
		active() {
			return store.state?.lmProfileActive;
		},
	},
	methods: {
		async apply(p) {
			this.applying = true;
			this.error = "";
			try {
				await api.post(`lmprofile/${encodeURIComponent(p.id)}/apply`);
			} catch (e) {
				this.error = this.$t("batterySettings.profiles.failed", {
					name: p.name,
					error: e?.response?.data?.error || e.message,
				});
			}
			this.applying = false;
		},
	},
};
</script>

<style scoped>
.profile {
	border-radius: 2rem;
	padding: 0.4rem 1rem;
}
</style>
