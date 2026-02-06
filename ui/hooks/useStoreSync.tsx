"use client";

import { baseApi } from "@/lib/store/apis/baseApi";
import { governanceApi } from "@/lib/store/apis/governanceApi";
import { useAppDispatch } from "@/lib/store/hooks";
import { useEffect } from "react";
import { useWebSocket } from "./useWebSocket";

/**
 * Hook that subscribes to WebSocket messages for real-time cache updates.
 *
 * Handles two message types:
 * - store_update: Invalidates RTK Query cache tags (triggers refetch)
 * - governance_update: Merges budget/rate-limit diffs into cached governance queries
 */
export function useStoreSync() {
	const { subscribe } = useWebSocket();
	const dispatch = useAppDispatch();

	useEffect(() => {
		const unsubTagSync = subscribe("store_update", (data) => {
			if (data.tags && Array.isArray(data.tags)) {
				dispatch(baseApi.util.invalidateTags(data.tags));
			}
		});

		const unsubGovUpdate = subscribe("governance_update", (data) => {
			const { budgets, rate_limits } = data.data || {};
			if (!budgets && !rate_limits) return;

			// Merge into model configs cache
			dispatch(
				governanceApi.util.updateQueryData("getModelConfigs", undefined, (draft) => {
					if (!draft?.model_configs) return;
					for (const mc of draft.model_configs) {
						if (budgets && mc.budget?.id && budgets[mc.budget.id]) {
							mc.budget = budgets[mc.budget.id];
						}
						if (rate_limits && mc.rate_limit?.id && rate_limits[mc.rate_limit.id]) {
							mc.rate_limit = rate_limits[mc.rate_limit.id];
						}
					}
				})
			);

			// Merge into provider governance cache
			dispatch(
				governanceApi.util.updateQueryData("getProviderGovernance", undefined, (draft) => {
					if (!draft?.providers) return;
					for (const p of draft.providers) {
						if (budgets && p.budget?.id && budgets[p.budget.id]) {
							p.budget = budgets[p.budget.id];
						}
						if (rate_limits && p.rate_limit?.id && rate_limits[p.rate_limit.id]) {
							p.rate_limit = rate_limits[p.rate_limit.id];
						}
					}
				})
			);

			// Merge into virtual keys cache (virtual_keys is an array)
			dispatch(
				governanceApi.util.updateQueryData("getVirtualKeys", undefined, (draft) => {
					if (!draft?.virtual_keys) return;
					for (const vk of draft.virtual_keys) {
						if (budgets && vk.budget?.id && budgets[vk.budget.id]) {
							vk.budget = budgets[vk.budget.id];
						}
						if (rate_limits && vk.rate_limit?.id && rate_limits[vk.rate_limit.id]) {
							vk.rate_limit = rate_limits[vk.rate_limit.id];
						}
						// Also merge into provider_configs
						if (vk.provider_configs) {
							for (const pc of vk.provider_configs) {
								if (budgets && pc.budget?.id && budgets[pc.budget.id]) {
									pc.budget = budgets[pc.budget.id];
								}
								if (rate_limits && pc.rate_limit?.id && rate_limits[pc.rate_limit.id]) {
									pc.rate_limit = rate_limits[pc.rate_limit.id];
								}
							}
						}
					}
				})
			);
		});

		return () => {
			unsubTagSync();
			unsubGovUpdate();
		};
	}, [subscribe, dispatch]);
}
