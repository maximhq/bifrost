import { supportsCalendarAlignment } from "@/lib/constants/governance";
import i18n from "@/lib/i18n";
import type { Budget, LimitScope, ModelConfig, RateLimit, RequestLimit, RequestLimitRequest } from "@/lib/types/governance";
import type { ParseKeys } from "i18next";

const RESET_UNIT_KEYS: Record<string, ParseKeys<"common">> = {
	s: "duration.second",
	m: "duration.minute",
	h: "duration.hour",
	d: "duration.day",
	w: "duration.week",
	M: "duration.month",
	Q: "duration.quarter",
	y: "duration.year",
};

/**
 * Parses a duration string (e.g., "1m", "5m", "1h", "1d", "1w", "1M") into human readable format
 */
export function parseResetPeriod(duration: string): string {
	if (!duration) return i18n.t("duration.unknown");

	const timeValue = parseInt(duration.slice(0, -1));
	const timeUnit = duration.slice(-1);

	const unitKey = RESET_UNIT_KEYS[timeUnit];
	if (!unitKey) return duration;

	return i18n.t(unitKey, { count: timeValue });
}

import { budgetOverrideFormSchema } from "@/lib/types/schemas";

import { formatCompactNumber } from "./numbers";

export function formatCurrency(dollars: number) {
	return `$${dollars.toFixed(2)}`;
}

export interface BudgetOverrideFields {
	max_limit: number;
	override_amount?: number;
	override_mode?: "cycles" | "forever";
	override_cycles_remaining?: number;
}

/** Returns whether a budget has a complete, currently active override. */
export function hasActiveBudgetOverride(budget: BudgetOverrideFields): boolean {
	if (!budget.override_amount || budget.override_amount <= 0) return false;
	return budget.override_mode === "forever" || (budget.override_mode === "cycles" && (budget.override_cycles_remaining ?? 0) > 0);
}

/** Returns the base limit plus the active additive override. */
export function getEffectiveBudgetLimit(budget: BudgetOverrideFields): number {
	return budget.max_limit + (hasActiveBudgetOverride(budget) ? (budget.override_amount ?? 0) : 0);
}

/** Validates the operator-entered override fields before sending them to the API. Delegates to budgetOverrideFormSchema. */
export function validateBudgetOverride(amount: number, mode: "cycles" | "forever", cycles: number): string | null {
	const result = budgetOverrideFormSchema.safeParse({ amount, mode, ...(mode === "cycles" ? { cycles } : {}) });
	return result.success ? null : (result.error.issues[0]?.message ?? i18n.t("validation.invalidInput"));
}

/**
 * Adds months in UTC, clamping to the target month's last day instead of letting
 * JS Date overflow spill into the following month (e.g. Jan 31 + 1M = Feb 28/29).
 */
function addUTCMonthsClamped(date: Date, months: number): void {
	const day = date.getUTCDate();
	date.setUTCDate(1);
	date.setUTCMonth(date.getUTCMonth() + months);
	const lastDay = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth() + 1, 0)).getUTCDate();
	date.setUTCDate(Math.min(day, lastDay));
}

/** Snaps a calendar-aligned reset timestamp to its UTC period boundary. */
function snapToCalendarPeriodStart(date: Date, unit: string): void {
	switch (unit) {
		case "d":
			date.setUTCHours(0, 0, 0, 0);
			break;
		case "w": {
			date.setUTCHours(0, 0, 0, 0);
			const daysFromMonday = (date.getUTCDay() + 6) % 7;
			date.setUTCDate(date.getUTCDate() - daysFromMonday);
			break;
		}
		case "M":
			date.setUTCDate(1);
			date.setUTCHours(0, 0, 0, 0);
			break;
		case "y":
		case "Y":
			date.setUTCMonth(0, 1);
			date.setUTCHours(0, 0, 0, 0);
			break;
	}
}

