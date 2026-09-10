import { RateLimit } from "@/lib/types/governance";

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
	profileRateLimit,
	vkRateLimit,
}: {
	isManagedByProfile: boolean;
	profileRateLimit: ProfileRateLimit;
	vkRateLimit: RateLimit | undefined;
}): RateLimit | undefined {
	if (!isManagedByProfile) return vkRateLimit;
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
