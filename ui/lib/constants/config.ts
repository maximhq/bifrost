import { AllowedRequests, BaseProvider, ConcurrencyAndBufferSize, NetworkConfig } from "@/lib/types/config";
import { ProviderName } from "./logs";

// Model placeholders based on provider type
export const ModelPlaceholders = {
	default: "e.g. gpt-4, gpt-3.5-turbo. Leave blank for all models.",
	anthropic: "e.g. claude-3-haiku, claude-2.1",
	azure: "e.g. gpt-4, gpt-35-turbo (must match deployment names)",
	bedrock: "e.g. claude-v2, titan-text-express-v1, ai21-j2-mid",
	cerebras: "e.g. cerebras-2, cerebras-2-vision",
	cohere: "e.g. command-r, command-r-plus",
	gemini: "e.g. gemini-1.5-pro, gemini-1.5-flash",
	groq: "e.g. llama3-70b-8192, mixtral-8x7b-32768",
	mistral: "e.g. mistral-7b-instruct, mixtral-8x7b",
	openrouter: "e.g. openai/gpt-4, anthropic/claude-3-haiku",
	sgl: "e.g. sgl-2, sgl-vision",
	parasail: "e.g. parasail-2, parasail-vision",
	elevenlabs: "e.g. eleven-v3, eleven-multilingual-v2",
	perplexity: "e.g. sonar-pro, sonar-deep-research",
	ollama: "e.g. llama3.1, llama2",
	openai: "e.g. gpt-4, gpt-4o, gpt-4o-mini, gpt-3.5-turbo",
	vertex: "e.g. gemini-1.5-pro, text-bison, chat-bison",
};

export const isKeyRequiredByProvider: Record<ProviderName, boolean> = {
	anthropic: true,
	azure: true,
	bedrock: true,
	cerebras: true,
	cohere: true,
	gemini: true,
	groq: true,
	mistral: true,
	openrouter: true,
	sgl: false,
	parasail: true,
	elevenlabs: true,
	ollama: false,
	openai: true,
	vertex: true,
	perplexity: true,
};

export const DefaultNetworkConfig = {
	base_url: "",
	default_request_timeout_in_seconds: 30,
	max_retries: 0,
	retry_backoff_initial: 1000,
	retry_backoff_max: 10000,
} satisfies NetworkConfig;

export const DefaultPerformanceConfig = {
	concurrency: 1000,
	buffer_size: 5000,
} satisfies ConcurrencyAndBufferSize;

export const MCP_STATUS_COLORS = {
	connected: "bg-green-100 text-green-800",
	error: "bg-red-100 text-red-800",
	disconnected: "bg-gray-100 text-gray-800",
} as const;

// Mapping of what IS supported by each base provider
export const PROVIDER_SUPPORTED_REQUESTS: Record<BaseProvider, string[]> = {
	openai: [
		"list_models",
        "text_completion",
        "text_completion_stream",
        "chat_completion",
        "chat_completion_stream",
        "responses",
        "responses_stream",
        "embedding",
        "speech",
        "speech_stream",
        "transcription",
        "transcription_stream",
    ],
	anthropic: [
		"list_models",
		"chat_completion",
		"chat_completion_stream",
		"responses",
		"responses_stream",
	],
	gemini: [
		"list_models",
		"chat_completion",
		"chat_completion_stream",
		"responses",
		"responses_stream",
		"embedding",
		"transcription",
		"transcription_stream",
        "speech",
        "speech_stream",
	],
	cohere: [
		"list_models",
		"chat_completion",
		"chat_completion_stream",
		"responses",
		"responses_stream",
		"embedding",
	],
	bedrock: [
		"list_models",
		"text_completion",
		"chat_completion",
		"chat_completion_stream",
		"responses",
		"responses_stream",
		"embedding",
	],
};


export const IS_ENTERPRISE = process.env.NEXT_PUBLIC_IS_ENTERPRISE === "true";
