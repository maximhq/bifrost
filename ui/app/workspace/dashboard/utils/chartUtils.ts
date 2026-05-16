// Chart utility functions for the dashboard

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
	if (cost < 0.01) {
		return `$${cost.toFixed(4)}`;
	}
	return `$${cost.toFixed(2)}`;
}

// Format token values
export function formatTokens(tokens: number): string {
	if (tokens >= 1000000) {
		return `${(tokens / 1000000).toFixed(1)}M`;
	}
	if (tokens >= 1000) {
		return `${(tokens / 1000).toFixed(1)}K`;
	}
	return tokens.toLocaleString();
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

// Shared CSS class constants for chart card headers
export const CHART_HEADER_ACTIONS_CLASS = "flex min-w-0 w-full flex-col-reverse gap-2";
export const CHART_HEADER_LEGEND_CLASS = "flex min-h-5 min-w-0 flex-wrap items-center gap-2 pl-2 text-xs";
export const CHART_HEADER_CONTROLS_CLASS = "flex items-center justify-end gap-2";

// Chart colors
export const CHART_COLORS = {
	success: "#10b981", // emerald-500
	error: "#ef4444", // red-500
	promptTokens: "#3b82f6", // blue-500
	completionTokens: "#10b981", // emerald-500
	totalTokens: "#8b5cf6", // violet-500
	cost: "#f59e0b", // amber-500
	cachedReadTokens: "#06b6d4", // cyan-500
};