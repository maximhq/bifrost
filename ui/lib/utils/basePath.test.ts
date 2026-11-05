import { afterEach, describe, expect, it, vi } from "vitest";
import { getBasePath, getCurrentAppPath, stripBasePath, withBasePath } from "./basePath";
import { buildLoginHref, normalizeLoginGoto } from "./loginGoto";

/** Stubs `window` with the injected base path and the current location. */
function stubWindow(basePath: string | undefined, pathname = "/", search = "") {
	vi.stubGlobal("window", { __BIFROST_BASE_PATH__: basePath, location: { pathname, search } });
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("getBasePath", () => {
	it("is empty without a window, a global, or a valid value", () => {
		expect(getBasePath()).toBe("");
		for (const value of [undefined, "", "/", "bifrost", "/bifrost/", "//evil.com", "/a/../b", '/x"y', "/a b"]) {
			stubWindow(value);
			expect(getBasePath(), String(value)).toBe("");
		}
	});

	it("returns the injected prefix", () => {
		stubWindow("/tools/llm-gw");
		expect(getBasePath()).toBe("/tools/llm-gw");
	});
});

describe("withBasePath", () => {
	it("is a no-op at the domain root", () => {
		stubWindow(undefined);
		expect(withBasePath("/images/mcp.svg")).toBe("/images/mcp.svg");
	});

	it("prefixes root-absolute paths only", () => {
		stubWindow("/bifrost");
		expect(withBasePath("/images/mcp.svg")).toBe("/bifrost/images/mcp.svg");
		expect(withBasePath("/login?goto=%2Fworkspace")).toBe("/bifrost/login?goto=%2Fworkspace");
		expect(withBasePath("https://docs.getbifrost.ai")).toBe("https://docs.getbifrost.ai");
		expect(withBasePath("//cdn.example/x.png")).toBe("//cdn.example/x.png");
		expect(withBasePath("data:image/png;base64,AA")).toBe("data:image/png;base64,AA");
	});
});

describe("stripBasePath", () => {
	it("removes the prefix on whole segments only", () => {
		stubWindow("/bifrost");
		expect(stripBasePath("/bifrost")).toBe("/");
		expect(stripBasePath("/bifrost/workspace/logs")).toBe("/workspace/logs");
		expect(stripBasePath("/bifrostx/workspace")).toBe("/bifrostx/workspace");
		expect(stripBasePath("/workspace")).toBe("/workspace");
	});
});

describe("getCurrentAppPath", () => {
	it("gives a goto value the login page accepts under a prefix", () => {
		stubWindow("/bifrost", "/bifrost/workspace/logs", "?id=1");
		const goto = getCurrentAppPath();
		expect(goto).toBe("/workspace/logs?id=1");
		expect(normalizeLoginGoto(goto)).toBe("/workspace/logs?id=1");
	});
});
describe("root-absolute asset references", () => {
	// A src/href literal starting with "/" bypasses the base path and 404s when
	// Bifrost is served under a prefix; wrap it with withBasePath instead.
	it("are not hardcoded in UI sources", async () => {
		const { readdirSync, readFileSync } = await import("node:fs");
		const { join, resolve } = await import("node:path");
		const uiRoot = resolve(__dirname, "../..");
		const rootAbsolute = /\b(?:src|href)=(?:\{`|\{"|")\/(?!\/)/;
		const offenders: string[] = [];
		for (const dir of ["app", "components", "hooks", "lib"]) {
			for (const entry of readdirSync(join(uiRoot, dir), { recursive: true, withFileTypes: true })) {
				if (!entry.isFile() || !/\.tsx?$/.test(entry.name) || /\.test\.tsx?$/.test(entry.name)) continue;
				const file = join(entry.parentPath, entry.name);
				readFileSync(file, "utf8")
					.split("\n")
					.forEach((line, i) => {
						if (rootAbsolute.test(line)) offenders.push(`${file.slice(uiRoot.length + 1)}:${i + 1}`);
					});
			}
		}
		expect(offenders).toEqual([]);
	});
});

describe("buildLoginHref", () => {
	// Rendered as <a href> and used for window.location, so it must carry the
	// base path itself; goto stays app-relative so normalizeLoginGoto accepts it.
	it("carries the base path while goto stays app-relative", () => {
		stubWindow("/bifrost");
		const href = buildLoginHref("/oauth/consent?flow=abc");
		expect(href).toBe("/bifrost/login?goto=%2Foauth%2Fconsent%3Fflow%3Dabc");
		const goto = new URLSearchParams(href.split("?")[1]).get("goto");
		expect(normalizeLoginGoto(goto)).toBe("/oauth/consent?flow=abc");
	});

	it("is the plain /login URL at the domain root", () => {
		stubWindow(undefined);
		expect(buildLoginHref("/workspace/mcp-sessions/auth")).toBe("/login?goto=%2Fworkspace%2Fmcp-sessions%2Fauth");
	});
});