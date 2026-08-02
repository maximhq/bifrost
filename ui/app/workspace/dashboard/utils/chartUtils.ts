// Chart utility functions for the dashboard
import { formatCompactNumber } from "@/lib/utils/numbers";

// Format timestamp based on bucket size
export function formatTimestamp(timestamp: string, bucketSizeSeconds: number): string {
	const date = new Date(timestamp);

	if (bucketSizeSeconds >= 86400) {
		// Daily buckets: "Jan 20"
		return date.toLocaleDateString("en-US", { month: "short", day: "numeric" });
	} else if (bucketSizeSeconds >= 3600) {
		// Hourly buckets: "10:00"
		return date.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false });
	} else {
		// Sub-hourly: "10:15"
		return date.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false });
	}
}

// Format full timestamp for tooltip
export function formatFullTimestamp(timestamp: string): string {
	const date = new Date(timestamp);
	return date.toLocaleString("en-US", {
		month: "short",
		day: "numeric",
		hour: "2-digit",
		minute: "2-digit",
		hour12: false,
	});
}

// Format cost values
export function formatCost(cost: number): string {
	if (cost === 0) {
		return `$0`;
	}
	if (cost < 0.01) {
		return `$${cost.toFixed(4)}`;
	}
	return `$${cost.toFixed(2)}`;
}

// Color palette for models. Length governs TOP_SERIES_LIMIT (top-N rollup cap),
// so colors and named-series count stay coupled — adding a color expands top-N.
export const MODEL_COLORS = [
	"#10b981", // emerald-500
	"#3b82f6", // blue-500
	"#f59e0b", // amber-500
	"#ef4444", // red-500
	"#8b5cf6", // violet-500
	"#ec4899", // pink-500
	"#06b6d4", // cyan-500
	"#84cc16", // lime-500
	"#f97316", // orange-500
	"#14b8a6", // teal-500
	"#eab308", // yellow-500
	"#d946ef", // fuchsia-500
];

// Get color for a model by index
export function getModelColor(index: number): string {
	return MODEL_COLORS[index % MODEL_COLORS.length];
}

// Top-N series rollup: keeps the visible recharts subtree bounded when a
// dimension (models, providers) grows large. The palette has 8 colors and
// the legend already says "+N more", so the data path follows the palette.
export const TOP_SERIES_LIMIT = MODEL_COLORS.length;
export const OTHER_SERIES_KEY = "__other__";
export const OTHER_SERIES_LABEL = "Other";
export const OTHER_SERIES_COLOR = "#94a3b8"; // slate-400

export const UNNAMED_MODEL_LABEL = "(unnamed)";

// Resolves a raw model value to its display label: the canonical model name
// when one is known (e.g. Bedrock inference-profile IDs mapped via key
// aliases), the raw value otherwise.
export function displayModelLabel(model: string, labels?: Record<string, string>): string {
	if (model === OTHER_SERIES_KEY) return OTHER_SERIES_LABEL;
	if (model === "") return UNNAMED_MODEL_LABEL;
	return labels?.[model] ?? model;
}

export function pickTopSeries<T>(
	buckets: T[],
	seriesLabels: string[],
	getValue: (bucket: T, label: string) => number,
	limit: number = TOP_SERIES_LIMIT,
): string[] {
	if (seriesLabels.length <= limit) return seriesLabels;
	const totals = new Map<string, number>();
	for (const bucket of buckets) {
		for (const label of seriesLabels) {
			totals.set(label, (totals.get(label) ?? 0) + getValue(bucket, label));
		}
	}
	return [...seriesLabels].sort((a, b) => (totals.get(b) ?? 0) - (totals.get(a) ?? 0)).slice(0, limit);
}

// The ordered series list as a chart draws it: top-N by total value with
// OTHER_SERIES_KEY appended when the long tail was rolled up (unless the chart
// opts out of the rollup, e.g. latency/throughput where averaging the tail
// would mislead). Legends color entries by index into this list, so they must
// consume the exact same list as the chart — deriving legend colors from the
// API's (alphabetical) label order desyncs once the series count exceeds
// TOP_SERIES_LIMIT and this list gets re-sorted by volume.
export function computeDisplaySeries<T>(
	buckets: T[] | undefined,
	seriesLabels: string[] | undefined,
	getValue: (bucket: T, label: string) => number,
	includeOther: boolean = true,
): string[] {
	if (!buckets || !seriesLabels || seriesLabels.length === 0) return [];
	const top = pickTopSeries(buckets, seriesLabels, getValue);
	if (includeOther && top.length < seriesLabels.length) return [...top, OTHER_SERIES_KEY];
	return top;
}

// Format latency values
export function formatLatency(ms: number): string {
	if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`;
	return `${ms.toFixed(0)}ms`;
}

// Latency chart color palette
export const LATENCY_COLORS = {
	avg: "#06b6d4", // cyan-500
	p90: "#3b82f6", // blue-500
	p95: "#f59e0b", // amber-500
	p99: "#ef4444", // red-500
};

// Format token-generation throughput (tokens/sec) with compact units (1k, 5k, 1M).
// Uses a non-breaking space so the value and unit never wrap onto two lines.
export function formatTokensPerSecond(tps: number): string {
	if (!Number.isFinite(tps) || tps <= 0) return "0 tok/s";
	return `${formatCompactNumber(tps, 1)} tok/s`;
}

// Throughput chart color
export const THROUGHPUT_COLOR = "#10b981"; // emerald-500

// Shared CSS class constants for chart card headers
export const CHART_HEADER_ACTIONS_CLASS = "flex min-w-0 w-full flex-col-reverse gap-2";
export const CHART_HEADER_LEGEND_CLASS = "flex min-h-5 min-w-0 flex-wrap items-center gap-2 pl-2 text-xs";
export const CHART_HEADER_CONTROLS_CLASS = "flex items-center justify-end gap-2";

// Chart colors
export const CHART_COLORS = {
	success: "#10b981", // emerald-500
	error: "#ef4444", // red-500
	cancelled: "#a1a1aa", // zinc-400
	promptTokens: "#3b82f6", // blue-500
	completionTokens: "#10b981", // emerald-500
	totalTokens: "#8b5cf6", // violet-500
	cost: "#f59e0b", // amber-500
	cachedReadTokens: "#06b6d4", // cyan-500
};