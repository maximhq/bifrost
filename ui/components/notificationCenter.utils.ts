/**
 * True when the backend has told us notifications can never be served, as
 * opposed to having failed this once.
 *
 * NotificationService is constructed with the config store, and lib.Config
 * leaves ConfigStore nil whenever the store is disabled, so both routes answer
 * 503 for the whole life of the process. That is a supported deployment, not an
 * outage, and the UI should hide the feature instead of showing an error the
 * user can never clear. Every other failure stays retryable.
 */
export function isNotificationsUnavailable(error: unknown): boolean {
	return typeof error === "object" && error !== null && "status" in error && (error as { status?: unknown }).status === 503;
}

/**
 * Whether the topbar trigger should be hidden entirely.
 *
 * Empty deployments should not flash an icon, so the trigger stays out of the
 * topbar while the first load is in flight and whenever the feed is genuinely
 * empty. A failed load is not one of those: with no rows to fall back on it is
 * the only state that can reach the tray's "Try again" retry, so hiding it
 * there strands the user on a feed that silently stays empty until a reload.
 *
 * `open` overrides all of it, so dismissing the last row does not yank the
 * popover out from under the pointer.
 */
export function shouldHideNotificationTrigger({
	open,
	isLoading,
	isError,
	count,
}: {
	open: boolean;
	isLoading: boolean;
	isError: boolean;
	count: number;
}): boolean {
	if (open) return false;
	if (isLoading) return true;
	return count === 0 && !isError;
}
