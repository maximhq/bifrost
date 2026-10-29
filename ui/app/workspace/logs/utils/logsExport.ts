/**
 * Utilities for exporting filtered logs to CSV.
 */

import { logAppDisplayName, mapAppToClientApp, mapUserAgentToApp } from "@/lib/constants/logs";
import type { LogEntry, LogFilters } from "@/lib/types/logs";
import { buildCSV } from "@/lib/utils/csv";
import { getRangeForPeriod } from "@/lib/utils/timeRange";

export const LOGS_CSV_HEADERS = [
	"Time",
	"Request ID",
	"Status",
	"Type",
	"Provider",
	"Model",
	"Canonical Model",
	"App",
	"Latency (ms)",
	"Prompt Tokens",
	"Completion Tokens",
	"Total Tokens",
	"Cost ($)",
	"Session ID",
	"Service Tier",
	"Virtual Key",
	"Routing Rule",
	"Team",
	"Customer",
	"User",
	"Business Unit",
	"Project",
	"Stop Reason",
];

/**
 * Extract display client application from log row.
 *
 * @param log The LogEntry to extract app display name from.
 * @returns Human-readable application name string.
 */
export function getLogApp(log: LogEntry): string {
	if (log.app) {
		const app = mapAppToClientApp(log.app);
		return logAppDisplayName(app, log.user_agent);
	}
	if (log.user_agent) {
		const app = mapUserAgentToApp(log.user_agent);
		return logAppDisplayName(app, log.user_agent);
	}
	return "";
}

/**
 * Extract token usage accounting, falling back to batch accounting if needed.
 *
 * @param log The LogEntry containing token usage or batch debug breakdowns.
 * @returns Object with promptTokens, completionTokens, and totalTokens.
 */
export function getLogTokenUsage(log: LogEntry): {
	promptTokens: number | "";
	completionTokens: number | "";
	totalTokens: number | "";
} {
	if (log.token_usage) {
		return {
			promptTokens: log.token_usage.prompt_tokens ?? "",
			completionTokens: log.token_usage.completion_tokens ?? "",
			totalTokens: log.token_usage.total_tokens ?? "",
		};
	}

	const breakdowns = log.batch_debug?.accounting?.model_breakdowns;
	if (breakdowns) {
		let prompt = 0;
		let completion = 0;
		let total = 0;
		let hasEntries = false;
		for (const entry of Object.values(breakdowns)) {
			hasEntries = true;
			prompt += entry.usage?.prompt_tokens ?? 0;
			completion += entry.usage?.completion_tokens ?? 0;
			total += entry.usage?.total_tokens ?? 0;
		}
		if (hasEntries && total > 0) {
			return {
				promptTokens: prompt,
				completionTokens: completion,
				totalTokens: total,
			};
		}
	}

	return {
		promptTokens: "",
		completionTokens: "",
		totalTokens: "",
	};
}

/**
 * Extract cost for log, checking settled batch or child cost if top-level cost is null.
 *
 * @param log The LogEntry to extract cost from.
 * @returns Numeric cost or empty string if unset.
 */
export function getLogCost(log: LogEntry): number | "" {
	if (log.cost != null) {
		return log.cost;
	}
	if (log.batch_debug?.accounting?.cost != null) {
		return log.batch_debug.accounting.cost;
	}
	if (log.children_cost != null && log.children_cost > 0) {
		return log.children_cost;
	}
	return "";
}

/**
 * Convert a single LogEntry into an array of cell values matching LOGS_CSV_HEADERS.
 *
 * @param log The LogEntry to convert into CSV cells.
 * @returns Array of cell values in header order.
 */
export function logEntryToCSVRow(log: LogEntry): unknown[] {
	const app = getLogApp(log);
	const { promptTokens, completionTokens, totalTokens } = getLogTokenUsage(log);
	const cost = getLogCost(log);

	const virtualKey = log.virtual_key_name || log.virtual_key?.name || log.virtual_key_id || "";
	const routingRule = log.routing_rule_name || log.routing_rule?.name || log.routing_rule_id || "";
	const team =
		log.team_name ||
		(log.team_names && log.team_names.length > 0
			? log.team_names.join("; ")
			: log.team_id || (log.team_ids ? log.team_ids.join("; ") : ""));
	const customer =
		log.customer_name ||
		(log.customer_names && log.customer_names.length > 0
			? log.customer_names.join("; ")
			: log.customer_id || (log.customer_ids ? log.customer_ids.join("; ") : ""));
	const user = log.user_name || log.user_id || "";
	const businessUnit =
		log.business_unit_name ||
		(log.business_unit_names && log.business_unit_names.length > 0
			? log.business_unit_names.join("; ")
			: log.business_unit_id || (log.business_unit_ids ? log.business_unit_ids.join("; ") : ""));
	const project = log.project_name || log.project_id || "";

	return [
		log.timestamp || "",
		log.id || "",
		log.status || "",
		log.object || "",
		log.provider || "",
		log.model || "",
		log.canonical_model_name || "",
		app,
		log.latency != null ? log.latency : "",
		promptTokens,
		completionTokens,
		totalTokens,
		cost,
		log.session_id || "",
		log.service_tier || "",
		virtualKey,
		routingRule,
		team,
		customer,
		user,
		businessUnit,
		project,
		log.stop_reason || "",
	];
}

