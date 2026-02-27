"use client";

import { baseApi } from "@/lib/store/apis/baseApi";
import { useAppDispatch } from "@/lib/store/hooks";
import { useEffect } from "react";
import { useWebSocket } from "./useWebSocket";

/**
 * Hook that subscribes to WebSocket messages for real-time cache updates.
 *
 * Handles store_update messages to invalidate RTK Query cache tags (triggers refetch).
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

		return () => {
			unsubTagSync();
		};
	}, [subscribe, dispatch]);
}
