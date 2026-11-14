import { RateLimit, VirtualKey } from "@/lib/types/governance";

/** The subset of an access profile's rate limit this derivation needs. */
export type ProfileRateLimit = Partial<RateLimit> | undefined;

export function hasProfileRateLimit(apRL: ProfileRateLimit): apRL is Partial<RateLimit> {
	return !!(apRL && (apRL.token_max_limit != null || apRL.request_max_limit != null));
}

/**
 * Which rate limit the VK screens should display.
 *
 * A profile-managed VK is governed by its access profile's counters, never its own, so the
 * raw `vk.rate_limit` must not surface for one - showing it would contradict the locked
 * edit/delete UX and advertise limits that do not actually govern the key.
 *
 * `isManagedByProfile` (the server-computed `is_access_profile_managed`) is the source of
 * truth for *whether* the key is managed; `managingProfile` only resolves *which* profile,
 * and is undefined for callers without AccessProfiles:View. So a managed key whose profile
 * the caller cannot see yields `undefined` - no limit shown - rather than falling back.
 */
export function resolveDisplayRateLimit({
	isManagedByProfile,
	isEntityGoverned = false,
	profileRateLimit,
	vkRateLimit,
}: {
	isManagedByProfile: boolean;
	/**
	 * The key is owned by a team, business unit or customer and assigned to no user, so the profile
	 * governing it is the owning entity's. No user profile resolves for it, and the server already
	 * returns that entity profile's rate limit as `vk.rate_limit` on the admin read paths.
	 */
	isEntityGoverned?: boolean;
	profileRateLimit: ProfileRateLimit;
	vkRateLimit: RateLimit | undefined;
}): RateLimit | undefined {
	if (!isManagedByProfile || isEntityGoverned) return vkRateLimit;
	if (!hasProfileRateLimit(profileRateLimit)) return undefined;
	return {
		id: "",
		token_max_limit: profileRateLimit.token_max_limit,
		token_reset_duration: profileRateLimit.token_reset_duration,
		token_current_usage: profileRateLimit.token_current_usage ?? 0,
		token_last_reset: profileRateLimit.token_last_reset ?? "",
		request_max_limit: profileRateLimit.request_max_limit,
		request_reset_duration: profileRateLimit.request_reset_duration,
		request_current_usage: profileRateLimit.request_current_usage ?? 0,
		request_last_reset: profileRateLimit.request_last_reset ?? "",
	};
}

/**
 * How an access profile manages a virtual key, from the server-computed `is_access_profile_managed`
 * and the key's owner.
 *
 * The server never attaches a key a team, business unit or customer owns to a user, so a managed
 * key with an owner is governed through that owner's profile: it is the owner's key, edited,
 * unassigned and deleted like any of its keys, and only its providers, budgets, rate limit and MCP
 * access are the profile's. A managed key with no owner is a user's managed key, locked except for
 * its name and description.
 */
export function virtualKeyManagement(
	vk: Pick<VirtualKey, "is_access_profile_managed" | "team_id" | "customer_id" | "business_unit_id"> | null | undefined,
): { isOwnerGoverned: boolean; isUserProfileManaged: boolean } {
	const managed = vk?.is_access_profile_managed ?? false;
	const owned = !!(vk?.team_id || vk?.customer_id || vk?.business_unit_id);
	return { isOwnerGoverned: managed && owned, isUserProfileManaged: managed && !owned };
}