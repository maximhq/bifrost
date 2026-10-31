/**
 * Pure helpers for the relay logs CSV export, kept apart from the dialog so they can be
 * unit tested without the store. Batching and the size cap are shared with the LLM export
 * (app/workspace/logs/utils/logsExport.ts).
 */

import { logAppDisplayName, mapAppToClientApp, mapUserAgentToApp } from "@/lib/constants/logs";
import type { RelayLog, RelayLogFilters } from "@/lib/types/relays";
import { buildCSV } from "@/lib/utils/csv";
import { getRangeForPeriod } from "@/lib/utils/timeRange";

// The column names and their order follow the LLM logs export (LOGS_CSV_HEADERS): time and request
// id first, the call, then the client, timing, usage and cost, and the attribution last.
export const RELAY_LOGS_CSV_HEADERS = [
	"Time",
	"Request ID",
	"Status",
	"Relay",
	"Kind",
	"Method",
	"Path",
	"Route",
	"Model",
	"App",
	"User Agent",
	"Latency (ms)",
	"Upstream Latency (ms)",
	"Overhead (ms)",
	"Bytes Out",
	"Billable",
	"Streamed",
	"Units",
	"Billed Units",
	"Cost per Request ($)",
	"Cost per Unit ($)",
	"Reported Cost ($)",
	"Cost ($)",
	"Input Tokens",
	"Output Tokens",
	"Total Tokens",
	"Virtual Key",
	"Team",
	"Customer",
	"User",
	"Business Unit",
	"Project",
	"Job ID",
] as const;

/** The client app for a row, resolved the way the table's App column resolves it. */
export function getRelayLogApp(log: RelayLog): string {
	if (log.app) return logAppDisplayName(mapAppToClientApp(log.app), log.user_agent);
	if (log.user_agent) return logAppDisplayName(mapUserAgentToApp(log.user_agent), log.user_agent);
	return "";
}

/** An optional number as a CSV cell: the number, or an empty cell when the row has none. */
function cell(value: number | undefined | null): number | "" {
	return value == null ? "" : value;
}

/** One RelayLog as a row of cells in RELAY_LOGS_CSV_HEADERS order. Bodies and headers are never exported. */
export function relayLogToCSVRow(log: RelayLog): unknown[] {
	return [
		log.timestamp || "",
		log.request_id || "",
		log.status || "",
		log.relay_name || "",
		log.kind || "",
		log.method || "",
		log.path || "",
		log.route_path || "",
		log.model || "",
		getRelayLogApp(log),
		log.user_agent || "",
		cell(log.latency),
		cell(log.upstream_latency),
		cell(log.overhead_latency),
		cell(log.bytes_out),
		log.billable ? "true" : "false",
		log.streamed ? "true" : "false",
		cell(log.units),
		cell(log.billed_units),
		cell(log.cost_per_request),
		cell(log.cost_per_unit),
		cell(log.extracted_cost),
		cell(log.cost),
		cell(log.input_tokens),
		cell(log.output_tokens),
		cell(log.total_tokens),
		log.virtual_key_name || log.virtual_key_id || "",
		log.team_name || log.team_id || "",
		log.customer_name || log.customer_id || "",
		log.user_name || log.user_id || "",
		log.business_unit_name || log.business_unit_id || "",
		log.project_name || log.project_id || "",
		log.job_id || "",
	];
}

/** RFC 4180 CSV of the given rows, with the header row first. */
export function relayLogsToCSV(logs: RelayLog[]): string {
	return buildCSV([...RELAY_LOGS_CSV_HEADERS], logs.map(relayLogToCSVRow));
}

/** A readable list of the filters in force, for the dialog's "Filters applied" line. */
export function formatRelayActiveFiltersSummary(filters: RelayLogFilters): string[] {
	const summary: string[] = [];
	if (filters.request_id) summary.push(`request "${filters.request_id}"`);
	if (filters.search) summary.push(`search "${filters.search}"`);
	const counted: [string, string[] | undefined][] = [
		["relays", filters.relay_names],
		["methods", filters.methods],
		["kinds", filters.kinds],
		["virtual keys", filters.virtual_key_ids],
		["users", filters.user_ids],
		["teams", filters.team_ids],
		["customers", filters.customer_ids],
		["business units", filters.business_unit_ids],
		["projects", filters.project_ids],
		["apps", filters.apps],
	];
	for (const [name, values] of counted) {
		if (values && values.length > 0) summary.push(`${name} (${values.length})`);
	}
	if (filters.statuses && filters.statuses.length > 0) summary.push(`status: ${filters.statuses.join(", ")}`);
	if (filters.billable !== undefined) summary.push(filters.billable ? "billable only" : "non-billable only");
	if (filters.min_cost !== undefined) summary.push(`min cost ${filters.min_cost}`);
	if (filters.max_cost !== undefined) summary.push(`max cost ${filters.max_cost}`);
	if (filters.period) summary.push(`period ${filters.period}`);
	return summary;
}

/**
 * Pins a relative period to fixed start/end timestamps so every batch of the export reads
 * the same window; otherwise rows arriving mid-export shift the offsets between batches.
 */
export function normalizeRelayExportFilters(filters: RelayLogFilters, timezone?: string): RelayLogFilters {
	if (!filters.period) return filters;
	const { from, to } = getRangeForPeriod(filters.period, timezone);
	return { ...filters, period: undefined, start_time: from.toISOString(), end_time: to.toISOString() };
}