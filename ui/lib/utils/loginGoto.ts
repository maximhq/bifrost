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

export function getLoginGotoFromSearch(search: string): string | null {
	return normalizeLoginGoto(new URLSearchParams(search).get("goto"));
}

function isWorkspaceRoute(value: string): boolean {
	return (
		value === DEFAULT_POST_LOGIN_PATH ||
		value.startsWith(`${DEFAULT_POST_LOGIN_PATH}/`) ||
		value.startsWith(`${DEFAULT_POST_LOGIN_PATH}?`) ||
		value.startsWith(`${DEFAULT_POST_LOGIN_PATH}#`)
	);
}