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
