/**
 * Dashboard-specific CSV data converters.
 *
 * Each function takes the dashboard data and returns { headers, rows } ready
 * for `buildCSV()`.
 */

import type {
	CostHistogramResponse,
	LatencyHistogramResponse,
	LogsHistogramResponse,
	MCPCostHistogramResponse,
	MCPHistogramResponse,
	MCPTopToolsResponse,
	ModelHistogramResponse,
	ModelRankingsResponse,
	ProviderCostHistogramResponse,
	ProviderLatencyHistogramResponse,
	ProviderTokenHistogramResponse,
	TokenHistogramResponse,
} from "@/lib/types/logs";

type CSVData = { headers: string[]; rows: unknown[][] };

export function overviewVolumeToCSV(data: LogsHistogramResponse | null): CSVData {
	const headers = ["Timestamp", "Total Requests", "Success", "Error"];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.count, b.success, b.error]);
	return { headers, rows };
}

export function overviewTokensToCSV(data: TokenHistogramResponse | null): CSVData {
	const headers = ["Timestamp", "Prompt Tokens", "Completion Tokens", "Total Tokens", "Cached Read Tokens"];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.prompt_tokens, b.completion_tokens, b.total_tokens, b.cached_read_tokens]);
	return { headers, rows };
}

export function overviewCostToCSV(data: CostHistogramResponse | null): CSVData {
	const models = data?.models ?? [];
	const headers = ["Timestamp", "Total Cost", ...models];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.total_cost, ...models.map((m) => b.by_model?.[m] ?? 0)]);
	return { headers, rows };
}

export function overviewModelUsageToCSV(data: ModelHistogramResponse | null): CSVData {
	const models = data?.models ?? [];
	const modelHeaders = models.flatMap((m) => [`${m} Total`, `${m} Success`, `${m} Error`]);
	const headers = ["Timestamp", ...modelHeaders];
	const rows = (data?.buckets ?? []).map((b) => [
		b.timestamp,
		...models.flatMap((m) => {
			const stats = b.by_model?.[m];
			return [stats?.total ?? 0, stats?.success ?? 0, stats?.error ?? 0];
		}),
	]);
	return { headers, rows };
}

export function overviewLatencyToCSV(data: LatencyHistogramResponse | null): CSVData {
	const headers = ["Timestamp", "Avg Latency (ms)", "P90 (ms)", "P95 (ms)", "P99 (ms)", "Total Requests"];
	const rows = (data?.buckets ?? []).map((b) => [
		b.timestamp,
		b.avg_latency,
		b.p90_latency,
		b.p95_latency,
		b.p99_latency,
		b.total_requests,
	]);
	return { headers, rows };
}

export function providerCostToCSV(data: ProviderCostHistogramResponse | null): CSVData {
	const providers = data?.providers ?? [];
	const headers = ["Timestamp", "Total Cost", ...providers];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.total_cost, ...providers.map((p) => b.by_provider?.[p] ?? 0)]);
	return { headers, rows };
}

export function providerTokensToCSV(data: ProviderTokenHistogramResponse | null): CSVData {
	const providers = data?.providers ?? [];
	const provHeaders = providers.flatMap((p) => [`${p} Prompt`, `${p} Completion`, `${p} Total`]);
	const headers = ["Timestamp", ...provHeaders];
	const rows = (data?.buckets ?? []).map((b) => [
		b.timestamp,
		...providers.flatMap((p) => {
			const stats = b.by_provider?.[p];
			return [stats?.prompt_tokens ?? 0, stats?.completion_tokens ?? 0, stats?.total_tokens ?? 0];
		}),
	]);
	return { headers, rows };
}

export function providerLatencyToCSV(data: ProviderLatencyHistogramResponse | null): CSVData {
	const providers = data?.providers ?? [];
	const provHeaders = providers.flatMap((p) => [`${p} Avg (ms)`, `${p} P90 (ms)`, `${p} P95 (ms)`, `${p} P99 (ms)`]);
	const headers = ["Timestamp", ...provHeaders];
	const rows = (data?.buckets ?? []).map((b) => [
		b.timestamp,
		...providers.flatMap((p) => {
			const stats = b.by_provider?.[p];
			return [stats?.avg_latency ?? 0, stats?.p90_latency ?? 0, stats?.p95_latency ?? 0, stats?.p99_latency ?? 0];
		}),
	]);
	return { headers, rows };
}

