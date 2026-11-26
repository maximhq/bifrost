import { describe, expect, it } from "vitest";
import { renderBuiltUrl } from "./builtUrl";

/** Evaluates the runtime expression Vite inlines for a JS-loaded chunk, against a stub window. */
function chunkUrl(win: Record<string, unknown>): string {
	const out = renderBuiltUrl("assets/page-abc.js", { hostType: "js" });
	if (typeof out !== "object" || !("runtime" in out)) throw new Error("expected a runtime expression");
	return new Function("window", `return ${out.runtime}`)(win);
}

describe("renderBuiltUrl", () => {
	it("prefixes JS-loaded chunks with the injected base path", () => {
		expect(chunkUrl({ __BIFROST_BASE_PATH__: "/bifrost" })).toBe("/bifrost/assets/page-abc.js");
	});

	it("loads chunks from the domain root when no base path is injected", () => {
		expect(chunkUrl({})).toBe("/assets/page-abc.js");
	});

	it("ignores a non-string global, such as an element clobbering window.__BIFROST_BASE_PATH__", () => {
		const clobbered = { toString: () => "https://evil.example.org" };
		expect(chunkUrl({ __BIFROST_BASE_PATH__: clobbered })).toBe("/assets/page-abc.js");
	});

	it("keeps CSS references relative and other hosts root-absolute", () => {
		expect(renderBuiltUrl("assets/font.woff2", { hostType: "css" })).toEqual({ relative: true });
		expect(renderBuiltUrl("assets/index.js", { hostType: "html" })).toBe("/assets/index.js");
	});
});