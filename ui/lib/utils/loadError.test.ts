import { describe, expect, it } from "vitest";
import { showLoadError } from "./loadError";

describe("showLoadError", () => {
	it("shows the load error when the query failed and there is no cached data", () => {
		expect(showLoadError(true, undefined)).toBe(true);
		expect(showLoadError(true, null)).toBe(true);
	});

	it("keeps cached data when a later fetch fails", () => {
		expect(showLoadError(true, [{ name: "openai" }])).toBe(false);
		expect(showLoadError(true, { total_count: 0, rules: [] })).toBe(false);
	});

	it("keeps the empty state when the query succeeded with nothing", () => {
		expect(showLoadError(false, [])).toBe(false);
		expect(showLoadError(false, undefined)).toBe(false);
	});
});