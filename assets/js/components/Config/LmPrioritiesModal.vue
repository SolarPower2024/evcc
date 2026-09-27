<template>
	<GenericModal
		id="lmPrioritiesModal"
		ref="modal"
		:title="$t('config.lmpriorities.title')"
		data-testid="lmpriorities-modal"
		config-modal-name="lmpriorities"
		@open="open"
	>
		<p>{{ $t("config.lmpriorities.description") }}</p>
		<p v-if="error" class="text-danger">{{ error }}</p>
		<p v-else-if="!loads.length" class="text-muted">
			{{ $t("config.lmpriorities.noLoads") }}
		</p>

		<form ref="form" class="container mx-0 px-0" @submit.prevent="save">
			<!-- custom: order by drag, top = highest, see utils/lmPriorityOrder.ts -->
			<DragDropList
				v-if="loads.length"
				:key="listKey"
				:values="order"
				@pointerdown.capture="dragStart"
				@touchstart.capture="dragStart"
				@reorder="reorder"
			>
				<DragDropItem
					v-for="name in order"
					:key="name"
					:title="label(name)"
					:data-testid="`lmpriority-${name}`"
				>
					{{ label(name) }}
					<template #actions>
						<span class="evcc-gray small text-nowrap" data-testid="lmpriority-value">
							{{ $t("config.lmpriorities.value", { priority: values[name] }) }}
						</span>
					</template>
				</DragDropItem>
			</DragDropList>
			<p v-if="loads.length > 1" class="small evcc-gray mb-0">
				{{ $t("config.lmpriorities.orderHint") }}
			</p>

			<div class="mt-4 d-flex justify-content-between gap-2 flex-column flex-sm-row">
				<button
					type="button"
					class="btn btn-link text-muted btn-cancel"
					data-bs-dismiss="modal"
				>
					{{ $t("config.general.cancel") }}
				</button>

				<button
					type="submit"
					class="btn btn-primary order-1 order-sm-2 flex-grow-1 flex-sm-grow-0 px-4"
					:disabled="saving || !changed.length"
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
		</form>
	</GenericModal>
</template>

<script>
import GenericModal from "../Helper/GenericModal.vue";
import DragDropList from "@/components/Helper/DragDropList.vue";
import DragDropItem from "@/components/Helper/DragDropItem.vue";
import store from "@/store";
import api from "@/api";
import { movedName, orderPriorities } from "@/utils/lmPriorityOrder";

// Shed priorities of all loads in load management: the loadpoints on a circuit
// and the home battery once it is assigned to one. Sorted by drag, the lowest is
// shed first. Unlike the loadpoints' own settings, these take effect immediately.
export default {
	name: "LmPrioritiesModal",
	components: { DragDropList, DragDropItem, GenericModal },
	emits: ["changed"],
	data() {
		return {
			saving: false,
			error: "",
			values: {},
			initial: {},
			order: [],
			listKey: 0,
			dragBase: null, // order and values when the drag started
		};
	},
	computed: {
		loads() {
			return (store.state?.lmPriorities || []).map((l) => ({
				name: l.name,
				priority: l.priority,
				label: l.battery ? this.$t("config.lmpriorities.battery") : l.title || l.name,
			}));
		},
		changed() {
			return this.loads
				.map((l) => l.name)
				.filter((name) => this.values[name] !== this.initial[name]);
		},
	},
	methods: {
		label(name) {
			return this.loads.find((l) => l.name === name)?.label || name;
		},
		reset() {
			const values = {};
			this.loads.forEach((l) => (values[l.name] = l.priority));
			this.saving = false;
			this.error = "";
			this.values = { ...values };
			this.initial = { ...values };
			// highest first, equal ones as listed
			this.order = this.loads
				.map((l, i) => ({ name: l.name, priority: l.priority, i }))
				.sort((a, b) => b.priority - a.priority || a.i - b.i)
				.map((l) => l.name);
			this.listKey++;
		},
		dragStart() {
			this.dragBase = { order: [...this.order], values: { ...this.values } };
		},
		// the list reports every step of a drag: always from where it started
		reorder(order) {
			const base = this.dragBase || { order: this.order, values: this.values };
			const moved = movedName(base.order, order);
			this.values = moved ? orderPriorities(order, base.values, moved) : { ...base.values };
			this.order = order;
		},
		open() {
			this.reset();
		},
		async save() {
			this.saving = true;
			this.error = "";

			try {
				for (const name of this.changed) {
					await api.post(
						`lmpriority/${encodeURIComponent(name)}/${encodeURIComponent(this.values[name])}`
					);
				}

				this.$emit("changed");
				this.$refs.modal.close();
			} catch (e) {
				this.error =
					e?.response?.data?.error || e.message || this.$t("config.lmpriorities.invalid");
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
