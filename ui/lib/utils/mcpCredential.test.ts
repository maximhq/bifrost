import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
	formatTokenExpiry,
	missingHeaderKeys,
	providerRejectedTheClient,
	shouldSuggestReplacementClient,
	supportsClientReregistration,
} from "./mcpCredential";

describe("formatTokenExpiry", () => {
	const now = new Date("2026-09-03T12:00:00Z");

	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(now);
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	test("missing expiry renders a dash", () => {
		expect(formatTokenExpiry(undefined, "active")).toBe("-");
		expect(formatTokenExpiry(null, "active")).toBe("-");
		expect(formatTokenExpiry("", "active")).toBe("-");
	});

	test("future expiry is relative", () => {
		expect(formatTokenExpiry("2026-09-03T12:42:00Z", "active")).toBe("in 42 min");
		expect(formatTokenExpiry("2026-09-03T15:30:00Z", "active")).toBe("in 3 hours");
		expect(formatTokenExpiry("2026-09-10T12:00:00Z", "active")).toBe("in 7 days");
	});

	test("a past expiry only reads as expired when the credential is dead", () => {
		const past = "2026-09-01T12:00:00Z";
		expect(formatTokenExpiry(past, "active")).toBe("Refreshes on next use");
		expect(formatTokenExpiry(past, "active", true)).toBe("Refreshes on next use");
		expect(formatTokenExpiry(past, "orphaned")).toBe("Refreshes when access is restored");
		expect(formatTokenExpiry(past, "needs_reauth")).toBe("expired");
	});

	test("the exact expiry instant counts as expired", () => {
		const exact = "2026-09-03T12:00:00Z";
		expect(formatTokenExpiry(exact, "needs_reauth")).toBe("expired");
		expect(formatTokenExpiry(exact, "active", false)).toBe("expired");
		expect(formatTokenExpiry(exact, "active", true)).toBe("Refreshes on next use");
	});

	test("a past expiry without a refresh token is expired whatever the status", () => {
		const past = "2026-09-01T12:00:00Z";
		expect(formatTokenExpiry(past, "active", false)).toBe("expired");
		expect(formatTokenExpiry(past, "orphaned", false)).toBe("expired");
		// Still in the future: the refresh token only matters once it lapses.
		expect(formatTokenExpiry("2026-09-03T12:42:00Z", "active", false)).toBe("in 42 min");
	});

	test("unparseable input passes through", () => {
		expect(formatTokenExpiry("not-a-date", "active")).toBe("not-a-date");
	});
});

describe("missingHeaderKeys", () => {
	test("empty required list has nothing missing", () => {
		expect(missingHeaderKeys(undefined, ["X-API-Key"])).toEqual([]);
		expect(missingHeaderKeys([], undefined)).toEqual([]);
	});

	test("everything is missing when nothing is covered", () => {
		expect(missingHeaderKeys(["X-API-Key", "X-Tenant-ID"], undefined)).toEqual(["X-API-Key", "X-Tenant-ID"]);
	});

	test("compares header names case-insensitively and ignores blanks", () => {
		expect(missingHeaderKeys(["X-API-Key", " X-Tenant-ID ", "", "X-Region"], ["x-api-key", "X-Tenant-Id"])).toEqual(["X-Region"]);
	});
});

describe("providerRejectedTheClient", () => {
	test("matches the provider disowning Bifrost's client", () => {
		expect(providerRejectedTheClient("provider rejected the refresh (HTTP 401, invalid_client: Invalid client_id)")).toBe(true);
	});

	test("matches regardless of case", () => {
		expect(providerRejectedTheClient("HTTP 401, INVALID_CLIENT")).toBe(true);
	});

	test("does not match a revoked grant, which a plain reauthorize fixes", () => {
		expect(providerRejectedTheClient("provider rejected the refresh (HTTP 400, invalid_grant: Token has been revoked)")).toBe(false);
	});

	test("does not match a rotation or an absent reason", () => {
		expect(providerRejectedTheClient("OAuth client credentials were rotated")).toBe(false);
		expect(providerRejectedTheClient("")).toBe(false);
		expect(providerRejectedTheClient(undefined)).toBe(false);
	});
});

// The servers table offers "Reauthorize with a new client" for exactly these
// auth types, and POST /reregister refuses every other one. Both the menu item
// and the hint that sends admins to it read this, so neither can name an
// action the other does not offer.
describe("supportsClientReregistration", () => {
	test("covers the auth types whose client_id Bifrost registered itself", () => {
		expect(supportsClientReregistration("oauth")).toBe(true);
		expect(supportsClientReregistration("per_user_oauth")).toBe(true);
	});

	test("excludes token_exchange, whose client is configured by hand and re-verified, never re-registered", () => {
		expect(supportsClientReregistration("token_exchange")).toBe(false);
	});

	test("excludes auth types with no OAuth client at all", () => {
		expect(supportsClientReregistration("none")).toBe(false);
		expect(supportsClientReregistration("headers")).toBe(false);
		expect(supportsClientReregistration("per_user_headers")).toBe(false);
	});

	// auth_type is optional on the wire, and a server without one has no OAuth
	// client to replace.
	test("excludes a server with no auth type recorded", () => {
		expect(supportsClientReregistration(undefined)).toBe(false);
		expect(shouldSuggestReplacementClient(undefined, "invalid_client: Invalid client_id")).toBe(false);
	});
});

describe("shouldSuggestReplacementClient", () => {
	const disowned = "provider rejected the refresh (HTTP 401, invalid_client: Invalid client_id)";

	test("suggests it where the action exists and the provider disowned the client", () => {
		expect(shouldSuggestReplacementClient("oauth", disowned)).toBe(true);
		expect(shouldSuggestReplacementClient("per_user_oauth", disowned)).toBe(true);
	});

	// A token_exchange credential reaches invalid_client too: the identity
	// provider's own error code is kept verbatim in the status reason. But its
	// actions menu has "Re-verify as me", not "Reauthorize with a new client",
	// so the hint would send the admin looking for a menu item that is not there.
	test("never points a token_exchange admin at an action their menu does not have", () => {
		expect(shouldSuggestReplacementClient("token_exchange", "invalid_client: client authentication failed")).toBe(false);
	});

	test("stays quiet for a revoked grant, which a plain reauthorize fixes", () => {
		expect(shouldSuggestReplacementClient("oauth", "provider rejected the refresh (HTTP 400, invalid_grant: Token has been revoked)")).toBe(
			false,
		);
		expect(shouldSuggestReplacementClient("oauth", undefined)).toBe(false);
	});
});