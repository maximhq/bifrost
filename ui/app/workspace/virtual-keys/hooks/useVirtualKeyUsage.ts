import { Budget, RateLimit, VirtualKey } from "@/lib/types/governance";
import { getEffectiveBudgetLimit } from "@/lib/utils/governance";
import { useGetUserAccessProfilesQuery } from "@enterprise/lib/store/apis/accessProfileApi";
import { useGetVirtualKeyUsersQuery } from "@enterprise/lib/store/apis/virtualKeyUsersApi";
import { UserAccessProfile } from "@enterprise/lib/types/accessProfile";
import { User } from "@enterprise/lib/types/user";
import { hasProfileRateLimit, resolveDisplayRateLimit } from "./useVirtualKeyUsage.utils";

/**
 * The assignee fields every caller actually reads. Narrower than User on purpose: the
 * VK list resolves assignees itself and carries only id/name/email, so widening this
 * to User would promise a role, teams and claims that are not there.
 */
export type VirtualKeyAssignee = Pick<User, "id" | "name" | "email">;

/**
 * When a VK is attached to users via an access profile, the governance plugin tracks usage on the
 * AP rather than on the VK itself (to avoid double-counting). This hook resolves the managing AP
 * for a VK and returns budget/rate-limit values that prefer AP counters, falling back to the VK's
 * own when there is no managing profile.
 *
 * The AP query polls every 5s so bars reflect live usage without manual refresh.
 *
 * The assignee comes off the VK itself (`assigned_user`), which the governance read paths
 * resolve for a whole page at a time. The per-VK /users request is only a fallback for rows
 * that came from a path which does not populate that field, so the common case costs no extra
 * request per row. Either source yields at most one user: the enterprise schema enforces a
 * uniqueIndex on TableVirtualKeyUser.virtual_key_id.
 */
export function useVirtualKeyUsage(vk: VirtualKey | null | undefined): {
	assignedUsers: VirtualKeyAssignee[];
	isManagedByProfile: boolean;
	managingProfile: UserAccessProfile | undefined;
	hasApRateLimit: boolean;
	displayBudgets: Budget[] | undefined;
	displayRateLimit: RateLimit | undefined;
	isExhausted: boolean;
} {
	// `assigned_user` present (user or null) means the response resolved assignees, so
	// there is nothing left to fetch. Only an absent field falls back to the request.
	const hasResolvedAssignee = vk ? vk.assigned_user !== undefined : false;
	const { data: vkUsersData } = useGetVirtualKeyUsersQuery(vk?.id ?? "", { skip: !vk?.id || hasResolvedAssignee });
	const assignedUsers: VirtualKeyAssignee[] = hasResolvedAssignee
		? vk?.assigned_user
			? [vk.assigned_user]
			: []
		: (vkUsersData?.users ?? []);

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
		isEntityGoverned: assignedUsers.length === 0 && !!(vk?.team_id || vk?.customer_id || vk?.business_unit_id),
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