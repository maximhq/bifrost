import { RateLimit } from "@/lib/types/governance";
import { describe, expect, it } from "vitest";
import {
	allProfilesSpent,
	type PayingProfile,
	payingProfiles,
	profileBudgetBarClass,
	profileBudgetLines,
	resolveDisplayRateLimit,
	sharedBudgetNote,
	summarizeProfileBudgets,
	virtualKeyManagement,
} from "./useVirtualKeyUsage.utils";

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

	// A key owned by a team, business unit or customer holding a profile has no user profile to
	// resolve; the server hands back the entity profile's rate limit as vk.rate_limit.
	it("shows the entity profile's rate limit for a key its owning entity's profile governs", () => {
		expect(resolveDisplayRateLimit({ isManagedByProfile: true, isEntityGoverned: true, profileRateLimit: undefined, vkRateLimit })).toEqual(
			vkRateLimit,
		);
	});

	it("shows nothing for an unmanaged key with no rate limit", () => {
		expect(resolveDisplayRateLimit({ isManagedByProfile: false, profileRateLimit: undefined, vkRateLimit: undefined })).toBeUndefined();
	});
});
describe("virtualKeyManagement", () => {
	// An owned key is never also a user's, so a managed key with an owner is governed by that owner's
	// profile and stays the owner's to edit, unassign and delete; one without an owner is a user's
	// managed key, which stays locked.
	it("a managed key a team, business unit or customer owns is owner-governed", () => {
		for (const owner of [{ team_id: "t" }, { business_unit_id: "b" }, { customer_id: "c" }]) {
			expect(virtualKeyManagement({ is_access_profile_managed: true, ...owner })).toEqual({
				isOwnerGoverned: true,
				isUserProfileManaged: false,
			});
		}
	});

	it("a managed key with no owner is a user's managed key", () => {
		expect(virtualKeyManagement({ is_access_profile_managed: true })).toEqual({ isOwnerGoverned: false, isUserProfileManaged: true });
	});

	it("an unmanaged key is neither, owned or not", () => {
		expect(virtualKeyManagement({ is_access_profile_managed: false, team_id: "t" })).toEqual({
			isOwnerGoverned: false,
			isUserProfileManaged: false,
		});
		expect(virtualKeyManagement(undefined)).toEqual({ isOwnerGoverned: false, isUserProfileManaged: false });
	});
});
describe("sharedBudgetNote", () => {
	const t = ((key: string, opts?: { name?: string }) => (opts?.name ? `${key}:${opts.name}` : key)) as unknown as Parameters<
		typeof sharedBudgetNote
	>[0];

	it("says nothing for a key with its own budget", () => {
		expect(sharedBudgetNote(t, { isManagedByProfile: false, isOwnerGoverned: false, assignee: { name: "Ann" } })).toBeUndefined();
	});

	it("names the assignee for a user's managed key", () => {
		expect(sharedBudgetNote(t, { isManagedByProfile: true, isOwnerGoverned: false, assignee: { name: "Ann" } })).toBe(
			"virtualKeys.sharedBudgetUser:Ann",
		);
	});

	it("falls back to the email, then to an unnamed user", () => {
		expect(sharedBudgetNote(t, { isManagedByProfile: true, isOwnerGoverned: false, assignee: { name: "", email: "a@x.io" } })).toBe(
			"virtualKeys.sharedBudgetUser:a@x.io",
		);
		expect(sharedBudgetNote(t, { isManagedByProfile: true, isOwnerGoverned: false })).toBe("virtualKeys.sharedBudgetUserUnnamed");
	});

	it("points at the owning entity for an owner-governed key", () => {
		expect(sharedBudgetNote(t, { isManagedByProfile: true, isOwnerGoverned: true })).toBe("virtualKeys.sharedBudgetOwner");
	});
});
const profile = (id: number, name: string, used: number, limit: number, extra: Partial<PayingProfile> = {}): PayingProfile => ({
	id,
	name,
	is_active: true,
	virtual_key_ids: ["vk-1"],
	budgets: [{ id: `b-${id}`, max_limit: limit, reset_duration: "1M", current_usage: used, last_reset: "2026-11-01T00:00:00Z" }],
	...extra,
});

describe("payingProfiles", () => {
	it("keeps the active, unexpired profiles that list the key", () => {
		const now = Date.parse("2026-11-27T00:00:00Z");
		const profiles = [
			profile(1, "Admin", 0, 500),
			profile(2, "Default", 0, 2000),
			profile(3, "Off", 0, 100, { is_active: false }),
			profile(4, "Old", 0, 100, { expires_at: "2026-11-01T00:00:00Z" }),
			profile(5, "Other key", 0, 100, { virtual_key_ids: ["vk-2"] }),
		];
		expect(payingProfiles(profiles, "vk-1", now).map((p) => p.name)).toEqual(["Admin", "Default"]);
		expect(payingProfiles(profiles, undefined, now)).toEqual([]);
	});
});