export const DEFAULT_MAX_EXPORT_LIMIT = 10000;
export const BATCH_PAGE_SIZE = 1000;

/**
 * Calculate the target count of logs to export given total available logs and an optional user limit.
 *
 * @param totalLogs Total number of logs available matching current filters.
 * @param userCap Optional user-provided limit override.
 * @param maxCap Hard maximum limit allowed (defaults to 10,000).
 * @returns Bounded target total count to fetch.
 */
export function calculateExportTargetCount(totalLogs: number, userCap?: number, maxCap = DEFAULT_MAX_EXPORT_LIMIT): number {
	const effectiveCap = userCap && userCap > 0 ? Math.min(userCap, maxCap) : maxCap;
	return totalLogs > 0 ? Math.min(effectiveCap, totalLogs) : 0;
}

/**
 * Format a human-readable list of active filters for display in the export dialog.
 *
 * @param filters Current active LogFilters object.
 * @returns Array of descriptive filter summary strings.
 */
export function formatActiveFiltersSummary(filters: LogFilters): string[] {
	const summary: string[] = [];
	if (filters.content_search) summary.push(`search "${filters.content_search}"`);
	if (filters.request_id) summary.push(`request "${filters.request_id}"`);
	if (filters.providers && filters.providers.length > 0) summary.push(`providers (${filters.providers.length})`);
	if (filters.models && filters.models.length > 0) summary.push(`models (${filters.models.length})`);
	if (filters.status && filters.status.length > 0) summary.push(`status: ${filters.status.join(", ")}`);
	if (filters.session_id) summary.push(`session "${filters.session_id}"`);
	if (filters.virtual_key_ids && filters.virtual_key_ids.length > 0) summary.push(`virtual keys (${filters.virtual_key_ids.length})`);
	if (filters.team_ids && filters.team_ids.length > 0) summary.push(`teams (${filters.team_ids.length})`);
	if (filters.customer_ids && filters.customer_ids.length > 0) summary.push(`customers (${filters.customer_ids.length})`);
	if (filters.period) summary.push(`period ${filters.period}`);
	return summary;
}

/**
 * Convert an array of LogEntry objects into a CSV string.
 *
 * @param logs Array of LogEntry records to serialize.
 * @returns RFC 4180 compliant CSV string with headers.
 */
export function logsToCSV(logs: LogEntry[]): string {
	const rows = logs.map(logEntryToCSVRow);
	return buildCSV(LOGS_CSV_HEADERS, rows);
}

/**
 * Normalizes log filters for export by converting any relative period into fixed start_time and end_time timestamps.
 * This prevents pagination drift across batches when new logs arrive mid-export.
 *
 * @param filters The active LogFilters.
 * @param timezone Optional user timezone (defaults to preferred timezone).
 * @returns Cloned LogFilters with fixed start_time and end_time and period cleared, or original filters if no period.
 */
export function normalizeExportFilters(filters: LogFilters, timezone?: string): LogFilters {
	if (!filters.period) {
		return filters;
	}
	const { from, to } = getRangeForPeriod(filters.period, timezone);
	return {
		...filters,
		period: undefined,
		start_time: from.toISOString(),
		end_time: to.toISOString(),
	};
}

/**
 * Options for batch log collection.
 */
export interface CollectLogsOptions<T extends { id?: string } = LogEntry> {
	targetTotal: number;
	batchSize?: number;
	fetchBatch: (offset: number, limit: number) => Promise<T[]>;
	onProgress?: (collectedCount: number, targetTotal: number) => void;
	isCancelled?: () => boolean;
}

/**
 * Collect logs across multiple paginated requests until the target count or end of logs is reached.
 * Deduplicates log records across batch boundaries by ID as a backstop against shifting offsets.
 * Generic over the row type so the relay export (relay-logs/views/relayLogsExportDialog.utils.ts)
 * shares the batching; it defaults to LogEntry for the LLM page.
 *
 * @param options Batch collection configuration including fetch function and cancellation check.
 * @returns Array of collected LogEntry items.
 */
export async function collectLogsInBatches<T extends { id?: string } = LogEntry>(options: CollectLogsOptions<T>): Promise<T[]> {
	const { targetTotal, batchSize = BATCH_PAGE_SIZE, fetchBatch, onProgress, isCancelled } = options;
	const collected: T[] = [];
	const seenIds = new Set<string>();
	let offset = 0;

	while (collected.length < targetTotal && !isCancelled?.()) {
		const limit = Math.min(batchSize, targetTotal - collected.length);
		const batch = await fetchBatch(offset, limit);
		if (isCancelled?.()) break;

		if (batch.length === 0) {
			break;
		}

		for (const log of batch) {
			if (log.id) {
				if (seenIds.has(log.id)) {
					continue;
				}
				seenIds.add(log.id);
			}
			collected.push(log);
			if (collected.length >= targetTotal) {
				break;
			}
		}

		onProgress?.(collected.length, targetTotal);

		// If server returned fewer items than requested, we reached the end of results
		if (batch.length < limit) {
			break;
		}

		offset += batch.length;
	}

	return collected;
}