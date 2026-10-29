import { describe, expect, it } from "vitest";
import { shouldSeedHeaders } from "./mcpLibraryInstallSheet.utils";

const emptySecretVar = { value: "", ref: "" };

// The shape buildInitialValues produces for a catalog entry, so the assertions
// below are about the rows the installer actually receives rather than a bare
// boolean. getHeadersValidationError rejects any row with neither value nor
// ref, so an empty row here is an install the sheet refuses to submit.
function seededHeaders(authType: "headers" | "per_user_headers", requiredKeys: string[]) {
	const declared = Object.fromEntries(requiredKeys.map((key) => [key, { ...emptySecretVar }]));
	return shouldSeedHeaders(authType, false) ? declared : undefined;
}

describe("shouldSeedHeaders", () => {
	it("seeds the static header rows a headers entry declares", () => {
		expect(shouldSeedHeaders("headers", false)).toBe(true);
		expect(seededHeaders("headers", ["Authorization"])).toEqual({
			Authorization: { value: "", ref: "" },
		});
	});

	// #7374: the Magic Hour entry seeded an empty Authorization row, which the
	// shared validator rejects before MCPHeadersAuthorizer can open. The names
	// a per-user entry declares reach the form through perUserHeaderKeys, so
	// seeding them here as well only produced a row nobody is meant to fill.
	it("leaves the static map untouched for per-user header authentication", () => {
		expect(shouldSeedHeaders("per_user_headers", false)).toBe(false);
		expect(seededHeaders("per_user_headers", ["Authorization"])).toBeUndefined();
	});

	it.each([["headers"], ["per_user_headers"]] as const)("never seeds for a stdio server, which sends no request auth (%s)", (authType) => {
		expect(shouldSeedHeaders(authType, true)).toBe(false);
	});
});