describe("profileBudgetLines", () => {
	it("labels each line with its profile when there are several", () => {
		const lines = profileBudgetLines([profile(1, "Admin", 500.01, 500), profile(2, "Default", 559.92, 2000)]);
		expect(lines.map((l) => [l.label, l.max_limit])).toEqual([
			["Admin", 500],
			["Default", 2000],
		]);
	});

	it("leaves a single profile's lines unlabelled", () => {
		expect(profileBudgetLines([profile(1, "Admin", 0, 500)])[0].label).toBeUndefined();
	});
});

describe("allProfilesSpent", () => {
	it("is false while another profile still has room", () => {
		expect(allProfilesSpent([profile(1, "Admin", 500.01, 500), profile(2, "Default", 559.92, 2000)])).toBe(false);
	});

	it("is true once every profile is used up", () => {
		expect(allProfilesSpent([profile(1, "Admin", 500.01, 500), profile(2, "Default", 2000, 2000)])).toBe(true);
	});

	it("counts a profile whose rate limit is used up as spent", () => {
		const limited = profile(2, "Default", 0, 2000, { rate_limit: { request_max_limit: 10, request_current_usage: 10 } });
		expect(allProfilesSpent([profile(1, "Admin", 500.01, 500), limited])).toBe(true);
	});

	it("is false with no profiles", () => {
		expect(allProfilesSpent([])).toBe(false);
	});
});
describe("summarizeProfileBudgets", () => {
	it("folds the reported case into one total that is not spent", () => {
		const summary = summarizeProfileBudgets([
			profile(1, "Admin", 500.01, 500),
			profile(2, "Default", 559.92, 2000),
			profile(3, "Free", 0, 0, { budgets: [] }),
		]);
		expect(summary.limit).toBe(2500);
		expect(summary.used).toBeCloseTo(1059.93);
		expect(summary.resetDuration).toBe("1M");
		expect(summary.anySpent).toBe(true);
		expect(summary.allSpent).toBe(false);
		expect(summary.lines.map((l) => [l.name, l.limit, l.spent])).toEqual([
			["Admin", 500, true],
			["Default", 2000, false],
			["Free", undefined, false],
		]);
	});

	it("uses each profile's budget with the least left and drops a period they do not share", () => {
		const twoBudgets = profile(1, "A", 0, 0, {
			budgets: [
				{ id: "m", max_limit: 1000, reset_duration: "1M", current_usage: 100, last_reset: "" },
				{ id: "d", max_limit: 50, reset_duration: "1d", current_usage: 40, last_reset: "" },
			],
		});
		const summary = summarizeProfileBudgets([twoBudgets, profile(2, "B", 0, 100)]);
		expect(summary.lines[0]).toMatchObject({ used: 40, limit: 50, resetDuration: "1d" });
		expect(summary.resetDuration).toBeUndefined();
	});
});
describe("profileBudgetBarClass", () => {
	const limited = (id: number, name: string) =>
		profile(id, name, 0, 100, { rate_limit: { request_max_limit: 10, request_current_usage: 10 } });

	it("tints the track red when every profile is spent with no budget used", () => {
		const summary = summarizeProfileBudgets([limited(1, "A"), limited(2, "B")]);
		expect(summary.used).toBe(0);
		expect(summary.allSpent).toBe(true);
		const cls = profileBudgetBarClass(summary).split(" ");
		expect(cls).toContain("bg-red-500/30");
		expect(cls).toContain("[&>div]:bg-red-500/70");
		expect(cls).not.toContain("dark:bg-muted/30");
	});

	it("tints the track amber when one profile is spent with no budget used", () => {
		const summary = summarizeProfileBudgets([limited(1, "A"), profile(2, "B", 0, 100)]);
		expect(summary.anySpent).toBe(true);
		expect(summary.allSpent).toBe(false);
		const cls = profileBudgetBarClass(summary).split(" ");
		expect(cls).toContain("bg-amber-500/30");
		expect(cls).toContain("[&>div]:bg-amber-500/70");
	});

	it("keeps the muted track while every profile can pay", () => {
		const cls = profileBudgetBarClass(summarizeProfileBudgets([profile(1, "A", 90, 100), profile(2, "B", 0, 100)])).split(" ");
		expect(cls).toEqual(["bg-muted/70", "dark:bg-muted/30", "[&>div]:bg-emerald-500/70"]);
	});
});