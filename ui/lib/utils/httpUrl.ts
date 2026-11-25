/**
 * Helpers for URLs the app navigates the browser to.
 *
 * An OAuth authorize URL comes from an identity provider's configuration or
 * metadata document, and the dashboard sends the browser there. Only absolute
 * http and https URLs are navigated to: assigning any other scheme to
 * location.href (or passing it to window.open) is not a navigation to a sign-in
 * page, so it is treated as a bad response instead.
 */

/** Reports whether value is an absolute http or https URL. */
export function isHttpUrl(value: unknown): value is string {
	if (typeof value !== "string" || value.trim() === "") return false;
	try {
		const { protocol, hostname } = new URL(value);
		return (protocol === "http:" || protocol === "https:") && hostname !== "";
	} catch {
		return false;
	}
}