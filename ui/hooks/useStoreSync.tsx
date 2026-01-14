"use client";

import { baseApi } from "@/lib/store/apis/baseApi";
import { useAppDispatch } from "@/lib/store/hooks";
import { useEffect } from "react";
import { useWebSocket } from "./useWebSocket";

/**
 * Hook that subscribes to WebSocket store_update messages and invalidates
 * RTK Query cache tags accordingly. This enables real-time cache invalidation
 * when data changes on the backend.
 *
 * Expected message format: { type: "store_update", tags: ["Providers", "VirtualKeys", ...] }
 */
export function useStoreSync() {
	const { subscribe } = useWebSocket();
	const dispatch = useAppDispatch();

	useEffect(() => {
		const unsubscribe = subscribe("store_update", (data) => {
			if (data.tags && Array.isArray(data.tags)) {
				dispatch(baseApi.util.invalidateTags(data.tags));
			}
		});

		return unsubscribe;
	}, [subscribe, dispatch]);
}
