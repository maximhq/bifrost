// Known provider names array - centralized definition
export const KnownProvidersNames = [
	"anthropic",
	"azure",
	"bedrock",
	"cerebras",
	"cohere",
	"gemini",
	"groq",
	"huggingface",
	"mistral",
	"ollama",
	"openai",
	"openrouter",
	"parasail",
	"elevenlabs",
	"perplexity",
	"sgl",
	"vertex",
	"nebius",
	"xai",
	"replicate",
	"vllm",
	"runway",
	"sapaicore",
] as const;

// Local Provider type derived from KNOWN_PROVIDERS constant
export type ProviderName = (typeof KnownProvidersNames)[number];

export const ProviderNames: readonly ProviderName[] = KnownProvidersNames;

export const Statuses = ["success", "error", "processing", "cancelled"] as const;

export const RequestTypes = [
	"list_models",
	"text_completion",
	"text_completion_stream",
	"chat_completion",
	"chat_completion_stream",
	"responses",
	"responses_stream",
	"embedding",
	"rerank",
	"speech",
	"speech_stream",
	"transcription",
	"transcription_stream",
	"image_generation",
	"image_generation_stream",
	"image_edit",
	"image_edit_stream",
	"image_variation",
	"video_generation",
	"video_retrieve",
	"video_download",
	"video_delete",
	"video_list",
	"video_remix",
	"count_tokens",
	// Container operations
	"container_create",
	"container_list",
	"container_retrieve",
	"container_delete",
	// Container file operations
	"container_file_create",
	"container_file_list",
	"container_file_retrieve",
	"container_file_content",
	"container_file_delete",
] as const;

export const ProviderLabels: Record<ProviderName, string> = {
	openai: "OpenAI",
	anthropic: "Anthropic",
	azure: "Azure",
	bedrock: "AWS Bedrock",
	cohere: "Cohere",
	vertex: "Vertex AI",
	mistral: "Mistral AI",
	ollama: "Ollama",
	groq: "Groq",
	parasail: "Parasail",
	elevenlabs: "Elevenlabs",
	perplexity: "Perplexity",
	sgl: "SGLang",
	cerebras: "Cerebras",
	gemini: "Gemini",
	openrouter: "OpenRouter",
	huggingface: "HuggingFace",
	nebius: "Nebius Token Factory",
	xai: "xAI",
	replicate: "Replicate",
	vllm: "vLLM",
	runway: "Runway",
	sapaicore: "SAP AI Core",
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

export const StatusBarColors = {
	success: "bg-green-500",
	error: "bg-red-500",
	processing: "bg-blue-500",
	cancelled: "bg-gray-400",
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
	list_models: "List Models",
	text_completion: "Text",
	text_completion_stream: "Text Stream",
	chat_completion: "Chat",
	chat_completion_stream: "Chat Stream",
	responses: "Responses",
	responses_stream: "Responses Stream",

	embedding: "Embedding",
	rerank: "Rerank",

	speech: "Speech",
	speech_stream: "Speech Stream",

	transcription: "Transcription",
	transcription_stream: "Transcription Stream",

	image_generation: "Image Generation",
	image_generation_stream: "Image Generation Stream",
	image_edit: "Image Edit",
	image_edit_stream: "Image Edit Stream",
	image_variation: "Image Variation",
	video_generation: "Video Generation",
	video_retrieve: "Video Retrieve",
	video_download: "Video Download",
	video_delete: "Video Delete",
	video_list: "Video List",
	video_remix: "Video Remix",
	count_tokens: "Count Tokens",

	batch_create: "Batch Create",
	batch_list: "Batch List",
	batch_retrieve: "Batch Retrieve",
	batch_cancel: "Batch Cancel",
	batch_results: "Batch Results",

	file_upload: "File Upload",
	file_list: "File List",
	file_retrieve: "File Retrieve",
	file_delete: "File Delete",
	file_content: "File Content",

	// Container operations
	container_create: "Container Create",
	container_list: "Container List",
	container_retrieve: "Container Retrieve",
	container_delete: "Container Delete",

	// Container file operations
	container_file_create: "Container File Create",
	container_file_list: "Container File List",
	container_file_retrieve: "Container File Retrieve",
	container_file_content: "Container File Content",
	container_file_delete: "Container File Delete",
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
	list_models: "bg-green-100 text-green-800",
	text_completion: "bg-green-100 text-green-800",
	text_completion_stream: "bg-amber-100 text-amber-800",

	chat_completion: "bg-blue-100 text-blue-800",
	chat_completion_stream: "bg-yellow-100 text-yellow-800",

	responses: "bg-teal-100 text-teal-800",
	responses_stream: "bg-violet-100 text-violet-800",

	embedding: "bg-red-100 text-red-800",
	rerank: "bg-fuchsia-100 text-fuchsia-800",

	speech: "bg-purple-100 text-purple-800",
	speech_stream: "bg-pink-100 text-pink-800",

	transcription: "bg-orange-100 text-orange-800",
	transcription_stream: "bg-lime-100 text-lime-800",

	image_generation: "bg-indigo-100 text-indigo-800",
	image_generation_stream: "bg-sky-100 text-sky-800",
	image_edit: "bg-emerald-100 text-emerald-800",
	image_edit_stream: "bg-teal-100 text-teal-800",
	image_variation: "bg-violet-100 text-violet-800",
	video_generation: "bg-fuchsia-100 text-fuchsia-800",
	video_retrieve: "bg-blue-100 text-blue-800",
	video_download: "bg-purple-100 text-purple-800",
	video_delete: "bg-rose-100 text-rose-800",
	video_list: "bg-cyan-100 text-cyan-800",
	video_remix: "bg-pink-100 text-pink-800",
	count_tokens: "bg-cyan-100 text-cyan-800",

	// Container operations
	container_create: "bg-emerald-100 text-emerald-800",
	container_list: "bg-teal-100 text-teal-800",
	container_retrieve: "bg-cyan-100 text-cyan-800",
	container_delete: "bg-rose-100 text-rose-800",

	// Container file operations
	container_file_create: "bg-emerald-100 text-emerald-800",
	container_file_list: "bg-teal-100 text-teal-800",
	container_file_retrieve: "bg-cyan-100 text-cyan-800",
	container_file_content: "bg-sky-100 text-sky-800",
	container_file_delete: "bg-rose-100 text-rose-800",

	batch_create: "bg-green-100 text-green-800",
	batch_list: "bg-blue-100 text-blue-800",
	batch_retrieve: "bg-red-100 text-red-800",
	batch_cancel: "bg-yellow-100 text-yellow-800",
	batch_results: "bg-purple-100 text-purple-800",

	file_upload: "bg-pink-100 text-pink-800",
	file_list: "bg-lime-100 text-lime-800",
	file_retrieve: "bg-orange-100 text-orange-800",
	file_delete: "bg-red-100 text-red-800",
	file_content: "bg-blue-100 text-blue-800",
} as const;

export const RoutingEngineUsedLabels = {
	"routing-rule": "Routing Rule",
	governance: "Governance",
	loadbalancing: "Loadbalancing",
} as const;

export const RoutingEngineUsedColors = {
	"routing-rule": "bg-blue-100 text-blue-800",
	governance: "bg-green-100 text-green-800",
	loadbalancing: "bg-red-100 text-red-800",
} as const;

export type Status = (typeof Statuses)[number];
