import { Budget, RateLimit, VirtualKey } from "@/lib/types/governance";
import { getEffectiveBudgetLimit } from "@/lib/utils/governance";
import { useGetUserAccessProfilesQuery } from "@enterprise/lib/store/apis/accessProfileApi";
import { useGetVirtualKeyUsersQuery } from "@enterprise/lib/store/apis/virtualKeyUsersApi";
import { UserAccessProfile } from "@enterprise/lib/types/accessProfile";
import { User } from "@enterprise/lib/types/user";
import { hasProfileRateLimit, resolveDisplayRateLimit } from "./useVirtualKeyUsage.utils";

/**
 * When a VK is attached to users via an access profile, the governance plugin tracks usage on the
 * AP rather than on the VK itself (to avoid double-counting). This hook resolves the managing AP
 * for a VK and returns budget/rate-limit values that prefer AP counters, falling back to the VK's
 * own when there is no managing profile.
 *
 * The AP query polls every 5s so bars reflect live usage without manual refresh.
 *
 * `assignedUsers[0]` is safe: the enterprise schema enforces a uniqueIndex on
 * TableVirtualKeyUser.virtual_key_id, so each VK can belong to at most one user. The list is
 * either empty or length 1 — [0] is always the sole assignee when one exists.
 */
export function useVirtualKeyUsage(vk: VirtualKey | null | undefined): {
	assignedUsers: User[];
	isManagedByProfile: boolean;
	managingProfile: UserAccessProfile | undefined;
	hasApRateLimit: boolean;
	displayBudgets: Budget[] | undefined;
	displayRateLimit: RateLimit | undefined;
	isExhausted: boolean;
} {
	const { data: vkUsersData } = useGetVirtualKeyUsersQuery(vk?.id ?? "", { skip: !vk?.id });
	const assignedUsers = vkUsersData?.users ?? [];

	const managingUserId = assignedUsers[0]?.id;
	const { data: userAPsData } = useGetUserAccessProfilesQuery(managingUserId ?? "", {
		skip: !managingUserId,
		pollingInterval: managingUserId ? 5000 : 0,
	});
	const userAPs = userAPsData?.access_profiles ?? [];

	// Only treat the VK as AP-managed when an AP explicitly lists this VK in its virtual_key_ids.
	// No fallback to "first active" / "first AP" — that misattributed budgets in multi-AP setups.
	const managingProfile = vk ? userAPs.find((p) => p.virtual_key_ids?.includes(vk.id)) : undefined;
	// The server-computed flag is the sole source of truth for "is this managed": the
	// access-profile call above is RBAC-gated and 403s for callers without AccessProfiles:View,
	// which used to leave the VK looking unmanaged (and editable) here. managingProfile is now
	// display-only — it resolves the profile name/actions when the caller can view access
	// profiles; without that permission the VK is still locked, just without the profile name.
	const isManagedByProfile = vk?.is_access_profile_managed ?? false;

	const displayBudgets: Budget[] | undefined = managingProfile
		? (managingProfile.budgets ?? []).map((line) => ({
				id: line.id,
				max_limit: line.max_limit,
				reset_duration: line.reset_duration,
				current_usage: line.current_usage,
				last_reset: line.last_reset,
				override_amount: line.override_amount,
				override_mode: line.override_mode,
				override_cycles_remaining: line.override_cycles_remaining,
			}))
		: vk?.budgets;

	const apRL = managingProfile?.rate_limit;
	const hasApRateLimit = hasProfileRateLimit(apRL);
	// When profile-managed, never fall back to raw VK rate limits (that would contradict the
	// locked edit/delete UX). If the profile has no rate limit - or the caller cannot see the
	// profile at all - displayRateLimit is undefined. Keyed off the server flag, not
	// managingProfile, for the same reason isManagedByProfile is: see resolveDisplayRateLimit.
	const displayRateLimit: RateLimit | undefined = resolveDisplayRateLimit({
		isManagedByProfile,
		profileRateLimit: apRL,
		vkRateLimit: vk?.rate_limit,
	});

	const isExhausted =
		(displayBudgets?.some((b) => b.current_usage >= getEffectiveBudgetLimit(b)) ?? false) ||
		(displayRateLimit?.token_current_usage != null &&
			displayRateLimit?.token_max_limit != null &&
			displayRateLimit.token_current_usage >= displayRateLimit.token_max_limit) ||
		(displayRateLimit?.request_current_usage != null &&
			displayRateLimit?.request_max_limit != null &&
			displayRateLimit.request_current_usage >= displayRateLimit.request_max_limit);

	return { assignedUsers, isManagedByProfile, managingProfile, hasApRateLimit, displayBudgets, displayRateLimit, isExhausted };
}