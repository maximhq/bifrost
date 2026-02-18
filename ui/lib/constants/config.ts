import { BaseProvider, ConcurrencyAndBufferSize, NetworkConfig } from "@/lib/types/config";
import { ProviderName } from "./logs";

/**
 * Parse a date string in YYYY-MM-DD format with strict validation.
 * Returns null if the string is empty, malformed, or represents an invalid date.
 */
function parseTrialExpiry(dateStr: string | undefined): Date | null {
	if (!dateStr || !dateStr.trim()) return null;

	// Strict format check: YYYY-MM-DD
	const dateRegex = /^\d{4}-\d{2}-\d{2}$/;
	if (!dateRegex.test(dateStr)) return null;

	const [year, month, day] = dateStr.split("-").map(Number);
	const date = new Date(year, month - 1, day);

	// Validate the date components match (catches invalid dates like 2024-02-30)
	if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day) {
		return null;
	}

	return date;
}

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
	huggingface: "e.g. sambanova/meta-llama/Llama-3.1-8B-Instruct, nebius/Qwen/Qwen3-Embedding-8B",
	mistral: "e.g. mistral-7b-instruct, mixtral-8x7b",
	openrouter: "e.g. openai/gpt-4, anthropic/claude-3-haiku",
	sgl: "e.g. sgl-2, sgl-vision",
	parasail: "e.g. parasail-2, parasail-vision",
	elevenlabs: "e.g. eleven_multilingual_v2, eleven_turbo_v2",
	perplexity: "e.g. sonar-pro, sonar-deep-research",
	ollama: "e.g. llama3.1, llama2",
	openai: "e.g. gpt-4, gpt-4o, gpt-4o-mini, gpt-3.5-turbo",
	vertex: "e.g. gemini-1.5-pro, text-bison, chat-bison",
	nebius: "e.g. openai/gpt-oss-120b, google/gemma-2-9b-it-fast, Qwen/Qwen2.5-VL-72B-Instruct",
	xai: "e.g. grok-4-0709, grok-3-mini, grok-3, grok-2-vision-1212",
	replicate: "e.g. meta/llama3-1-8b-instruct, black-forest-labs/flux-dev",
	vllm: "e.g. Qwen/Qwen3-0.6B, Qwen/Qwen3-1.5B",
	runway: "e.g. gen4_turbo_image_to_video, gen3a_turbo_image_to_video",
	sapaicore: "e.g. gpt-4o, anthropic--claude-3-5-sonnet, gemini-1.5-pro",
};

export const isKeyRequiredByProvider: Record<ProviderName, boolean> = {
	anthropic: true,
	azure: true,
	bedrock: true,
	cerebras: true,
	cohere: true,
	gemini: true,
	groq: true,
	huggingface: true,
	mistral: true,
	openrouter: true,
	sgl: false,
	parasail: true,
	elevenlabs: true,
	ollama: false,
	openai: true,
	vertex: true,
	perplexity: true,
	nebius: true,
	xai: true,
	replicate: true,
	runway: true,
	vllm: false,
	sapaicore: true,
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
		"image_generation",
		"image_generation_stream",
		"image_edit",
		"image_edit_stream",
		"image_variation",
		"count_tokens",
		"video_generation",
		"video_retrieve",
		"video_download",
		"video_delete",
		"video_list",
		"video_remix",
	],
	anthropic: ["list_models", "chat_completion", "chat_completion_stream", "responses", "responses_stream", "count_tokens"],
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
		"image_generation",
		"image_edit",
		"count_tokens",
		"video_generation",
		"video_retrieve",
		"video_download",
		"video_delete",
		"video_list",
		"video_remix",
	],
	cohere: ["list_models", "chat_completion", "chat_completion_stream", "responses", "responses_stream", "embedding", "count_tokens"],
	bedrock: [
		"list_models",
		"text_completion",
		"chat_completion",
		"chat_completion_stream",
		"responses",
		"responses_stream",
		"embedding",
		"image_generation",
		"image_edit",
		"image_variation",
	],
	replicate: [
		"list_models",
		"text_completion",
		"chat_completion",
		"chat_completion_stream",
		"responses",
		"responses_stream",
		"image_generation",
		"image_generation_stream",
		"image_edit",
		"image_edit_stream",
		"video_generation",
		"video_retrieve",
		"video_download",
		"video_delete",
		"video_list",
		"video_remix",
	],
};

export const IS_ENTERPRISE = process.env.NEXT_PUBLIC_IS_ENTERPRISE === "true";
export const TRIAL_EXPIRY = parseTrialExpiry(process.env.NEXT_PUBLIC_ENTERPRISE_TRIAL_EXPIRY);
