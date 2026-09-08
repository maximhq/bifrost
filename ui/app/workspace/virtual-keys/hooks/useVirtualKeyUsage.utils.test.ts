import { RateLimit } from "@/lib/types/governance";
import { describe, expect, it } from "vitest";
import { resolveDisplayRateLimit } from "./useVirtualKeyUsage.utils";

const vkRateLimit: RateLimit = {
	id: "vk-rl",
	token_max_limit: 1000,
	token_current_usage: 10,
	token_last_reset: "2026-01-01T00:00:00Z",
	request_max_limit: 50,
	request_current_usage: 5,
	request_last_reset: "2026-01-01T00:00:00Z",
};

describe("resolveDisplayRateLimit", () => {
	it("shows the VK's own rate limit for an unmanaged key", () => {
		expect(resolveDisplayRateLimit({ isManagedByProfile: false, profileRateLimit: undefined, vkRateLimit })).toEqual(vkRateLimit);
	});

	it("shows the profile's rate limit for a managed key whose profile is visible", () => {
		const result = resolveDisplayRateLimit({
			isManagedByProfile: true,
			profileRateLimit: { token_max_limit: 99, token_current_usage: 7, token_last_reset: "2026-02-02T00:00:00Z" },
			vkRateLimit,
		});
		expect(result?.token_max_limit).toBe(99);
		expect(result?.token_current_usage).toBe(7);
	});

	it("shows nothing for a managed key whose visible profile carries no rate limit", () => {
		expect(resolveDisplayRateLimit({ isManagedByProfile: true, profileRateLimit: {}, vkRateLimit })).toBeUndefined();
	});

	// The finding: is_access_profile_managed is true but the access-profile query is
	// RBAC-blocked, so no profile resolves. Falling back to vk.rate_limit here would show
	// limits that do not govern the key.
	it("shows nothing for a managed key whose profile the caller cannot see", () => {
		expect(resolveDisplayRateLimit({ isManagedByProfile: true, profileRateLimit: undefined, vkRateLimit })).toBeUndefined();
	});

	it("shows nothing for a managed key with no profile visible and no VK rate limit either", () => {
		expect(resolveDisplayRateLimit({ isManagedByProfile: true, profileRateLimit: undefined, vkRateLimit: undefined })).toBeUndefined();
	});

	it("shows nothing for an unmanaged key with no rate limit", () => {
		expect(resolveDisplayRateLimit({ isManagedByProfile: false, profileRateLimit: undefined, vkRateLimit: undefined })).toBeUndefined();
	});
});
