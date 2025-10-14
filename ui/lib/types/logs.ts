// Types for the logs interface based on BifrostResponse schema

// Speech and Transcription types
export interface VoiceConfig {
	speaker: string;
	voice: string;
}

export interface SpeechInput {
	input: string;
	voice: string | VoiceConfig[];
	instructions?: string;
	response_format?: string; // Default is "mp3"
}

export interface TranscriptionInput {
	file: string; // base64 encoded (send empty string when using input_audio)
	language?: string;
	prompt?: string;
	response_format?: string; // Default is "json"
	format?: string;
}

export interface AudioTokenDetails {
	text_tokens: number;
	audio_tokens: number;
}

export interface AudioLLMUsage {
	input_tokens: number;
	input_tokens_details?: AudioTokenDetails;
	output_tokens: number;
	total_tokens: number;
}

export interface TranscriptionWord {
	word: string;
	start: number;
	end: number;
}

export interface TranscriptionSegment {
	id: number;
	seek: number;
	start: number;
	end: number;
	text: string;
	tokens: number[];
	temperature: number;
	avg_logprob: number;
	compression_ratio: number;
	no_speech_prob: number;
}

export interface TranscriptionLogProb {
	token: string;
	logprob: number;
	bytes: number[];
}

export interface TranscriptionUsage {
	type: string; // "tokens" or "duration"
	input_tokens?: number;
	input_token_details?: AudioTokenDetails;
	output_tokens?: number;
	total_tokens?: number;
	seconds?: number; // For duration-based usage
}

export interface BifrostSpeech {
	usage?: AudioLLMUsage;
	audio: string; // base64 encoded audio data
}

export interface BifrostTranscribe {
	text: string;
	logprobs?: TranscriptionLogProb[];
	usage?: TranscriptionUsage;
	// Non-streaming specific fields
	task?: string; // e.g., "transcribe"
	language?: string; // e.g., "english"
	duration?: number; // Duration in seconds
	words?: TranscriptionWord[];
	segments?: TranscriptionSegment[];
}

// Message content types
export type MessageContentType =
	| "text"
	| "image_url"
	| "input_audio"
	| "input_text"
	| "input_file"
	| "output_text"
	| "refusal"
	| "reasoning";

export interface ContentBlock {
	type: MessageContentType;
	text?: string;
	image_url?: {
		url: string;
		detail?: string;
	};
	input_audio?: {
		data: string;
		format?: string;
	};
}

export type ChatMessageContent = string | ContentBlock[];

export interface ChatMessage {
	role: "assistant" | "user" | "system" | "chatbot" | "tool";
	content: ChatMessageContent;
	tool_call_id?: string;
	refusal?: string;
	annotations?: Annotation[];
	tool_calls?: ToolCall[]; // For backward compatibility, tool calls are now in the content
	thought?: string;
}

export interface BifrostEmbedding {
	index: number;
	object: string;
	embedding: string | number[] | number[][];
}

// Tool related types
export interface FunctionParameters {
	type: string;
	description?: string;
	required?: string[];
	properties?: Record<string, unknown>;
	enum?: string[];
}

export interface Function {
	name: string;
	description: string;
	parameters: FunctionParameters;
}

export interface Tool {
	id?: string;
	type: string;
	function: Function;
}

export interface FunctionCall {
	name?: string;
	arguments: string; // stringified JSON
}

export interface ToolCall {
	type?: string;
	id?: string;
	function: FunctionCall;
}

// Model parameters types
export interface ModelParameters {
	tool_choice?: unknown; // Can be string or object
	tools?: Tool[];
	temperature?: number;
	top_p?: number;
	top_k?: number;
	max_tokens?: number;
	stop_sequences?: string[];
	presence_penalty?: number;
	frequency_penalty?: number;
	parallel_tool_calls?: boolean;
	extra_params?: Record<string, unknown>;
}

// Token usage types
export interface TokenDetails {
	cached_tokens?: number;
	audio_tokens?: number;
}

export interface CompletionTokensDetails {
	reasoning_tokens?: number;
	audio_tokens?: number;
	accepted_prediction_tokens?: number;
	rejected_prediction_tokens?: number;
}

export interface LLMUsage {
	prompt_tokens: number;
	completion_tokens: number;
	total_tokens: number;
	prompt_tokens_details?: TokenDetails;
	completion_tokens_details?: CompletionTokensDetails;
}

