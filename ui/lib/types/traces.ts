// Types of the Tracing dashboard API (/api/traces). They mirror framework/logstore/traces.go.

export type TraceSource = "braintrust" | "langfuse" | "arize" | "otel";

export type TraceSpanKind =
	| "llm"
	| "tool"
	| "task"
	| "function"
	| "agent"
	| "chain"
	| "retriever"
	| "embedding"
	| "reranker"
	| "guardrail"
	| "score"
	| "eval"
	| "event"
	| "http"
	| "unknown";

/** The gateway's own figures for a call, copied onto the span when it was linked. */
export interface TraceLinkSnapshot {
	model?: string;
	provider?: string;
	prompt_tokens?: number;
	completion_tokens?: number;
	total_tokens?: number;
	cached_read_tokens?: number;
	cost?: number;
	latency_ms?: number;
}

export interface TraceSpan {
	project_id: string;
	trace_id: string;
	span_id: string;
	parent_span_id?: string;
	parent_span_ids?: string[];
	row_id?: string;
	source: TraceSource;
	name: string;
	kind: TraceSpanKind;
	status?: "ok" | "error";
	error?: string;
	partial: boolean;
	start_time: string;
	end_time?: string;
	duration_ms?: number;
	input?: string;
	output?: string;
	expected?: string;
	metadata?: Record<string, unknown>;
	tags?: string[];
	attributes?: Record<string, unknown>;
	model?: string;
	provider?: string;
	prompt_tokens?: number;
	completion_tokens?: number;
	total_tokens?: number;
	cached_read_tokens?: number;
	cached_write_tokens?: number;
	reasoning_tokens?: number;
	cost?: number;
	cost_estimated?: boolean;
	time_to_first_token_ms?: number;
	session_id?: string;
	user_id?: string;
	environment?: string;
	experiment_id?: string;
	link_method?: string;
	link_snapshot?: TraceLinkSnapshot;
	linked_log_expired?: boolean;
	content_hidden?: boolean;
}

export interface Trace {
	project_id: string;
	trace_id: string;
	source: TraceSource;
	name: string;
	root_span_id?: string;
	in_progress: boolean;
	start_time: string;
	end_time?: string;
	duration_ms?: number;
	input?: string;
	output?: string;
	tags?: string[];
	scores?: Record<string, number>;
	session_id?: string;
	user_id?: string;
	environment?: string;
	experiment_id?: string;
	span_count: number;
	llm_span_count: number;
	linked_span_count: number;
	error_count: number;
	prompt_tokens: number;
	completion_tokens: number;
	total_tokens: number;
	cached_read_tokens: number;
	cost?: number;
	estimated_cost?: number;
	unpriced_span_count: number;
}

export interface TraceSpanLink {
	span_id: string;
	log_kind: "llm" | "mcp";
	log_id: string;
	method: string;
}

export interface TraceScore {
	id: string;
	trace_id: string;
	span_id?: string;
	name: string;
	value?: number;
	string_value?: string;
	data_type?: string;
	comment?: string;
	source: "sdk" | "evaluator" | "human";
	author?: string;
	created_at: string;
}

export interface TraceComment {
	id: string;
	trace_id: string;
	span_id?: string;
	author?: string;
	text: string;
	source: "sdk" | "human";
	created_at: string;
}

export interface TraceDetail {
	trace: Trace;
	spans: TraceSpan[];
	links: TraceSpanLink[] | null;
	scores: TraceScore[] | null;
	comments: TraceComment[] | null;
}

export interface TraceFilters {
	project_ids?: string[];
	sources?: string[];
	kinds?: string[];
	models?: string[];
	environments?: string[];
	tags?: string[];
	session_id?: string;
	user_id?: string;
	experiment_id?: string;
	status?: string;
	trace_id?: string;
	search?: string;
	start_time?: string;
	end_time?: string;
}

export interface TracePagination {
	limit: number;
	offset: number;
	sort_by?: string;
	order?: "asc" | "desc";
	total_count?: number;
}

export interface TraceSearchResponse {
	traces: Trace[] | null;
	pagination: TracePagination;
}

export interface TraceSpanSearchResponse {
	spans: TraceSpan[] | null;
	pagination: TracePagination;
}

export interface TraceStats {
	trace_count: number;
	span_count: number;
	llm_span_count: number;
	linked_span_count: number;
	error_trace_count: number;
	in_progress_count: number;
	total_tokens: number;
	prompt_tokens: number;
	completion_tokens: number;
	cached_read_tokens: number;
	total_cost: number;
	estimated_cost: number;
	unpriced_span_count: number;
	avg_duration_ms: number;
}

export interface TraceBreakdownRow {
	key: string;
	trace_count: number;
	total_tokens: number;
	total_cost: number;
	error_trace_count: number;
	llm_span_count: number;
	linked_span_count: number;
}

export interface TraceStatsResponse {
	stats: TraceStats;
	by_project: TraceBreakdownRow[] | null;
	by_source: TraceBreakdownRow[] | null;
}

export interface TraceHistogramBucket {
	timestamp: string;
	count: number;
	errors: number;
	total_tokens: number;
	total_cost: number;
}

export interface TraceHistogramResponse {
	buckets: TraceHistogramBucket[] | null;
	bucket_size_seconds: number;
}

export interface TraceSession {
	project_id: string;
	session_id: string;
	trace_count: number;
	first_seen: string;
	last_seen: string;
	total_tokens: number;
	total_cost: number;
	error_count: number;
	user_id?: string;
}

export interface TraceSessionSearchResponse {
	sessions: TraceSession[] | null;
	pagination: TracePagination;
}

export interface TraceMaskingRule {
	name: string;
	pattern: string;
	replacement: string;
}

export interface TraceForwardTarget {
	vendor: TraceSource;
	base_url: string;
	headers?: Record<string, string>;
	enabled: boolean;
}

/** Runs one evaluator on the project's traces as they complete. Its result is a score on the trace. */
export interface TraceEvaluatorRule {
	evaluator_id: string;
	name?: string;
	sample_rate: number;
	daily_cap: number;
	enabled: boolean;
}

export interface TraceProject {
	id: string;
	name: string;
	description?: string;
	retention_days?: number | null;
	masking_rules?: TraceMaskingRule[];
	forward?: TraceForwardTarget | null;
	evaluators?: TraceEvaluatorRule[];
	created_at?: string;
}

export interface TraceConnectInfo {
	base_url: string;
	endpoints: Record<TraceSource, string>;
	uploads_available: boolean;
	/** Whether SDKs must send the dashboard credentials as their API key. */
	auth_required: boolean;
}

export interface TraceFilterData {
	sources: string[] | null;
	environments: string[] | null;
	models: string[] | null;
	kinds: string[] | null;
}
/** An experiment an SDK registered (a Braintrust experiment or a Langfuse dataset run), with what its traces add up to. */
export interface TraceExperiment {
	project_id: string;
	id: string;
	name: string;
	source: TraceSource;
	dataset_name?: string;
	created_at: string;
	summary: {
		trace_count: number;
		error_count: number;
		scores: Record<string, number>;
		avg_duration_ms: number;
		total_tokens: number;
		total_cost: number;
		truncated?: boolean;
	};
}