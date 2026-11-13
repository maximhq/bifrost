import type { TraceFilters, TracePagination } from "@/lib/types/traces";

/** Builds the query string of a /api/traces list or overview request. Empty values are left out. */
export function buildTraceQuery(
	filters: TraceFilters,
	pagination?: Partial<TracePagination>,
	extra?: Record<string, string | number>,
): string {
	const params = new URLSearchParams();
	const list = (name: string, values?: string[]) => {
		if (values && values.length > 0) params.set(name, values.join(","));
	};
	const text = (name: string, value?: string) => {
		if (value) params.set(name, value);
	};
	list("project_ids", filters.project_ids);
	list("sources", filters.sources);
	list("kinds", filters.kinds);
	list("models", filters.models);
	list("environments", filters.environments);
	list("tags", filters.tags);
	text("session_id", filters.session_id);
	text("user_id", filters.user_id);
	text("experiment_id", filters.experiment_id);
	text("status", filters.status);
	text("trace_id", filters.trace_id);
	text("search", filters.search);
	text("start_time", filters.start_time);
	text("end_time", filters.end_time);
	if (pagination) {
		if (pagination.limit !== undefined) params.set("limit", String(pagination.limit));
		if (pagination.offset !== undefined) params.set("offset", String(pagination.offset));
		text("sort_by", pagination.sort_by);
		text("order", pagination.order);
	}
	for (const [name, value] of Object.entries(extra ?? {})) params.set(name, String(value));
	return params.toString();
}