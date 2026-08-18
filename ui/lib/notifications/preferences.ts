const STORAGE_PREFIX = "bifrost-notifications";

export interface StoredNotificationPreferences {
	readIds: string[];
	dismissedIds: string[];
}

export function notificationPreferenceKey(scope: string): string {
	return `${STORAGE_PREFIX}:${encodeURIComponent(scope)}`;
}

export function readNotificationPreferences(scope: string): StoredNotificationPreferences {
	if (typeof window === "undefined") return { readIds: [], dismissedIds: [] };
	try {
		const parsed = JSON.parse(localStorage.getItem(notificationPreferenceKey(scope)) || "{}") as Partial<StoredNotificationPreferences>;
		return {
			readIds: Array.isArray(parsed.readIds) ? parsed.readIds.filter((id): id is string => typeof id === "string").slice(-500) : [],
			dismissedIds: Array.isArray(parsed.dismissedIds)
				? parsed.dismissedIds.filter((id): id is string => typeof id === "string").slice(-500)
				: [],
		};
	} catch {
		return { readIds: [], dismissedIds: [] };
	}
}

/**
 * Best-effort persistence. setItem throws QuotaExceededError when storage is
 * full or the user declined more space, and merely touching localStorage throws
 * where storage is blocked outright. This runs from useNotificationSync's effect
 * on every preference change, so an unguarded write turns a failure to remember
 * which notifications were read into an uncaught error that takes down the
 * dashboard. Losing the preference is the correct outcome; the read path already
 * treats missing state as "nothing read yet".
 */
export function writeNotificationPreferences(scope: string, preferences: StoredNotificationPreferences) {
	if (typeof window === "undefined") return;
	try {
		localStorage.setItem(
			notificationPreferenceKey(scope),
			JSON.stringify({ readIds: preferences.readIds.slice(-500), dismissedIds: preferences.dismissedIds.slice(-500) }),
		);
	} catch {
		// Intentionally silent: nothing the user can act on, and the tray works
		// without persisted read state.
	}
}