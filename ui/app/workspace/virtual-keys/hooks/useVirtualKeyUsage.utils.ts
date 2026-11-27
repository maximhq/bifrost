import { Budget, RateLimit, VirtualKey } from "@/lib/types/governance";
import { getEffectiveBudgetLimit } from "@/lib/utils/governance";
import type { TFunction } from "i18next";

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
/**
 * Who shares a managed key's budget, for the "shared" note: each assignee holds their own copy of
 * the profile, and an owner-governed key shares its team, business unit or customer's profile.
 */
export function sharedBudgetNote(
	t: TFunction<"models">,
	{
		isManagedByProfile,
		isOwnerGoverned,
		assignee,
	}: { isManagedByProfile: boolean; isOwnerGoverned: boolean; assignee?: { name?: string; email?: string } },
): string | undefined {
	if (!isManagedByProfile) return undefined;
	if (isOwnerGoverned) return t("virtualKeys.sharedBudgetOwner");
	const name = assignee?.name || assignee?.email;
	return name ? t("virtualKeys.sharedBudgetUser", { name }) : t("virtualKeys.sharedBudgetUserUnnamed");
}
type ProfileBudget = Omit<Budget, "reset_config"> & { reset_config?: Budget["reset_config"] };

/** The parts of a user's access profile that decide whether it can still pay for a key. */
export interface PayingProfile {
	id: number;
	name: string;
	is_active: boolean;
	expires_at?: string;
	virtual_key_ids?: string[];
	budgets?: ProfileBudget[];
	rate_limit?: ProfileRateLimit;
}

/** The active, unexpired profiles that list the key; the gateway charges each request to one of them. */
export function payingProfiles<P extends PayingProfile>(profiles: P[], vkId: string | undefined, now = Date.now()): P[] {
	if (!vkId) return [];
	return profiles.filter(
		(p) => p.virtual_key_ids?.includes(vkId) && p.is_active && !(p.expires_at && new Date(p.expires_at).getTime() <= now),
	);
}

/** Every profile's budget lines, labelled with the profile's name when there is more than one profile. */
export function profileBudgetLines(profiles: PayingProfile[]): (Budget & { label?: string })[] {
	const labelled = profiles.length > 1;
	return profiles.flatMap((p) =>
		(p.budgets ?? []).map((line) => ({
			id: line.id,
			max_limit: line.max_limit,
			reset_duration: line.reset_duration,
			reset_config: line.reset_config,
			current_usage: line.current_usage,
			last_reset: line.last_reset,
			override_amount: line.override_amount,
			override_mode: line.override_mode,
			override_cycles_remaining: line.override_cycles_remaining,
			...(labelled ? { label: p.name } : {}),
		})),
	);
}

function rateLimitSpent(rl: ProfileRateLimit): boolean {
	if (!rl) return false;
	const tokens = rl.token_max_limit != null && (rl.token_current_usage ?? 0) >= rl.token_max_limit;
	const requests = rl.request_max_limit != null && (rl.request_current_usage ?? 0) >= rl.request_max_limit;
	return tokens || requests;
}

/** A profile that can pay for nothing: one of its budgets or its rate limit is used up. */
export function profileSpent(p: PayingProfile): boolean {
	return (p.budgets ?? []).some((b) => b.current_usage >= getEffectiveBudgetLimit(b)) || rateLimitSpent(p.rate_limit);
}

/** A key is out only when none of its profiles can pay, since the gateway moves to another profile when one is used up. */
export function allProfilesSpent(profiles: PayingProfile[]): boolean {
	return profiles.length > 0 && profiles.every(profileSpent);
}
/** One profile's line in the multi-profile hover: its budget with the least left, or none. */
export interface ProfileBudgetLine {
	name: string;
	used: number;
	limit?: number;
	resetDuration?: string;
	spent: boolean;
}

/** A key's profiles folded into one bar: their tightest budgets summed, plus a line per profile. */
export interface ProfileBudgetSummary {
	used: number;
	limit: number;
	/** Set when every budgeted profile resets on the same period. */
	resetDuration?: string;
	anySpent: boolean;
	allSpent: boolean;
	lines: ProfileBudgetLine[];
}

export function summarizeProfileBudgets(profiles: PayingProfile[]): ProfileBudgetSummary {
	const lines = profiles.map((p): ProfileBudgetLine => {
		const tightest = (p.budgets ?? []).reduce<ProfileBudget | undefined>(
			(best, b) => (!best || getEffectiveBudgetLimit(b) - b.current_usage < getEffectiveBudgetLimit(best) - best.current_usage ? b : best),
			undefined,
		);
		return {
			name: p.name,
			used: tightest?.current_usage ?? 0,
			limit: tightest ? getEffectiveBudgetLimit(tightest) : undefined,
			resetDuration: tightest?.reset_duration,
			spent: profileSpent(p),
		};
	});
	const budgeted = lines.filter((l) => l.limit !== undefined);
	const periods = new Set(budgeted.map((l) => l.resetDuration));
	return {
		used: budgeted.reduce((sum, l) => sum + l.used, 0),
		limit: budgeted.reduce((sum, l) => sum + (l.limit ?? 0), 0),
		resetDuration: periods.size === 1 ? budgeted[0].resetDuration : undefined,
		anySpent: lines.some((l) => l.spent),
		allSpent: allProfilesSpent(profiles),
		lines,
	};
}

/** How full the summary bar is, as a percentage of the summed limit. */
export function profileBudgetBarPercent(summary: Pick<ProfileBudgetSummary, "used" | "limit">): number {
	return summary.limit > 0 ? Math.min((summary.used / summary.limit) * 100, 100) : 0;
}

/**
 * The summary bar's track and fill colours: red once no profile can pay, amber once one cannot or
 * the total is past 80%. A profile can be spent through its rate limit with no budget used, which
 * leaves the fill empty, so a spent state tints the track as well.
 */
export function profileBudgetBarClass(summary: Pick<ProfileBudgetSummary, "used" | "limit" | "anySpent" | "allSpent">): string {
	if (summary.allSpent) return "bg-red-500/30 [&>div]:bg-red-500/70";
	if (summary.anySpent) return "bg-amber-500/30 [&>div]:bg-amber-500/70";
	const fill = profileBudgetBarPercent(summary) > 80 ? "[&>div]:bg-amber-500/70" : "[&>div]:bg-emerald-500/70";
	return `bg-muted/70 dark:bg-muted/30 ${fill}`;
}