import { useWebSocket } from "@/hooks/useWebSocket";
import {
	addNotification,
	hydrateNotificationPreferences,
	pruneExpiredNotifications,
	selectNotificationPreferences,
	setNotifications,
	useAppDispatch,
	useAppSelector,
	useGetNotificationsQuery,
} from "@/lib/store";
import type { Notification } from "@/lib/types/notifications";
import { readNotificationPreferences, writeNotificationPreferences } from "@/lib/notifications/preferences";
import { getUserInfo } from "@enterprise/lib/store/utils/tokenManager";
import { useEffect, useMemo } from "react";

/** Expired rows are dropped this often so a tab left open overnight converges. */
const PRUNE_INTERVAL_MS = 60_000;

/**
 * Identity that read/dismissed state is filed under.
 *
 * This state is deliberately client-side only: it lives in localStorage, not on
 * the server, so it does not follow the user across browsers or devices, and on
 * an OSS deployment with no SSO every browser profile shares the "local-admin"
 * bucket and therefore its own read state. That is the accepted trade for not
 * adding a per-user write path; the notification rows themselves are always
 * server-owned and role-scoped. Changing this means adding a real read-receipt
 * endpoint, not widening the key.
 */
function getNotificationScope(): string {
	const user = getUserInfo() as ReturnType<typeof getUserInfo> & { sub?: string; id?: string };
	return user?.sub || user?.id || user?.email || user?.preferred_username || "local-admin";
}

export function useNotificationSync() {
	const dispatch = useAppDispatch();
	const { subscribe } = useWebSocket();
	const preferences = useAppSelector(selectNotificationPreferences);
	const scope = useMemo(getNotificationScope, []);
	const { data } = useGetNotificationsQuery({ limit: 50 });

	useEffect(() => {
		const stored = readNotificationPreferences(scope);
		dispatch(hydrateNotificationPreferences({ scope, ...stored }));
	}, [dispatch, scope]);

	useEffect(() => {
		if (data?.notifications) dispatch(setNotifications(data.notifications));
	}, [data, dispatch]);

	useEffect(
		() =>
			subscribe("notification", (message) => {
				if (message.data?.id) dispatch(addNotification(message.data as Notification));
			}),
		[dispatch, subscribe],
	);

	useEffect(() => {
		const timer = setInterval(() => dispatch(pruneExpiredNotifications()), PRUNE_INTERVAL_MS);
		return () => clearInterval(timer);
	}, [dispatch]);

	useEffect(() => {
		if (preferences.scope !== scope) return;
		writeNotificationPreferences(scope, preferences);
	}, [preferences, scope]);
}