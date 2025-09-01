import { AllowedRequests } from "@/lib/types/config";

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

// Define the default allowed requests for each provider type
// This is based on the actual capabilities of each provider
export const PROVIDER_DEFAULT_ALLOWED_REQUESTS: Record<string, AllowedRequests> = {
	// OpenAI
	openai: {
		text_completion: false,
		chat_completion: true,
		chat_completion_stream: true,
		embedding: true,
		speech: true,
		speech_stream: true,
		transcription: true,
		transcription_stream: true,
	},

	// Anthropic
	anthropic: {
		text_completion: true,
		chat_completion: true,
		chat_completion_stream: true,
		embedding: false,
		speech: false,
		speech_stream: false,
		transcription: false,
		transcription_stream: false,
	},

	// Cohere
	cohere: {
		text_completion: false,
		chat_completion: true,
		chat_completion_stream: true,
		embedding: true,
		speech: false,
		speech_stream: false,
		transcription: false,
		transcription_stream: false,
	},

	// AWS Bedrock
	bedrock: {
		text_completion: true,
		chat_completion: true,
		chat_completion_stream: true,
		embedding: true,
		speech: false,
		speech_stream: false,
		transcription: false,
		transcription_stream: false,
	},

	// Gemini
	gemini: {
		text_completion: false,
		chat_completion: true,
		chat_completion_stream: true,
		embedding: true,
		speech: true,
		speech_stream: true,
		transcription: true,
		transcription_stream: true,
	},
};

// Helper function to get default allowed requests for a provider
export const getProviderDefaultAllowedRequests = (providerName: string): AllowedRequests => {
	const normalizedName = providerName.toLowerCase().trim();
	return PROVIDER_DEFAULT_ALLOWED_REQUESTS[normalizedName] ?? (DEFAULT_ALLOWED_REQUESTS as AllowedRequests);
};
