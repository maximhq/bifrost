import { describe, expect, it } from "vitest";
import { showProvidersLoadError } from "./page.utils";

describe("showProvidersLoadError", () => {
	it("shows the load error when the query failed and there is no cached list", () => {
		expect(showProvidersLoadError(true, undefined)).toBe(true);
		expect(showProvidersLoadError(true, null)).toBe(true);
	});

	it("keeps a cached list when a later fetch fails", () => {
		expect(showProvidersLoadError(true, [{ name: "openai" }])).toBe(false);
	});

	it("keeps the empty state when the query succeeded with no providers", () => {
		expect(showProvidersLoadError(false, [])).toBe(false);
		expect(showProvidersLoadError(false, undefined)).toBe(false);
	});
});
