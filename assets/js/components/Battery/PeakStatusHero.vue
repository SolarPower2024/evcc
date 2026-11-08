<template>
	<div
		class="hero"
		:style="{ '--tone': tone }"
		:data-state="status.state"
		data-testid="peak-status-hero"
	>
		<div class="d-flex align-items-start gap-3">
			<span class="icon flex-shrink-0">
				<component :is="icon" size="m" />
			</span>
			<div class="min-w-0">
				<div class="title fw-bold" data-testid="peak-status-title">{{ title }}</div>
				<div class="text" data-testid="peak-status-text">{{ text }}</div>
			</div>
		</div>

		<div class="minis mt-3">
			<div
				v-for="m in minis"
				:key="m.key"
				class="mini"
				:data-testid="`peak-status-mini-${m.key}`"
			>
				<div class="mini-label">{{ m.label }}</div>
				<div class="mini-value fw-bold text-nowrap">{{ m.value }}</div>
				<div class="bar">
					<div class="bar-fill" :style="{ width: `${m.fill * 100}%` }"></div>
					<div
						v-if="m.mark !== undefined"
						class="bar-mark"
						:style="{ left: `${m.mark * 100}%` }"
					></div>
				</div>
			</div>
		</div>

		<div class="limits mt-2 small" data-testid="peak-status-limits">{{ limits }}</div>
	</div>
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/batterycharge";
import "@h2d2/shopicons/es/regular/batterydischarge";
import "@h2d2/shopicons/es/regular/batteryfull";
import "@h2d2/shopicons/es/regular/batteryquarter";
import "@h2d2/shopicons/es/regular/exclamationtriangle";
import "@h2d2/shopicons/es/regular/lightning";
import "@h2d2/shopicons/es/regular/lock";
import "@h2d2/shopicons/es/regular/powersupply";
import { defineComponent, type PropType } from "vue";
import formatter from "@/mixins/formatter";
import type { PeakState, PeakStatus } from "@/types/evcc-lm";
import { heroText, heroTitle, heroTone, limitsText, makeFmt, miniValues } from "./peakStatus";

// the tile at the top of the peak status dialog, see core/site_peak_status.go
const ICONS: Record<PeakState, string> = {
	off: "shopicon-regular-powersupply",
	storage: "shopicon-regular-exclamationtriangle",
	meter: "shopicon-regular-exclamationtriangle",
	gridCharge: "shopicon-regular-batterycharge",
	gridDischarge: "shopicon-regular-batterydischarge",
	holdShaving: "shopicon-regular-lightning",
	hold: "shopicon-regular-lock",
	shaving: "shopicon-regular-lightning",
	saving: "shopicon-regular-batteryquarter",
	free: "shopicon-regular-batteryfull",
};

export default defineComponent({
	name: "PeakStatusHero",
	mixins: [formatter],
	props: {
		status: { type: Object as PropType<PeakStatus>, required: true },
	},
	computed: {
		state(): PeakState {
			return this.status.state as PeakState;
		},
		tone(): string {
			return heroTone(this.state);
		},
		icon(): string {
			return ICONS[this.state];
		},
		fmt() {
			return makeFmt(
				(watt: number) => this.fmtW(watt),
				(percent: number) => this.fmtPercentage(percent)
			);
		},
		title(): string {
			return heroTitle(this.status, this.tr);
		},
		text(): string {
			return heroText(this.status, this.tr, this.fmt);
		},
		minis() {
			return miniValues(this.status, this.tr, this.fmt);
		},
		limits(): string {
			return limitsText(this.status, this.tr, this.fmt);
		},
	},
	methods: {
		tr(key: string, params?: Record<string, unknown>): string {
			return this.$t(key, params as Record<string, string>);
		},
	},
});
</script>

<style scoped>
.hero {
	--tone: var(--evcc-gray);
	background: color-mix(in srgb, var(--tone) 10%, var(--evcc-box));
	border: 1px solid color-mix(in srgb, var(--tone) 45%, transparent);
	border-radius: 1rem;
	padding: 1rem;
}
.min-w-0 {
	min-width: 0;
}
.icon {
	display: inline-flex;
	align-items: center;
	justify-content: center;
	width: 2.5rem;
	height: 2.5rem;
	border-radius: 50%;
	color: var(--tone);
	background: color-mix(in srgb, var(--tone) 18%, transparent);
}
.title {
	color: var(--tone);
	font-size: 1.1rem;
}
.text {
	overflow-wrap: anywhere;
}
.minis {
	display: grid;
	grid-template-columns: repeat(3, minmax(0, 1fr));
	gap: 0.75rem;
}
.mini-label {
	font-size: 0.75rem;
	text-transform: uppercase;
	color: var(--evcc-gray);
}
.mini-value {
	font-size: 1.05rem;
}
.bar {
	position: relative;
	height: 6px;
	margin-top: 0.3rem;
	background: var(--bs-border-color);
	border-radius: 3px;
	overflow: visible;
}
.bar-fill {
	height: 6px;
	border-radius: 3px;
	background: var(--tone);
}
.bar-mark {
	position: absolute;
	top: -3px;
	width: 2px;
	height: 12px;
	margin-left: -1px;
	background: var(--evcc-default-text);
}
.limits {
	color: var(--evcc-gray);
}
</style>
