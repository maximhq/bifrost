import type { BifrostError, OverheadBucket } from "@/lib/types/logs";

export type AgentRecordKind = "request" | "event";
export type AgentLogStatus = "processing" | "success" | "error";

// Metadata row returned by GET /api/agents/history. The bounded input preview
// stays database-resident so the table does not hydrate full payload objects.
// Mirrors logstore.AgentLogSummary JSON field names.
export interface AgentLogSummary {
	id: string;
	timestamp: string; // ISO string from Go time.Time
	record_kind: AgentRecordKind;
	operation: string;
	status: AgentLogStatus;
	agent_name: string;
	user_id?: string;
	user_name?: string;
	virtual_key_id?: string;
	virtual_key_name?: string;
	team_id?: string;
	team_name?: string;
	team_ids?: string[];
	team_names?: string[];
	customer_id?: string;
	customer_name?: string;
	customer_ids?: string[];
	customer_names?: string[];
	business_unit_id?: string;
	business_unit_name?: string;
	business_unit_ids?: string[];
	business_unit_names?: string[];
	project_id?: string;
	project_name?: string;
	budget_ids?: string[];
	rate_limit_ids?: string[];
	request_id: string;
	trace_id?: string;
	task_id?: string;
	context_id?: string;
	message_id?: string;
	request_message_id?: string;
	response_message_id?: string;
	artifact_id?: string;
	push_config_id?: string;
	delivery_id?: string;
	attempt_id?: string;
	event_sequence?: number;
	event_type?: string;
	task_state?: string;
	downstream_transport?: string;
	upstream_transport?: string;
	latency?: number; // milliseconds
	upstream_latency?: number; // milliseconds
	overhead_latency?: number; // milliseconds
	overhead_breakdown?: OverheadBucket[];
	content_type?: string;
	input?: string;
}

// Hydrated entry returned by GET /api/agents/history/{id}. Bodies are JSON
// strings with secrets already redacted server-side.
export interface AgentLogDetail extends AgentLogSummary {
	request_body?: string;
	response_body?: string;
	event_body?: string;
	plugin_logs?: string; // JSON string of plugin execution logs grouped by plugin name
	error_details?: BifrostError;
}

export interface AgentLogOperation extends AgentLogDetail {
	events: AgentLogDetail[];
}

// Client-side filter state for the Agent logs page. The page always supplies a
// period or an explicit start/end unless an identifier is set.
export interface AgentLogFilters {
	// Multi-value filters match any of their values; filters are ANDed together.
	agent_name?: string[];
	operation?: string[];
	user_id?: string[];
	virtual_key_id?: string[];
	team_id?: string[];
	customer_id?: string[];
	business_unit_id?: string[];
	project_id?: string[];
	request_id?: string;
	trace_id?: string;
	task_id?: string;
	context_id?: string;
	push_config_id?: string;
	delivery_id?: string;
	attempt_id?: string;
	record_kind?: string[];
	status?: string[];
	event_type?: string[];
	task_state?: string[];
	// Free-text match across DB-resident metadata columns only (agent, operation,
	// task/context/request/trace id, error). Protocol payloads are not searchable.
	search?: string;
	// Relative period (e.g. "1h"); the API slice converts it to a fresh
	// RFC3339 window on every request because the backend has no period param.
	period?: string;
	start_time?: string; // RFC3339
	end_time?: string; // RFC3339
}

export interface AgentPagination {
	limit: number;
	offset: number;
	sort_by: "timestamp" | "latency";
	order: "asc" | "desc";
}

// Mirrors logstore.PaginationOptions JSON field names.
export interface AgentLogHistoryPagination {
	limit: number;
	offset: number;
	sort_by?: string;
	order: string;
	total_count: number;
	selected_offset?: number;
}

export interface AgentLogHistoryResponse {
	logs: AgentLogSummary[];
	pagination: AgentLogHistoryPagination;
}

export interface AgentLogOperationResponse {
	logs: AgentLogOperation[];
	pagination: AgentLogHistoryPagination;
}

// Mirrors logstore.AgentLogStats. Agent has no pricing model, so there are no
// token or cost fields here.
export interface AgentLogStats {
	total_entries: number;
	success_count: number;
	error_count: number;
	success_rate: number;
	average_latency: number; // milliseconds
}

// Mirrors logstore.AgentHistogramResult. Structurally compatible with the shape
// LogsVolumeChart already accepts for MCP, so the chart is reused as-is.
export interface AgentHistogramBucket {
	timestamp: string;
	count: number;
	success: number;
	error: number;
}

export interface AgentHistogramResponse {
	buckets: AgentHistogramBucket[];
	bucket_size_seconds: number;
}