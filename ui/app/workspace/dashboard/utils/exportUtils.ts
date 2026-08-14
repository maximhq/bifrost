/**
 * Dashboard-specific CSV data converters.
 *
 * Each function takes the dashboard data and returns { headers, rows } ready
 * for `buildCSV()`.
 */

import type {
	CostHistogramResponse,
	DimensionRankingsResponse,
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
	const headers = ["Timestamp", "总请求数", "成功", "Error", "已取消"];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.count, b.success, b.error, b.cancelled ?? 0]);
	return { headers, rows };
}

export function overviewTokensToCSV(data: TokenHistogramResponse | null): CSVData {
	const headers = ["Timestamp", "提示 Token 数", "完成 Token 数", "Token 总数", "Cached Read Tokens"];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.prompt_tokens, b.completion_tokens, b.total_tokens, b.cached_read_tokens]);
	return { headers, rows };
}

export function overviewCostToCSV(data: CostHistogramResponse | null): CSVData {
	const models = data?.models ?? [];
	const headers = ["Timestamp", "总费用", ...models];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.total_cost, ...models.map((m) => b.by_model?.[m] ?? 0)]);
	return { headers, rows };
}

export function overviewModelUsageToCSV(data: ModelHistogramResponse | null): CSVData {
	const models = data?.models ?? [];
	const modelHeaders = models.flatMap((m) => [`${m} Total`, `${m} Success`, `${m} Error`, `${m} Cancelled`]);
	const headers = ["Timestamp", ...modelHeaders];
	const rows = (data?.buckets ?? []).map((b) => [
		b.timestamp,
		...models.flatMap((m) => {
			const stats = b.by_model?.[m];
			return [stats?.total ?? 0, stats?.success ?? 0, stats?.error ?? 0, stats?.cancelled ?? 0];
		}),
	]);
	return { headers, rows };
}

export function overviewLatencyToCSV(data: LatencyHistogramResponse | null): CSVData {
	const headers = ["Timestamp", "Avg Latency (ms)", "P90 (ms)", "P95 (ms)", "P99 (ms)", "总请求数"];
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
	const headers = ["Timestamp", "总费用", ...providers];
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
		"模型",
		"规范模型",
		"提供商",
		"总请求数",
		"Success Count",
		"Success Rate (%)",
		"Token 总数",
		"Total Cost ($)",
		"Avg Latency (ms)",
		"Throughput (tok/s)",
		"Requests Trend (%)",
		"Tokens Trend (%)",
		"Cost Trend (%)",
		"Latency Trend (%)",
		"Throughput Trend (%)",
	];
	const rows = (data?.rankings ?? []).map((r) => [
		r.model,
		r.canonical_model_name ?? "",
		r.provider,
		r.total_requests,
		r.success_count,
		r.success_rate,
		r.total_tokens,
		r.total_cost,
		r.avg_latency,
		r.throughput,
		r.trend.has_previous_period ? r.trend.requests_trend : "不适用",
		r.trend.has_previous_period ? r.trend.tokens_trend : "不适用",
		r.trend.has_previous_period ? r.trend.cost_trend : "不适用",
		r.trend.has_previous_period ? r.trend.latency_trend : "不适用",
		r.trend.has_previous_period ? r.trend.throughput_trend : "不适用",
	]);
	return { headers, rows };
}

export function dimensionRankingsToCSV(data: DimensionRankingsResponse | null, dimensionLabel: string): CSVData {
	const headers = [
		`${dimensionLabel} ID`,
		`${dimensionLabel} Name`,
		"总请求数",
		"Token 总数",
		"Total Cost ($)",
		"Requests Trend (%)",
		"Tokens Trend (%)",
		"Cost Trend (%)",
	];
	const rows = (data?.rankings ?? []).map((r) => [
		r.id,
		r.name ?? "",
		r.total_requests,
		r.total_tokens,
		r.total_cost,
		r.trend.has_previous_period ? r.trend.requests_trend : "不适用",
		r.trend.has_previous_period ? r.trend.tokens_trend : "不适用",
		r.trend.has_previous_period ? r.trend.cost_trend : "不适用",
	]);
	return { headers, rows };
}

