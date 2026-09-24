<template>
	<component :is="component" size="s" />
</template>

<script>
import "@h2d2/shopicons/es/regular/sun";
import "@h2d2/shopicons/es/regular/cloudsun";
import "@h2d2/shopicons/es/regular/snow";
import "@h2d2/shopicons/es/regular/umbrella";
import "@h2d2/shopicons/es/regular/home";
import "@h2d2/shopicons/es/regular/eco1";
import "@h2d2/shopicons/es/regular/moonstars";
import "@h2d2/shopicons/es/regular/flower1";

// Custom extension: the icons a battery profile can show, see core/lm/profile
export const PROFILE_ICONS = {
	sun: "shopicon-regular-sun",
	cloudsun: "shopicon-regular-cloudsun",
	snow: "shopicon-regular-snow",
	umbrella: "shopicon-regular-umbrella",
	home: "shopicon-regular-home",
	eco: "shopicon-regular-eco1",
	moon: "shopicon-regular-moonstars",
	flower: "shopicon-regular-flower1",
};

export default {
	name: "ProfileIcon",
	props: { icon: String },
	computed: {
		component() {
			return PROFILE_ICONS[this.icon] || PROFILE_ICONS.home;
		},
	},
};
</script>
