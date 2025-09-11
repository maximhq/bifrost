// Known provider names array - centralized definition
export const KNOWN_PROVIDERS = [
	"openai",
	"anthropic",
	"azure",
	"bedrock",
	"cohere",
	"vertex",
	"mistral",
	"ollama",
	"groq",
	"parasail",
	"sgl",
	"cerebras",
	"gemini",
	"openrouter",
] as const;

// Local Provider type derived from KNOWN_PROVIDERS constant
export type Provider = (typeof KNOWN_PROVIDERS)[number];

export const PROVIDERS: readonly Provider[] = KNOWN_PROVIDERS;

export const STATUSES = ["success", "error", "processing", "cancelled"] as const;

export const REQUEST_TYPES = [
	"chat.completion",
	"text.completion",
	"embedding",
	"list",
	"audio.speech",
	"audio.transcription",
	"chat.completion.chunk",
	"audio.speech.chunk",
	"audio.transcription.chunk",
] as const;

export const PROVIDER_LABELS: Record<Provider, string> = {
	openai: "OpenAI",
	anthropic: "Anthropic",
	azure: "Azure OpenAI",
	bedrock: "AWS Bedrock",
	cohere: "Cohere",
	vertex: "Vertex AI",
	mistral: "Mistral AI",
	ollama: "Ollama",
	groq: "Groq",
	parasail: "Parasail",
	sgl: "SGLang",
	cerebras: "Cerebras",
	gemini: "Gemini",
	openrouter: "OpenRouter",
} as const;

// Helper function to get provider label, supporting custom providers
export const getProviderLabel = (provider: string): string => {
	// Use hasOwnProperty for safe lookup without checking prototype chain
	if (Object.prototype.hasOwnProperty.call(PROVIDER_LABELS, provider.toLowerCase().trim() as Provider)) {
		return PROVIDER_LABELS[provider.toLowerCase().trim() as Provider];
	}

	// For custom providers, return the original provider name as is
	return provider;
};

export const STATUS_COLORS = {
	success: "bg-green-100 text-green-800",
	error: "bg-red-100 text-red-800",
	processing: "bg-blue-100 text-blue-800",
	cancelled: "bg-gray-100 text-gray-800",
} as const;

export const REQUEST_TYPE_LABELS = {
	"chat.completion": "Chat",
	"text.completion": "Text",
	embedding: "Embedding",
	list: "List",
	"audio.speech": "Speech",
	"audio.transcription": "Transcription",
	"chat.completion.chunk": "Chat Stream",
	"audio.speech.chunk": "Speech Stream",
	"audio.transcription.chunk": "Transcription Stream",
} as const;

export const REQUEST_TYPE_COLORS = {
	"chat.completion": "bg-blue-100 text-blue-800",
	"text.completion": "bg-green-100 text-green-800",
	embedding: "bg-red-100 text-red-800",
	list: "bg-red-100 text-red-800",
	"audio.speech": "bg-purple-100 text-purple-800",
	"audio.transcription": "bg-orange-100 text-orange-800",
	"chat.completion.chunk": "bg-yellow-100 text-yellow-800",
	"audio.speech.chunk": "bg-pink-100 text-pink-800",
	"audio.transcription.chunk": "bg-lime-100 text-lime-800",
} as const;

export type Status = (typeof STATUSES)[number];
