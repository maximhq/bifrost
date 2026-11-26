import { describe, expect, it } from "vitest";

import { readSidebarOpenCookie } from "./sidebar.utils";

describe("readSidebarOpenCookie", () => {
	it("returns the persisted collapsed state", () => {
		expect(readSidebarOpenCookie("theme=dark; sidebar_state=false", true)).toBe(false);
	});

	it("returns the persisted expanded state", () => {
		expect(readSidebarOpenCookie("sidebar_state=true; theme=dark", false)).toBe(true);
	});

	it("falls back when the cookie is missing", () => {
		expect(readSidebarOpenCookie("theme=dark", true)).toBe(true);
		expect(readSidebarOpenCookie("", false)).toBe(false);
	});

	it("falls back on an unexpected value", () => {
		expect(readSidebarOpenCookie("sidebar_state=maybe", true)).toBe(true);
	});

	it("does not match a cookie whose name only ends with sidebar_state", () => {
		expect(readSidebarOpenCookie("other_sidebar_state=false", true)).toBe(true);
	});
});