export const DEFAULT_NETWORK_CONFIG = {
	base_url: "",
	default_request_timeout_in_seconds: 30,
	max_retries: 0,
	retry_backoff_initial: 1000,
	retry_backoff_max: 10000,
};

export const DEFAULT_PERFORMANCE_CONFIG = {
	concurrency: 10,
	buffer_size: 100,
};

export const MCP_STATUS_COLORS = {
	connected: "bg-green-100 text-green-800",
	error: "bg-red-100 text-red-800",
	disconnected: "bg-gray-100 text-gray-800",
} as const;
