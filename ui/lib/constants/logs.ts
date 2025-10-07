// Known provider names array - centralized definition
export const KnownProvidersNames = [
	"anthropic",
	"azure",
	"bedrock",
	"cerebras",
	"cohere",
	"gemini",
	"groq",
	"mistral",
	"ollama",
	"openai",
	"openrouter",
	"parasail",
	"sgl",
	"vertex",
] as const;

// Local Provider type derived from KNOWN_PROVIDERS constant
export type ProviderName = (typeof KnownProvidersNames)[number];

export const ProviderNames: readonly ProviderName[] = KnownProvidersNames;

export const Statuses = ["success", "error", "processing", "cancelled"] as const;

export const RequestTypes = [
	"chat.completion",
	"text.completion",
	"text_completion",
	"completion",
	"embedding",
	"list",
	"audio.speech",
	"audio.transcription",
	"chat.completion.chunk",
	"audio.speech.chunk",
	"audio.transcription.chunk",
	"response",
	"response.stream",
] as const;

export const ProviderLabels: Record<ProviderName, string> = {
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
	if (Object.prototype.hasOwnProperty.call(ProviderLabels, provider.toLowerCase().trim() as ProviderName)) {
		return ProviderLabels[provider.toLowerCase().trim() as ProviderName];
	}

	// For custom providers, return the original provider name as is
	return provider;
};

export const StatusColors = {
	success: "bg-green-100 text-green-800",
	error: "bg-red-100 text-red-800",
	processing: "bg-blue-100 text-blue-800",
	cancelled: "bg-gray-100 text-gray-800",
} as const;

export const RequestTypeLabels = {
	"chat.completion": "Chat",
	text_completion: "Text",
	response: "Responses",
	completion: "Completion",
	"text.completion": "Text",
	embedding: "Embedding",
	list: "List",
	"audio.speech": "Speech",
	"audio.transcription": "Transcription",
	"chat.completion.chunk": "Chat Stream",
	"audio.speech.chunk": "Speech Stream",
	"audio.transcription.chunk": "Transcription Stream",
} as const;

export const RequestTypeColors = {
	"chat.completion": "bg-blue-100 text-blue-800",
	response: "bg-teal-100 text-teal-800",
	text_completion: "bg-green-100 text-green-800",
	"text.completion": "bg-green-100 text-green-800",
	embedding: "bg-red-100 text-red-800",
	list: "bg-red-100 text-red-800",
	"audio.speech": "bg-purple-100 text-purple-800",
	"audio.transcription": "bg-orange-100 text-orange-800",
	"chat.completion.chunk": "bg-yellow-100 text-yellow-800",
	"audio.speech.chunk": "bg-pink-100 text-pink-800",
	"audio.transcription.chunk": "bg-lime-100 text-lime-800",
	completion: "bg-yellow-100 text-yellow-800",
} as const;

export type Status = (typeof Statuses)[number];
