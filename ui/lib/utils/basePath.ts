/**
 * Base path utility - the URL path prefix Bifrost is served under behind a
 * reverse proxy (server.base_path, e.g. "/bifrost" for https://abc.com/bifrost).
 *
 * The Go server injects window.__BIFROST_BASE_PATH__ into the HTML shell when a
 * base path is configured (transports/bifrost-http/handlers/ui.go). It is absent
 * at the domain root and in the Vite dev server, where every helper is a no-op.
 */

declare global {
	interface Window {
		__BIFROST_BASE_PATH__?: string;
	}
}

// Mirrors lib.NormalizeBasePath on the server: one or more "/segment" parts of
// URL-unreserved characters, no trailing slash.
const BASE_PATH_PATTERN = /^(\/[A-Za-z0-9._~-]+)+$/;

/**
 * Get the configured base path, or "" when served at the domain root.
 */
export function getBasePath(): string {
	if (typeof window === "undefined") {
		return "";
	}
	const value = window.__BIFROST_BASE_PATH__;
	if (typeof value !== "string" || !BASE_PATH_PATTERN.test(value) || value.split("/").some((s) => s === "." || s === "..")) {
		return "";
	}
	return value;
}

/**
 * Prefix a root-absolute path ("/login", "/images/x.png") with the base path.
 * Anything else (absolute URLs, protocol-relative URLs, relative paths, data:
 * URIs) is returned unchanged.
 */
export function withBasePath(path: string): string {
	if (!path.startsWith("/") || path.startsWith("//")) {
		return path;
	}
	return `${getBasePath()}${path}`;
}

/**
 * Remove the base path from a browser pathname, giving the router-relative
 * path ("/bifrost/workspace/logs" -> "/workspace/logs"). Paths outside the
 * base path are returned unchanged.
 */
export function stripBasePath(pathname: string): string {
	const basePath = getBasePath();
	if (!basePath) {
		return pathname;
	}
	if (pathname === basePath) {
		return "/";
	}
	if (pathname.startsWith(`${basePath}/`)) {
		return pathname.slice(basePath.length);
	}
	return pathname;
}

/**
 * The current router-relative location (pathname without the base path, plus
 * the query string), for login "goto" values and per-page keys.
 */
export function getCurrentAppPath(): string {
	if (typeof window === "undefined") {
		return "/";
	}
	return stripBasePath(window.location.pathname) + window.location.search;
}