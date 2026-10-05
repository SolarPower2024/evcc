<template>
	<div v-if="note">
		<div class="form-check form-switch mt-2 mb-0 d-md-none">
			<input
				id="snowCoverRow"
				:checked="snowCover"
				class="form-check-input"
				type="checkbox"
				role="switch"
				data-testid="snowcover-switch-row"
				@change="change"
			/>
			<label class="form-check-label text-muted" for="snowCoverRow">
				{{ $t("snowCover.label") }}
			</label>
		</div>
		<div v-if="autoAvailable" class="mt-2">
			<div class="form-check form-switch mb-0">
				<input
					id="snowAuto"
					:checked="snowAuto"
					class="form-check-input"
					type="checkbox"
					role="switch"
					data-testid="snowauto-switch"
					@change="changeAuto"
				/>
				<label class="form-check-label text-muted" for="snowAuto">
					{{ $t("snowCover.autoLabel") }}
				</label>
			</div>
			<div class="small text-muted" data-testid="snowauto-help">
				{{ $t("snowCover.autoHelp") }}
			</div>
		</div>
		<div v-if="snowCover" class="small text-muted mt-2" data-testid="snowcover-note">
			{{ $t(snowCoverAuto ? "snowCover.noteAuto" : "snowCover.note") }}
		</div>
	</div>
	<div v-else class="form-check form-switch mb-0 text-nowrap d-none d-md-block">
		<input
			id="snowCover"
			:checked="snowCover"
			class="form-check-input"
			type="checkbox"
			role="switch"
			data-testid="snowcover-switch"
			@change="change"
		/>
		<label class="form-check-label text-muted" for="snowCover">
			{{ $t("snowCover.label") }}
		</label>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import api from "@/api";
import store from "@/store";

// Custom extension: switch "snow on pv" on the forecast page, see core/site_snow.go,
// and the switch to detect it automatically under the chart, see core/site_snow_auto.go.
// In the card header from md up; below that it sits with the hint under the chart,
// as the header has no room for it next to the title and evcc's switch.
export default defineComponent({
	name: "SnowCoverSwitch",
	props: {
		note: Boolean,
	},
	computed: {
		snowCover() {
			return !!store.state?.snowCover;
		},
		// turned on by the detection from the weather
		snowCoverAuto() {
			return !!store.state?.snowCoverAuto;
		},
		snowAuto() {
			return !!store.state?.snowAuto;
		},
		// the detection needs the location of an Open-Meteo solar forecast
		autoAvailable() {
			return !!store.state?.snowAutoAvailable;
		},
	},
	methods: {
		async change(e: Event) {
			const input = e.target as HTMLInputElement;
			try {
				await api.post(`snowcover/${input.checked ? "true" : "false"}`);
			} catch (err) {
				// back to the state evcc has, the store did not change
				input.checked = this.snowCover;
				console.error(err);
			}
		},
		async changeAuto(e: Event) {
			const input = e.target as HTMLInputElement;
			try {
				await api.post(`snowauto/${input.checked ? "true" : "false"}`);
			} catch (err) {
				input.checked = this.snowAuto;
				console.error(err);
			}
		},
	},
});
</script>
