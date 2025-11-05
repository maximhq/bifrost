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
	"perplexity",
	"sgl",
	"vertex",
] as const;

// Local Provider type derived from KNOWN_PROVIDERS constant
export type ProviderName = (typeof KnownProvidersNames)[number];

export const ProviderNames: readonly ProviderName[] = KnownProvidersNames;

export const Statuses = ["success", "error", "processing", "cancelled"] as const;

export const RequestTypes = [
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
	perplexity: "Perplexity",
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
	response: "Responses",
	"response.completion.chunk": "Responses Stream",
	completion: "Completion",
	"text.completion": "Text",
	list: "List",
	"audio.speech": "Speech",
	"audio.transcription": "Transcription",
	"chat.completion.chunk": "Chat Stream",
	"audio.speech.chunk": "Speech Stream",
	"audio.transcription.chunk": "Transcription Stream",

	// Request Types
	text_completion: "Text",
	text_completion_stream: "Text Stream",
	chat_completion: "Chat",
	chat_completion_stream: "Chat Stream",
	responses: "Responses",
	responses_stream: "Responses Stream",
	embedding: "Embedding",
	speech: "Speech",
	speech_stream: "Speech Stream",
	transcription: "Transcription",
	transcription_stream: "Transcription Stream",
} as const;

export const RequestTypeColors = {
	"chat.completion": "bg-blue-100 text-blue-800",
	response: "bg-teal-100 text-teal-800",
	"response.completion.chunk": "bg-violet-100 text-violet-800",
	"text.completion": "bg-green-100 text-green-800",
	list: "bg-red-100 text-red-800",
	"audio.speech": "bg-purple-100 text-purple-800",
	"audio.transcription": "bg-orange-100 text-orange-800",
	"chat.completion.chunk": "bg-yellow-100 text-yellow-800",
	"audio.speech.chunk": "bg-pink-100 text-pink-800",
	"audio.transcription.chunk": "bg-lime-100 text-lime-800",
	completion: "bg-yellow-100 text-yellow-800",

	// Request Types
	text_completion: "bg-green-100 text-green-800",
	text_completion_stream: "bg-amber-100 text-amber-800",
	chat_completion: "bg-blue-100 text-blue-800",
	chat_completion_stream: "bg-yellow-100 text-yellow-800",
	responses: "bg-teal-100 text-teal-800",
	responses_stream: "bg-violet-100 text-violet-800",
	embedding: "bg-red-100 text-red-800",
	speech: "bg-purple-100 text-purple-800",
	speech_stream: "bg-pink-100 text-pink-800",
	transcription: "bg-orange-100 text-orange-800",
	transcription_stream: "bg-lime-100 text-lime-800",
} as const;

export type Status = (typeof Statuses)[number];
