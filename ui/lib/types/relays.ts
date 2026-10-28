import type { BifrostError, OverheadBucket } from "@/lib/types/logs";

// Types for Relays (/api/relays) and Relay Logs
// (/api/relay-logs). They mirror core/schemas/relay.go and
// framework/logstore/relaylogs.go.

export type RelayAuthType = "bearer" | "header" | "query" | "basic" | "aws_sigv4" | "none";
export type RelayModelSource = "body" | "query";
export type RelayUnitsSource = "response" | "request";
export type RelayUnitsMeasure = "value" | "chars" | "body_bytes" | "body_chars";
export type RelayRouteMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "HEAD";

export const RelayAuthTypes: RelayAuthType[] = ["bearer", "header", "query", "basic", "aws_sigv4", "none"];
export const RelayRouteMethods: RelayRouteMethod[] = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"];

export const RelayAuthTypeLabels: Record<RelayAuthType, string> = {
	bearer: "Bearer token",
	header: "Custom header",
	query: "Query parameter",
	basic: "Basic auth",
	aws_sigv4: "AWS SigV4",
	none: "No authentication",
};

export const RelayUnitsSourceLabels: Record<RelayUnitsSource, string> = {
	response: "Response body",
	request: "Request body",
};

export const RelayUnitsMeasureLabels: Record<RelayUnitsMeasure, string> = {
	value: "Number at path",
	chars: "Characters of the string at path",
	body_bytes: "Size of the whole body (bytes)",
	body_chars: "Characters of the whole body",
};

/** A secret as the API carries it: a value (redacted on read), or an env./vault reference. */
export interface RelaySecret {
	value: string;
	ref?: string;
	type?: string;
}

export interface RelayAuthConfig {
	type: RelayAuthType;
	header_name?: string;
	query_param?: string;
	value_prefix?: string;
	aws?: { service: string };
}

export interface RelayAWSCredential {
	access_key?: RelaySecret;
	secret_key?: RelaySecret;
	session_token?: RelaySecret;
	region?: RelaySecret;
	role_arn?: RelaySecret;
	external_id?: RelaySecret;
	session_name?: RelaySecret;
}

export interface RelayCredential {
	value?: RelaySecret;
	aws?: RelayAWSCredential;
}

export interface RelayUsageConfig {
	model_path?: string;
	model_source?: RelayModelSource;
	input_tokens_path?: string;
	output_tokens_path?: string;
	total_tokens_path?: string;
	cost_path?: string;
	cost_per_request?: number;
	units_path?: string;
	units_source?: RelayUnitsSource;
	units_measure?: RelayUnitsMeasure;
	cost_per_unit?: number;
	units_block_size?: number;
	units_minimum?: number;
}

export interface RelayStreamingConfig {
	paths?: string[];
}

export interface RelayJobConfig {
	id_path: string;
	poll: { method?: string; path: string; headers?: Record<string, string>; body?: string; content_type?: string };
	status_path: string;
	succeeded: string[];
	failed?: string[];
	interval_seconds?: number;
	timeout_seconds?: number;
	usage?: RelayUsageConfig;
}

export interface RelayRouteConfig {
	method: RelayRouteMethod;
	path: string;
	headers?: Record<string, string>;
	enabled?: boolean;
	billable?: boolean;
	stream?: boolean;
	usage?: RelayUsageConfig;
	job?: RelayJobConfig;
}

export interface RelayNetworkConfig {
	timeout_seconds?: number;
	stream_idle_timeout_in_seconds?: number;
	max_conns_per_host?: number;
	insecure_skip_verify?: boolean;
	ca_cert_pem?: string;
}

export interface RelayProxyConfig {
	type: "none" | "http" | "socks5" | "environment";
	url?: RelaySecret;
	username?: RelaySecret;
	password?: RelaySecret;
	ca_cert_pem?: RelaySecret;
}

export interface Relay {
	name: string;
	description?: string;
	base_url: string;
	auth: RelayAuthConfig;
	credential?: RelayCredential;
	extra_headers?: Record<string, RelaySecret>;
	network?: RelayNetworkConfig;
	proxy_config?: RelayProxyConfig;
	usage?: RelayUsageConfig;
	streaming?: RelayStreamingConfig;
	routes?: RelayRouteConfig[];
	skip_route_check?: boolean;
	ignore_upstream_cost?: boolean;
	/** Read-only: the path callers use, /relay/{name}. */
	url?: string;
}

export interface GetRelaysParams {
	search?: string;
	limit?: number;
	offset?: number;
}

export interface GetRelaysResponse {
	relays: Relay[];
	count: number;
	total_count: number;
}

export interface UsagePreviewSample {
	method: string;
	path: string;
	headers?: Record<string, string>;
	accept?: string;
	request_content_type?: string;
	request_body?: string;
	status_code?: number;
	response_content_type?: string;
	response_body?: string;
}

export interface UsagePreviewRequest {
	name?: string;
	config?: Relay;
	sample: UsagePreviewSample;
}

/** POST /api/relays/openapi-spec: the gateway fetches a document the browser cannot (CORS) for the route import. */
export interface RelayOpenAPISpecResponse {
	spec: string;
	/** The URL the document was read from after redirects; relative servers resolve against it. */
	spec_url: string;
}

