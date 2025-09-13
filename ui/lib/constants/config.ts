import { AllowedRequests, ConcurrencyAndBufferSize, NetworkConfig } from "@/lib/types/config";

export const DefaultNetworkConfig = {
	base_url: "",
	default_request_timeout_in_seconds: 30,
	max_retries: 0,
	retry_backoff_initial: 1000,
	retry_backoff_max: 10000,
} satisfies NetworkConfig;

export const DefaultPerformanceConfig = {
	concurrency: 1000,
	buffer_size: 10000,
} satisfies ConcurrencyAndBufferSize;

export const MCP_STATUS_COLORS = {
	connected: "bg-green-100 text-green-800",
	error: "bg-red-100 text-red-800",
	disconnected: "bg-gray-100 text-gray-800",
} as const;

export const DEFAULT_ALLOWED_REQUESTS = {
	text_completion: true,
	chat_completion: true,
	chat_completion_stream: true,
	embedding: true,
	speech: true,
	speech_stream: true,
	transcription: true,
	transcription_stream: true,
} as const satisfies Required<AllowedRequests>;