export interface CacheDebug {
	cache_hit: boolean;
	cache_id?: string;
	hit_type?: string;
	provider_used?: string;
	model_used?: string;
	input_tokens?: number;
	threshold?: number;
	similarity?: number;
}

// Error types
export interface ErrorField {
	type?: string;
	code?: string;
	message: string;
	error?: unknown;
	param?: unknown;
	event_id?: string;
}

export interface BifrostError {
	event_id?: string;
	type?: string;
	is_bifrost_error: boolean;
	status_code?: number;
	error: ErrorField;
}

// Citation and Annotation types
export interface Citation {
	start_index: number;
	end_index: number;
	title: string;
	url?: string;
	sources?: unknown;
	type?: string;
}

export interface Annotation {
	type: string;
	url_citation: Citation;
}

// Main LogEntry interface matching backend
export interface LogEntry {
	id: string;
	object: string; // text.completion, chat.completion, embedding, audio.speech, or audio.transcription
	timestamp: string; // ISO string format from Go time.Time
	provider: string;
	model: string;
	input_history: ChatMessage[];
	output_message?: ChatMessage;
	responses_output?: ResponsesMessage[];
	embedding_output?: BifrostEmbedding[];
	params?: ModelParameters;
	speech_input?: SpeechInput;
	transcription_input?: TranscriptionInput;
	speech_output?: BifrostSpeech;
	transcription_output?: BifrostTranscribe;
	tools?: Tool[];
	tool_calls?: ToolCall[];
	latency?: number;
	token_usage?: LLMUsage;
	cache_debug?: CacheDebug;
	cost?: number; // Cost in dollars (total cost of the request - includes cache lookup cost)
	status: string; // "success" or "error"
	error_details?: BifrostError;
	stream: boolean; // true if this was a streaming response
	created_at: string; // ISO string format from Go time.Time - when the log was first created
	raw_response?: string; // Raw provider response
}

export interface LogFilters {
	providers?: string[];
	models?: string[];
	status?: string[];
	objects?: string[]; // For filtering by request type (chat.completion, text.completion, embedding)
	start_time?: string; // RFC3339 format
	end_time?: string; // RFC3339 format
	min_latency?: number;
	max_latency?: number;
	min_tokens?: number;
	max_tokens?: number;
	content_search?: string;
}

export interface Pagination {
	limit: number;
	offset: number;
	sort_by: "timestamp" | "latency" | "tokens" | "cost";
	order: "asc" | "desc";
}

export interface LogStats {
	total_requests: number;
	success_rate: number;
	average_latency: number;
	total_tokens: number;
	total_cost: number;
}

export interface LogsResponse {
	logs: LogEntry[];
	pagination: Pagination;
	stats: LogStats;
}

// Responses API types (for responses_output field)

// Message roles for responses
export type ResponsesMessageRoleType = "assistant" | "user" | "system" | "developer";

// Message types for responses
export type ResponsesMessageType =
	| "message"
	| "file_search_call"
	| "computer_call"
	| "computer_call_output"
	| "web_search_call"
	| "function_call"
	| "function_call_output"
	| "code_interpreter_call"
	| "local_shell_call"
	| "local_shell_call_output"
	| "mcp_call"
	| "custom_tool_call"
	| "custom_tool_call_output"
	| "image_generation_call"
	| "mcp_list_tools"
	| "mcp_approval_request"
	| "mcp_approval_responses"
	| "reasoning"
	| "item_reference"
	| "refusal";

// Content block types for responses
export type ResponsesMessageContentBlockType =
	| "input_text"
	| "input_image"
	| "input_file"
	| "input_audio"
	| "output_text"
	| "refusal"
	| "reasoning_text";

// Content blocks for responses messages
export interface ResponsesMessageContentBlock {
	type: ResponsesMessageContentBlockType;
	file_id?: string;
	text?: string;
	image_url?: string;
	detail?: string; // "low" | "high" | "auto"
	file_data?: string; // Base64 encoded file data
	file_url?: string;
	filename?: string;
	input_audio?: {
		format: string; // "mp3" or "wav"
		data: string; // base64 encoded audio data
	};
	annotations?: Array<{
		type: string;
		index?: number;
		file_id?: string;
		start_index?: number;
		end_index?: number;
		filename?: string;
		title?: string;
		url?: string;
		container_id?: string;
	}>;
	logprobs?: Array<{
		bytes: number[];
		logprob: number;
		token: string;
		top_logprobs: Array<{
			bytes: number[];
			logprob: number;
			token: string;
		}>;
	}>;
	refusal?: string;
}