export function mcpVolumeToCSV(data: MCPHistogramResponse | null): CSVData {
	const headers = ["Timestamp", "总执行次数", "成功", "Error"];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.count, b.success, b.error]);
	return { headers, rows };
}

export function mcpCostToCSV(data: MCPCostHistogramResponse | null): CSVData {
	const headers = ["Timestamp", "Total Cost ($)"];
	const rows = (data?.buckets ?? []).map((b) => [b.timestamp, b.total_cost]);
	return { headers, rows };
}

export function mcpTopToolsToCSV(data: MCPTopToolsResponse | null): CSVData {
	const headers = ["工具名称", "Execution Count", "Cost ($)"];
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
	teamRankingsData: DimensionRankingsResponse | null;
	customerRankingsData: DimensionRankingsResponse | null;
	buRankingsData: DimensionRankingsResponse | null;
	userRankingsData: DimensionRankingsResponse | null;
	virtualKeyRankingsData: DimensionRankingsResponse | null;
	appRankingsData: DimensionRankingsResponse | null;
	// MCP
	mcpHistogramData: MCPHistogramResponse | null;
	mcpCostData: MCPCostHistogramResponse | null;
	mcpTopToolsData: MCPTopToolsResponse | null;
}

export type DashboardTab =
	| "overview"
	| "provider-usage"
	| "rankings"
	| "team-rankings"
	| "customer-rankings"
	| "bu-rankings"
	| "user-rankings"
	| "virtual-key-rankings"
	| "app-rankings"
	| "mcp";

export type ExportTab = DashboardTab | "all";

/**
 * Every exportable tab, in the order sections appear in a full export.
 * Single source of truth for the tab labels shown in the export menu and as
 * PDF section headings, and for the DOM ids the PDF capture reads.
 */
export const DASHBOARD_EXPORT_TABS: { value: DashboardTab; label: string; sectionId: string }[] = [
	{ value: "overview", label: "概览", sectionId: "dashboard-section-overview" },
	{ value: "provider-usage", label: "提供商用量", sectionId: "dashboard-section-provider-usage" },
	{ value: "rankings", label: "模型排行", sectionId: "dashboard-section-rankings" },
	{ value: "mcp", label: "MCP 用量", sectionId: "dashboard-section-mcp" },
	{ value: "team-rankings", label: "团队排行", sectionId: "dashboard-section-team-rankings" },
	{ value: "customer-rankings", label: "客户排行", sectionId: "dashboard-section-customer-rankings" },
	{ value: "bu-rankings", label: "业务单元排行", sectionId: "dashboard-section-bu-rankings" },
	{ value: "user-rankings", label: "用户排行", sectionId: "dashboard-section-user-rankings" },
	{ value: "virtual-key-rankings", label: "虚拟密钥排行", sectionId: "dashboard-section-virtual-key-rankings" },
	{ value: "app-rankings", label: "App Rankings", sectionId: "dashboard-section-app-rankings" },
];

export const getExportTabLabel = (tab: DashboardTab): string => DASHBOARD_EXPORT_TABS.find((t) => t.value === tab)?.label ?? "Current Tab";

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

	if (tab === "all" || tab === "team-rankings") {
		sections.push({ name: "team-rankings", csv: dimensionRankingsToCSV(data.teamRankingsData, "团队") });
	}

	if (tab === "all" || tab === "customer-rankings") {
		sections.push({ name: "customer-rankings", csv: dimensionRankingsToCSV(data.customerRankingsData, "客户") });
	}

	if (tab === "all" || tab === "bu-rankings") {
		sections.push({ name: "bu-rankings", csv: dimensionRankingsToCSV(data.buRankingsData, "业务单元") });
	}

	if (tab === "all" || tab === "user-rankings") {
		sections.push({ name: "user-rankings", csv: dimensionRankingsToCSV(data.userRankingsData, "用户") });
	}

	if (tab === "all" || tab === "virtual-key-rankings") {
		sections.push({ name: "virtual-key-rankings", csv: dimensionRankingsToCSV(data.virtualKeyRankingsData, "虚拟密钥") });
	}

	if (tab === "all" || tab === "app-rankings") {
		sections.push({ name: "app-rankings", csv: dimensionRankingsToCSV(data.appRankingsData, "应用") });
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