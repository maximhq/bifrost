// Kept out of sidebar.tsx so the cookie parsing can be tested without loading the component.

export const SIDEBAR_COOKIE_NAME = "sidebar_state";

// Returns the persisted open state from a `document.cookie` string, or `fallback` when the
// cookie is missing or holds anything other than "true"/"false".
export function readSidebarOpenCookie(cookie: string, fallback: boolean): boolean {
	const match = cookie.match(new RegExp(`(?:^|; )${SIDEBAR_COOKIE_NAME}=([^;]*)`));
	if (match && (match[1] === "true" || match[1] === "false")) {
		return match[1] === "true";
	}
	return fallback;
}