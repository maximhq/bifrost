import { describe, expect, it } from "vitest";
import { shouldSeedHeaders } from "./mcpLibraryInstallSheet.utils";

describe("shouldSeedHeaders", () => {
	it("includes per-user header authentication", () => {
		expect(shouldSeedHeaders("headers", false)).toBe(true);
		expect(shouldSeedHeaders("per_user_headers", false)).toBe(true);
		expect(shouldSeedHeaders("per_user_headers", true)).toBe(false);
	});
});