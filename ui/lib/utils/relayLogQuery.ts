import type { RelayLogFilters, RelayLogPagination } from "@/lib/types/relays";
import { getRangeForPeriod } from "@/lib/utils/timeRange";

/** Filters sent as repeated query params, one value each. */
export const RELAY_LOG_MULTI_FILTER_KEYS = [
	"relay_names",
	"methods",
	"statuses",
	"kinds",
	"virtual_key_ids",
	"user_ids",
	"team_ids",
	"customer_ids",
	"business_unit_ids",
	"project_ids",
	"apps",
] as const;

/**
 * Builds the query string shared by the relay log list, stats and histogram requests so all three
 * narrow to the same rows. A relative period is expanded to a fresh window per call, so polling
 * always queries a rolling window.
 */
export function buildRelayLogQuery(filters: RelayLogFilters, pagination?: RelayLogPagination): string {
	const params = new URLSearchParams();
	if (pagination) {
		params.set("limit", String(pagination.limit));
		params.set("offset", String(pagination.offset));
		params.set("sort_by", pagination.sort_by);
		params.set("order", pagination.order);
	}
	for (const key of RELAY_LOG_MULTI_FILTER_KEYS) {
		for (const value of filters[key] ?? []) {
			if (value) params.append(key, value);
		}
	}
	if (filters.billable !== undefined) params.set("billable", String(filters.billable));
	// 0 is a real bound (max_cost=0 is "free calls only"), so only undefined and NaN mean unset.
	if (filters.min_cost !== undefined && Number.isFinite(filters.min_cost)) params.set("min_cost", String(filters.min_cost));
	if (filters.max_cost !== undefined && Number.isFinite(filters.max_cost)) params.set("max_cost", String(filters.max_cost));
	if (filters.request_id) params.set("request_id", filters.request_id);
	if (filters.search) params.set("search", filters.search);
	if (filters.period) {
		const { from, to } = getRangeForPeriod(filters.period);
		params.set("start_time", from.toISOString());
		params.set("end_time", to.toISOString());
	} else if (filters.start_time && filters.end_time) {
		params.set("start_time", filters.start_time);
		params.set("end_time", filters.end_time);
	}
	return params.toString();
}

/**
 * Turns the text of a cost bound input into the filter value: undefined for empty or
 * unparsable text and for negative numbers (a cost is never below zero), else the number.
 */
export function parseCostBound(raw: string): number | undefined {
	const trimmed = raw.trim();
	if (trimmed === "") return undefined;
	const n = Number(trimmed);
	if (!Number.isFinite(n) || n < 0) return undefined;
	return n;
}

/** Text for a cost bound input: an unset bound is an empty field. */
export function formatCostBound(value: number | undefined): string {
	return value === undefined || !Number.isFinite(value) ? "" : String(value);
}