export interface UsagePreview {
	route_index: number | null;
	route_allowed: boolean;
	billable: boolean;
	streams: boolean;
	counted: boolean;
	model_path: string;
	model_source: RelayModelSource;
	model: string;
	units_source: RelayUnitsSource;
	units_measure: RelayUnitsMeasure;
	units_path: string;
	units: number | null;
	units_billed: number | null;
	units_block_size: number;
	units_minimum: number;
	cost_per_unit: number;
	cost_per_request: number;
	extracted_cost: number | null;
	tokens: { input: number; output: number; total: number } | null;
	cost: number;
	notes: string[];
}

// --- Relay Logs ---

export type RelayLogStatus = "success" | "error" | "cancelled" | "processing";

export interface RelayLog {
	id: string;
	timestamp: string;
	relay_name: string;
	kind: "call" | "job";
	method: string;
	path: string;
	route_path?: string;
	status: RelayLogStatus;
	status_code: number;
	streamed: boolean;
	billable: boolean;
	model?: string;
	latency?: number; // ms, pre-hooks to the end of the reply
	upstream_latency?: number; // ms blocked on the upstream socket
	overhead_latency?: number; // Bifrost overhead (total minus upstream), ms
	overhead_breakdown?: OverheadBucket[]; // per-span self-time decomposition of overhead (microseconds)
	bytes_out: number;
	cost?: number;
	cost_per_request?: number;
	cost_per_unit?: number;
	extracted_cost?: number;
	units?: number;
	billed_units?: number;
	input_tokens?: number;
	output_tokens?: number;
	total_tokens?: number;
	job_id?: string;
	job_status?: string;
	parent_log_id?: string;
	user_id?: string;
	user_name?: string;
	virtual_key_id?: string;
	virtual_key_name?: string;
	team_id?: string;
	team_name?: string;
	customer_id?: string;
	customer_name?: string;
	business_unit_id?: string;
	business_unit_name?: string;
	project_id?: string;
	project_name?: string;
	user_agent?: string; // Raw HTTP User-Agent of the calling client
	app?: string; // Backend-detected client app derived from user_agent
	request_id: string;
	metadata?: Record<string, unknown>; // logging headers, x-bf-lh-* labels, x-bf-dim-* dimensions and the key's metadata, as on LLM and MCP rows
	request_headers?: Record<string, string>; // the headers the matched route matches on
	response_headers?: Record<string, string>;
	request_body?: string;
	response_body?: string;
	content_hidden: boolean;
	error_details?: BifrostError;
	plugin_logs?: string; // JSON string of plugin execution logs grouped by plugin name
	budget_ids?: string[];
	rate_limit_ids?: string[];
}

export interface RelayLogFilters {
	relay_names?: string[];
	methods?: string[];
	statuses?: string[];
	kinds?: string[];
	virtual_key_ids?: string[];
	user_ids?: string[];
	team_ids?: string[];
	customer_ids?: string[];
	business_unit_ids?: string[];
	project_ids?: string[];
	apps?: string[];
	billable?: boolean;
	min_cost?: number;
	max_cost?: number;
	/** Exact request-id lookup; set by the header's "Request ID" search mode. */
	request_id?: string;
	search?: string;
	period?: string;
	start_time?: string;
	end_time?: string;
}

export interface RelayLogPagination {
	limit: number;
	offset: number;
	sort_by: "timestamp" | "latency" | "cost";
	order: "asc" | "desc";
	total_count?: number;
}

export interface RelayLogSearchResponse {
	logs: RelayLog[];
	pagination: RelayLogPagination;
	has_logs: boolean;
}

export interface RelayLogStats {
	total_requests: number;
	success_count: number;
	error_count: number;
	success_rate: number;
	average_latency: number;
	total_cost: number;
	total_units: number;
	total_bytes_out: number;
}

/** GET /api/relay-logs/stats with compare_to_previous=true: the window's stats plus the preceding window's. */
export interface RelayLogStatsResponse extends RelayLogStats {
	previous?: RelayLogStats;
	has_previous_period?: boolean;
}

export interface RelayHistogramBucket {
	timestamp: string;
	count: number;
	success: number;
	error: number;
	cancelled: number;
	cost: number;
	avg_latency: number; // ms, 0 when nothing was timed
	units: number; // billed units
	bytes_out: number;
}

export interface RelayHistogramResponse {
	buckets: RelayHistogramBucket[];
	bucket_size_seconds: number;
}

/** A named entity the filter sidebar lists by id. */
export interface RelayLogFilterEntity {
	id: string;
	name: string;
}

/**
 * The dimensions GET /api/relay-logs/filterdata can return. A request names the ones it wants with
 * `dimensions=`, so the response is partial: every field is present only when asked for.
 */
export interface RelayLogFilterData {
	relay_names: string[];
	methods: string[];
	apps: string[];
	virtual_keys: RelayLogFilterEntity[];
	users: RelayLogFilterEntity[];
	teams: RelayLogFilterEntity[];
	customers: RelayLogFilterEntity[];
	business_units: RelayLogFilterEntity[];
	projects: RelayLogFilterEntity[];
}

export type RelayLogFilterDimension = keyof RelayLogFilterData;

/** @deprecated Use RelayLogFilterData; kept so older imports keep compiling. */
export type RelayFilterData = RelayLogFilterData;