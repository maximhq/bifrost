import { withBasePath } from "./basePath";

export const DEFAULT_POST_LOGIN_PATH = "/workspace";

export function normalizeLoginGoto(value: string | null | undefined): string | null {
	if (
		!value ||
		!isWorkspaceRoute(value) ||
		value.startsWith("//") ||
		value.includes("\\") ||
		value.includes("\n") ||
		value.includes("\r")
	) {
		return null;
	}
	return value;
}

/**
 * The /login URL, under the base path, that returns to the app-relative `goto`
 * path after sign-in. Use it as-is for <a href> and window.location.
 */
export function buildLoginHref(goto: string): string {
	return withBasePath(`/login?goto=${encodeURIComponent(goto)}`);
}

export function getLoginGotoFromSearch(search: string): string | null {
	return normalizeLoginGoto(new URLSearchParams(search).get("goto"));
}

function isWorkspaceRoute(value: string): boolean {
	return (
		value === DEFAULT_POST_LOGIN_PATH ||
		value.startsWith(`${DEFAULT_POST_LOGIN_PATH}/`) ||
		value.startsWith(`${DEFAULT_POST_LOGIN_PATH}?`) ||
		value.startsWith(`${DEFAULT_POST_LOGIN_PATH}#`) ||
		value === "/oauth/consent" ||
		value.startsWith("/oauth/consent?") ||
		value.startsWith("/oauth/consent#") ||
		value.startsWith("/oauth/consent/")
	);
}