export function modelRankingsToCSV(data: ModelRankingsResponse | null): CSVData {
	const headers = [
		"Model",
		"Provider",
		"Total Requests",
		"Success Count",
		"Success Rate (%)",
		"Total Tokens",
		"Total Cost ($)",
		"Avg Latency (ms)",
		"Requests Trend (%)",
		"Tokens Trend (%)",
		"Cost Trend (%)",
		"Latency Trend (%)",
	];
	const rows = (data?.rankings ?? []).map((r) => [
		r.model,
		r.provider,
		r.total_requests,
		r.success_count,
		r.success_rate,
		r.total_tokens,
		r.total_cost,
		r.avg_latency,
		r.trend.has_previous_period ? r.trend.requests_trend : "N/A",
		r.trend.has_previous_period ? r.trend.tokens_trend : "N/A",
		r.trend.has_previous_period ? r.trend.cost_trend : "N/A",
		r.trend.has_previous_period ? r.trend.latency_trend : "N/A",
	]);
	return { headers, rows };
}

export function mcpVolumeToCSV(data: MCPHistogramResponse | null): CSVData {
	const headers = ["Timestamp", "Total Executions", "Success", "Error"];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.count, b.success, b.error]);
	return { headers, rows };
}

export function mcpCostToCSV(data: MCPCostHistogramResponse | null): CSVData {
	const headers = ["Timestamp", "Total Cost ($)"];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.total_cost]);
	return { headers, rows };
}

export function mcpTopToolsToCSV(data: MCPTopToolsResponse | null): CSVData {
	const headers = ["Tool Name", "Execution Count", "Cost ($)"];
	const rows = (data?.tools ?? []).map((t) => [t.tool_name, t.count, t.cost]);
	return { headers, rows };
}

export interface DashboardData {
	// Overview
	histogramData: LogsHistogramResponse | null;
	tokenData: TokenHistogramResponse | null;
	costData: CostHistogramResponse | null;
	modelData: ModelHistogramResponse | null;
	latencyData: LatencyHistogramResponse | null;
	// Provider Usage
	providerCostData: ProviderCostHistogramResponse | null;
	providerTokenData: ProviderTokenHistogramResponse | null;
	providerLatencyData: ProviderLatencyHistogramResponse | null;
	// Rankings
	rankingsData: ModelRankingsResponse | null;
	// MCP
	mcpHistogramData: MCPHistogramResponse | null;
	mcpCostData: MCPCostHistogramResponse | null;
	mcpTopToolsData: MCPTopToolsResponse | null;
}

export type ExportTab = "all" | "overview" | "provider-usage" | "rankings" | "mcp";

/** Return all CSV sections for the selected scope. Each entry becomes its own sheet / file section. */
export function getCSVSections(data: DashboardData, tab: ExportTab): { name: string; csv: CSVData }[] {
	const sections: { name: string; csv: CSVData }[] = [];

	if (tab === "all" || tab === "overview") {
		sections.push(
			{ name: "overview-volume", csv: overviewVolumeToCSV(data.histogramData) },
			{ name: "overview-tokens", csv: overviewTokensToCSV(data.tokenData) },
			{ name: "overview-cost", csv: overviewCostToCSV(data.costData) },
			{ name: "overview-model-usage", csv: overviewModelUsageToCSV(data.modelData) },
			{ name: "overview-latency", csv: overviewLatencyToCSV(data.latencyData) },
		);
	}

	if (tab === "all" || tab === "provider-usage") {
		sections.push(
			{ name: "provider-cost", csv: providerCostToCSV(data.providerCostData) },
			{ name: "provider-tokens", csv: providerTokensToCSV(data.providerTokenData) },
			{ name: "provider-latency", csv: providerLatencyToCSV(data.providerLatencyData) },
		);
	}

	if (tab === "all" || tab === "rankings") {
		sections.push({ name: "model-rankings", csv: modelRankingsToCSV(data.rankingsData) });
	}

	if (tab === "all" || tab === "mcp") {
		sections.push(
			{ name: "mcp-volume", csv: mcpVolumeToCSV(data.mcpHistogramData) },
			{ name: "mcp-cost", csv: mcpCostToCSV(data.mcpCostData) },
			{ name: "mcp-top-tools", csv: mcpTopToolsToCSV(data.mcpTopToolsData) },
		);
	}

	return sections;
}