// Message content - can be string or array of blocks
export type ResponsesMessageContent = string | ResponsesMessageContentBlock[];

// Tool message structure
export interface ResponsesToolMessage {
	call_id?: string;
	name?: string;
	arguments?: string;
	// Tool-specific fields would be added here based on tool type
	[key: string]: any; // For tool-specific properties
}

// Reasoning content
export interface ResponsesReasoningContent {
	type: "summary_text";
	text: string;
}

export interface ResponsesReasoning {
	summary: ResponsesReasoningContent[];
	encrypted_content?: string;
}

// Main response message structure
export interface ResponsesMessage {
	id?: string;
	type?: ResponsesMessageType;
	status?: string; // "in_progress" | "completed" | "incomplete" | "interpreting" | "failed"
	role?: ResponsesMessageRoleType;
	content?: ResponsesMessageContent;
	// Tool message fields (merged when type indicates tool usage)
	call_id?: string;
	name?: string;
	arguments?: string;
	// Reasoning fields (merged when type is "reasoning")
	summary?: ResponsesReasoningContent[];
	encrypted_content?: string;
	// Additional tool-specific fields
	[key: string]: any;
}

// Stream options for responses
export interface ResponsesStreamOptions {
	include_obfuscation?: boolean;
}

// Text configuration
export interface ResponsesTextConfig {
	format?: {
		type: "text" | "json_schema" | "json_object";
		json_schema?: {
			name: string;
			schema: Record<string, any>;
			type: string;
			description?: string;
			strict?: boolean;
		};
	};
	verbosity?: "low" | "medium" | "high";
}

// Tool choice configuration
export type ResponsesToolChoiceType =
	| "none"
	| "auto"
	| "any"
	| "required"
	| "function"
	| "allowed_tools"
	| "file_search"
	| "web_search_preview"
	| "computer_use_preview"
	| "code_interpreter"
	| "image_generation"
	| "mcp"
	| "custom";

export interface ResponsesToolChoiceStruct {
	type: ResponsesToolChoiceType;
	mode?: "none" | "auto" | "required";
	name?: string;
	server_label?: string;
	tools?: Array<{
		type: string;
		name?: string;
		server_label?: string;
	}>;
}

export type ResponsesToolChoice = string | ResponsesToolChoiceStruct;

// Tool configuration
export interface ResponsesToolFunction {
	parameters?: {
		type: string;
		description?: string;
		required?: string[];
		properties?: Record<string, unknown>;
		enum?: string[];
	};
	strict?: boolean;
}

export interface ResponsesTool {
	type: string;
	name?: string;
	description?: string;
	// Tool-specific configurations
	function?: ResponsesToolFunction;
	// Other tool type configs would be added here
	[key: string]: any;
}

// Reasoning parameters
export interface ResponsesParametersReasoning {
	effort?: "minimal" | "low" | "medium" | "high";
	/**
	 * @deprecated Use `summary` instead
	 */
	generate_summary?: string;
	summary?: "auto" | "concise" | "detailed";
}

// Response conversation structure
export type ResponsesResponseConversation = string | { id: string };

// Response instructions structure
export type ResponsesResponseInstructions = string | ResponsesMessage[];

// Response prompt structure
export interface ResponsesPrompt {
	id: string;
	variables: Record<string, any>;
	version?: string;
}

// Response usage information
export interface ResponsesResponseInputTokens {
	cached_tokens: number;
}

export interface ResponsesResponseOutputTokens {
	reasoning_tokens: number;
}

export interface ResponsesExtendedResponseUsage {
	input_tokens: number;
	input_tokens_details?: ResponsesResponseInputTokens;
	output_tokens: number;
	output_tokens_details?: ResponsesResponseOutputTokens;
}

export interface ResponsesResponseUsage extends ResponsesExtendedResponseUsage {
	total_tokens: number;
}

// Response error structure
export interface ResponsesResponseError {
	code: string;
	message: string;
}

// Response incomplete details
export interface ResponsesResponseIncompleteDetails {
	reason: string;
}

// WebSocket message types
export interface WebSocketLogMessage {
	type: "log";
	operation: "create" | "update";
	payload: LogEntry;
}