/** Calculates when a cycle-based override will expire on the budget's current reset schedule. */
export function getBudgetOverrideValidUntil(
	budget: Pick<BudgetOverrideFields, "max_limit"> & { last_reset: string; reset_duration: string },
	cycles: number,
	calendarAligned = false,
): Date | null {
	if (!Number.isSafeInteger(cycles) || cycles <= 0) return null;
	const match = /^(\d+)([smhdwMQyY])$/.exec(budget.reset_duration);
	const validUntil = new Date(budget.last_reset);
	if (!match || Number.isNaN(validUntil.getTime())) return null;

	const unit = match[2];
	calendarAligned = calendarAligned && supportsCalendarAlignment(budget.reset_duration);
	if (calendarAligned) snapToCalendarPeriodStart(validUntil, unit);
	const durationValue = Number(match[1]) * cycles;
	switch (unit) {
		case "s":
			validUntil.setTime(validUntil.getTime() + durationValue * 1000);
			break;
		case "m":
			validUntil.setTime(validUntil.getTime() + durationValue * 60 * 1000);
			break;
		case "h":
			validUntil.setTime(validUntil.getTime() + durationValue * 60 * 60 * 1000);
			break;
		case "d":
			validUntil.setUTCDate(validUntil.getUTCDate() + durationValue);
			break;
		case "w":
			validUntil.setUTCDate(validUntil.getUTCDate() + durationValue * 7);
			break;
		case "M":
			if (calendarAligned) {
				addUTCMonthsClamped(validUntil, durationValue);
			} else {
				validUntil.setTime(validUntil.getTime() + durationValue * 30 * 24 * 60 * 60 * 1000);
			}
			break;
		case "Q":
			// A calendar quarter is exactly three months; a rolling one is the 90-day
			// approximation ParseDuration uses on the Go side.
			if (calendarAligned) {
				addUTCMonthsClamped(validUntil, durationValue * 3);
			} else {
				validUntil.setTime(validUntil.getTime() + durationValue * 90 * 24 * 60 * 60 * 1000);
			}
			break;
		case "y":
		case "Y":
			if (calendarAligned) {
				addUTCMonthsClamped(validUntil, durationValue * 12);
			} else {
				validUntil.setTime(validUntil.getTime() + durationValue * 365 * 24 * 60 * 60 * 1000);
			}
			break;
	}
	return Number.isNaN(validUntil.getTime()) ? null : validUntil;
}

const shortDurationLabels: Record<string, string> = {
	"1m": "/min",
	"5m": "/5min",
	"15m": "/15min",
	"30m": "/30min",
	"1h": "/hr",
	"6h": "/6hr",
	"1d": "/day",
	"1w": "/wk",
	"1M": "/mo",
};

/**
 * Formats rate limit into compact display lines.
 * e.g. ["10K tokens/hr", "100 req/hr"]
 */
export function formatRateLimitLines(
	rateLimits:
		| {
				token_max_limit?: number | null;
				token_reset_duration?: string | null;
				request_max_limit?: number | null;
				request_reset_duration?: string | null;
		  }
		| null
		| undefined,
): string[] {
	if (!rateLimits) return [];
	const lines: string[] = [];
	if (rateLimits.token_max_limit != null) {
		const duration = rateLimits.token_reset_duration ?? "";
		const suffix = shortDurationLabels[duration] ?? (duration ? `/${duration}` : "");
		lines.push(`${formatCompactNumber(rateLimits.token_max_limit)} tokens${suffix}`);
	}
	if (rateLimits.request_max_limit != null) {
		const duration = rateLimits.request_reset_duration ?? "";
		const suffix = shortDurationLabels[duration] ?? (duration ? `/${duration}` : "");
		lines.push(`${formatCompactNumber(rateLimits.request_max_limit)} req${suffix}`);
	}
	return lines;
}

/**
 * Calculates usage percentage for rate limits
 */
export function calculateUsagePercentage(current: number, max: number): number {
	if (max === 0) return 0;
	return Math.round((current / max) * 100);
}

/**
 * Gets the appropriate variant for usage percentage badges
 */
export function getUsageVariant(percentage: number): "default" | "secondary" | "destructive" | "outline" {
	if (percentage >= 90) return "destructive";
	if (percentage >= 75) return "secondary";
	return "default";
}
/** A budget reduced to the fields that decide whether its reset window moved. */
export interface BudgetComparisonEntry {
	id?: string;
	max_limit?: number;
	reset_duration?: string;
	reset_config?: { quarter_start_month?: number };
	scope?: string;
	current_usage?: number;
}

/** Normalised fiscal quarter start; absent and an explicit January are the same window. */
export const quarterStartOf = (entry: Pick<BudgetComparisonEntry, "reset_config">) => entry.reset_config?.quarter_start_month || 1;

/**
 * A stable string describing every budget setting that moves a reset boundary.
 *
 * The fiscal quarter start belongs here for quarterly budgets: April and July
 * are the same limit on the same 1Q cadence and differ only in when the window
 * turns over, so a signature built from limit and duration alone reports "no
 * change" for precisely the edit that reschedules the reset. A non-default scope
 * is included too, so moving a budget between model and MCP traffic is a change;
 * the shared default adds nothing, keeping pre-scope signatures unchanged.
 */
export const budgetSignature = (budgets?: BudgetComparisonEntry[]) =>
	(budgets || [])
		.filter((budget) => budget.max_limit !== undefined)
		.map((budget) => {
			const quarterStart = budget.reset_duration?.endsWith("Q") ? quarterStartOf(budget) : "";
			const scope = budget.scope && budget.scope !== "shared" ? `:${budget.scope}` : "";
			return `${budget.id ?? ""}:${budget.max_limit}:${budget.reset_duration ?? ""}:${quarterStart}${scope}`;
		})
		.sort()
		.join("|");

/** One editable request limit, the shape MultiBudgetLines edits. */
export interface RequestLimitLine {
	id?: string;
	max_limit?: number;
	reset_duration: string;
	scope?: LimitScope;
}

interface RequestLimitHolder {
	rate_limit?: RateLimit | null;
	request_limits?: RequestLimit[] | null;
}

