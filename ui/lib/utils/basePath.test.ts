import { afterEach, describe, expect, it, vi } from "vitest";
import { getBasePath, getCurrentAppPath, stripBasePath, withBasePath } from "./basePath";
import { normalizeLoginGoto } from "./loginGoto";

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