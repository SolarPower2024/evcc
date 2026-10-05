<template>
	<button
		type="button"
		class="btn flex-grow-0 d-flex align-items-center gap-2 text-nowrap"
		:class="
			state?.error
				? 'btn-outline-danger'
				: state?.enabled
					? 'btn-secondary'
					: 'btn-outline-secondary'
		"
		:aria-label="$t('logFile.title')"
		data-testid="logfile-button"
		@click="open"
	>
		<shopicon-regular-note size="s" class="icon flex-shrink-0"></shopicon-regular-note>
		<span class="d-none d-xl-inline" data-testid="logfile-status">{{ status }}</span>
	</button>
	<LogFileModal ref="modal" @changed="load" />
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/note";
import { defineComponent } from "vue";
import Modal from "bootstrap/js/dist/modal";
import api from "@/api";
import type { LogFileState } from "@/types/evcc-lm";
import LogFileModal from "./LogFileModal.vue";

// Custom extension: opens the log file dialog from the log page, see
// util/logstash/file_custom.go. The button shows whether the file is written.
export default defineComponent({
	name: "LogFileButton",
	components: { LogFileModal },
	data() {
		return { state: null as LogFileState | null };
	},
	computed: {
		status() {
			if (this.state?.error) return this.$t("logFile.failed");
			return this.state?.enabled ? this.state.level : this.$t("logFile.off");
		},
	},
	mounted() {
		this.load();
	},
	methods: {
		async load() {
			try {
				this.state = (await api.get("logfile")).data;
			} catch (e) {
				console.error(e);
			}
		},
		open() {
			const el = document.getElementById("logFileModal") as HTMLElement;
			Modal.getOrCreateInstance(el).show();
		},
	},
});
</script>