/**
 * A holder's request limits for display: its request_limits, preceded by its rate limit when that
 * row still carries a request limit (for example a legacy combined row).
 * Consumers read only the request fields of each entry.
 */
export function requestLimitsOf(holder: RequestLimitHolder | null | undefined): RequestLimit[] {
	if (!holder) return [];
	const legacy = holder.rate_limit?.request_max_limit != null ? [holder.rate_limit] : [];
	return [...legacy, ...(holder.request_limits ?? [])];
}

/** True when a request limit's counter has reached its maximum. */
export function isRequestLimitExhausted(rule: Pick<RateLimit, "request_max_limit" | "request_current_usage">): boolean {
	return rule.request_max_limit != null && (rule.request_current_usage ?? 0) >= rule.request_max_limit;
}

/**
 * A holder's request limits as editable lines. A legacy combined rate limit's request limit becomes
 * one shared line without an id. Saving request_limits makes the backend replace that row's request
 * fields for this holder (forking a row other holders share), and a new shared limit on the same
 * reset period inherits the old row's usage.
 */
export function requestLimitLinesOf(holder: RequestLimitHolder | null | undefined): RequestLimitLine[] {
	return requestLimitsOf(holder).map((rule) => ({
		id: rule === holder?.rate_limit ? undefined : rule.id,
		max_limit: rule.request_max_limit ?? undefined,
		reset_duration: rule.request_reset_duration || "1h",
		scope: rule === holder?.rate_limit ? undefined : rule.scope,
	}));
}

/** Request lines as a request_limits payload; lines without a maximum are dropped. */
export function toRequestLimitRequests(lines: RequestLimitLine[] | undefined): RequestLimitRequest[] {
	return (lines ?? [])
		.filter((line): line is RequestLimitLine & { max_limit: number } => line.max_limit !== undefined && line.max_limit !== null)
		.map((line) => ({
			...(line.id ? { id: line.id } : {}),
			max_limit: line.max_limit,
			reset_duration: line.reset_duration,
			...(line.scope ? { scope: line.scope } : {}),
		}));
}

// Callers name the entity in English; the sentence shows its localized noun when one is known.
const GOVERNANCE_ENTITY_KEYS: Record<string, ParseKeys<"common">> = {
	"virtual keys": "entities.virtualKeys",
	teams: "entities.teams",
	customers: "entities.customers",
	"model limits": "entities.modelLimits",
};

export type GovernanceAction = "create" | "edit" | "delete" | "rotate";

/**
 * Why a governance action is disabled, or undefined when it is allowed. A reason
 * that is not about permissions (e.g. the entity is managed elsewhere) wins, since
 * granting the permission would not unblock the action.
 */
export function actionDisabledReason(
	allowed: boolean,
	action: GovernanceAction,
	entityPlural: string,
	blockedReason?: string,
): string | undefined {
	if (blockedReason) return blockedReason;
	if (allowed) return undefined;
	const entityKey = GOVERNANCE_ENTITY_KEYS[entityPlural];
	return i18n.t(`permissionDenied.${action}`, { entity: entityKey ? i18n.t(entityKey) : entityPlural });
}
/**
 * The traffic a Budgets & Limits page edits on a holder-level row: the Models page edits the lines
 * model requests are charged by, the MCP page the lines MCP tool executions are charged by.
 */
export type LimitTraffic = "model" | "mcp";

/** Whether a budget of this scope is charged by the given traffic; an unset scope is Shared. */
export function budgetScopeChargedBy(scope: LimitScope | undefined, traffic: LimitTraffic): boolean {
	const value = scope || "shared";
	return value === "shared" || value === traffic;
}

/**
 * A holder-level model config: all models with no provider. It holds the deployment's or a virtual
 * key's own limits, the only lines that may be scoped to Model or MCP traffic.
 */
export function isHolderLevelModelConfig(mc: Pick<ModelConfig, "model_name" | "provider">): boolean {
	return mc.model_name === "*" && !mc.provider;
}

/** Whether MCP tool executions are charged by a model config; mirrors the API's applies_to=mcp listing. */
export function modelConfigAppliesToMCP(mc: ModelConfig): boolean {
	return (
		isHolderLevelModelConfig(mc) &&
		[...(mc.budgets ?? []), ...(mc.request_limits ?? [])].some((line) => budgetScopeChargedBy(line.scope, "mcp"))
	);
}

/**
 * What removing one page's limits from a model config leaves on it, or null when the row is to be
 * deleted whole. On a holder-level row the lines the other page edits stay: from the MCP page the
 * Model lines and the rate limit, from the Models page the MCP lines. Shared lines are on both
 * pages, so either removes them. Any other row belongs to the Models page alone.
 */
export function budgetsLeftAfterRemoving(mc: ModelConfig, traffic: LimitTraffic): Budget[] | null {
	if (!isHolderLevelModelConfig(mc)) return null;
	const left = (mc.budgets ?? []).filter((b) => !budgetScopeChargedBy(b.scope, traffic));
	const keepsRateLimit = traffic === "mcp" && !!mc.rate_limit;
	return left.length > 0 || keepsRateLimit ? left : null;
}