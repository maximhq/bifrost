/**
 * Vite's renderBuiltUrl hook (wired in vite.config.mts), kept here so it can be
 * unit tested.
 *
 * The server may be mounted under a path prefix chosen at runtime
 * (server.base_path). Chunks and assets loaded from JS resolve against the
 * prefix the Go server injects into the HTML shell as
 * window.__BIFROST_BASE_PATH__; CSS references stay relative to the stylesheet.
 * The shell's own tags are rewritten by the Go server (handlers/ui.go).
 *
 * The global is read only when it is a string. The server sets it only to a
 * validated path, and an element with id or name "__BIFROST_BASE_PATH__" (DOM
 * clobbering) would otherwise choose the origin chunks are loaded from.
 */
export function renderBuiltUrl(filename: string, { hostType }: { hostType: "js" | "css" | "html" }) {
	if (hostType === "js") {
		return {
			runtime: `(typeof window.__BIFROST_BASE_PATH__=="string"?window.__BIFROST_BASE_PATH__:"")+${JSON.stringify(`/${filename}`)}`,
		};
	}
	if (hostType === "css") {
		return { relative: true };
	}
	return `/${filename}`;
}