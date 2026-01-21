// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"sync"
	"time"
)

// Trace represents a distributed trace that captures the full lifecycle of a request
type Trace struct {
	TraceID    string         // Unique identifier for this trace
	ParentID   string         // Parent trace ID from incoming W3C traceparent header
	RootSpan   *Span          // The root span of this trace
	Spans      []*Span        // All spans in this trace
	StartTime  time.Time      // When the trace started
	EndTime    time.Time      // When the trace completed
	Attributes map[string]any // Additional attributes for the trace
	mu         sync.Mutex     // Mutex for thread-safe span operations
}

// AddSpan adds a span to the trace in a thread-safe manner
func (t *Trace) AddSpan(span *Span) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Spans = append(t.Spans, span)
}

// GetSpan retrieves a span by ID
func (t *Trace) GetSpan(spanID string) *Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, span := range t.Spans {
		if span.SpanID == spanID {
			return span
		}
	}
	return nil
}

// Reset clears the trace for reuse from pool
func (t *Trace) Reset() {
	t.TraceID = ""
	t.ParentID = ""
	t.RootSpan = nil
	t.Spans = t.Spans[:0]
	t.StartTime = time.Time{}
	t.EndTime = time.Time{}
	t.Attributes = nil
}

// Span represents a single operation within a trace
type Span struct {
	SpanID     string         // Unique identifier for this span
	ParentID   string         // Parent span ID (empty for root span)
	TraceID    string         // The trace this span belongs to
	Name       string         // Name of the operation
	Kind       SpanKind       // Type of span (LLM call, plugin, etc.)
	StartTime  time.Time      // When the span started
	EndTime    time.Time      // When the span completed
	Status     SpanStatus     // Status of the operation
	StatusMsg  string         // Optional status message (for errors)
	Attributes map[string]any // Additional attributes for the span
	Events     []SpanEvent    // Events that occurred during the span
	mu         sync.Mutex     // Mutex for thread-safe attribute operations
}

// SetAttribute sets an attribute on the span in a thread-safe manner
func (s *Span) SetAttribute(key string, value any) {
	if value == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Attributes == nil {
		s.Attributes = make(map[string]any)
	}
	s.Attributes[key] = value
}

// AddEvent adds an event to the span in a thread-safe manner
func (s *Span) AddEvent(event SpanEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Events = append(s.Events, event)
}

// End marks the span as complete with the given status
func (s *Span) End(status SpanStatus, statusMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.EndTime = time.Now()
	s.Status = status
	s.StatusMsg = statusMsg
}

// Reset clears the span for reuse from pool
func (s *Span) Reset() {
	s.SpanID = ""
	s.ParentID = ""
	s.TraceID = ""
	s.Name = ""
	s.Kind = SpanKindUnspecified
	s.StartTime = time.Time{}
	s.EndTime = time.Time{}
	s.Status = SpanStatusUnset
	s.StatusMsg = ""
	s.Attributes = nil
	s.Events = s.Events[:0]
}

// SpanEvent represents a time-stamped event within a span
type SpanEvent struct {
	Name       string         // Name of the event
	Timestamp  time.Time      // When the event occurred
	Attributes map[string]any // Additional attributes for the event
}

// SpanKind represents the type of operation a span represents
// These are LLM-specific kinds designed for AI gateway observability
type SpanKind string

const (
	// SpanKindUnspecified is the default span kind
	SpanKindUnspecified SpanKind = ""
	// SpanKindLLMCall represents a call to an LLM provider
	SpanKindLLMCall SpanKind = "llm.call"
	// SpanKindPlugin represents plugin execution (PreHook/PostHook)
	SpanKindPlugin SpanKind = "plugin"
	// SpanKindMCPTool represents an MCP tool invocation
	SpanKindMCPTool SpanKind = "mcp.tool"
	// SpanKindRetry represents a retry attempt
	SpanKindRetry SpanKind = "retry"
	// SpanKindFallback represents a fallback to another provider
	SpanKindFallback SpanKind = "fallback"
	// SpanKindHTTPRequest represents the root HTTP request span
	SpanKindHTTPRequest SpanKind = "http.request"
	// SpanKindEmbedding represents an embedding request
	SpanKindEmbedding SpanKind = "embedding"
	// SpanKindSpeech represents a text-to-speech request
	SpanKindSpeech SpanKind = "speech"
	// SpanKindTranscription represents a speech-to-text request
	SpanKindTranscription SpanKind = "transcription"
	// SpanKindInternal represents internal operations (key selection, etc.)
	SpanKindInternal SpanKind = "internal"
)

// SpanStatus represents the status of a span's operation
type SpanStatus string

const (
	// SpanStatusUnset indicates status has not been set
	SpanStatusUnset SpanStatus = "unset"
	// SpanStatusOk indicates the operation completed successfully
	SpanStatusOk SpanStatus = "ok"
	// SpanStatusError indicates the operation failed
	SpanStatusError SpanStatus = "error"
)

// LLM Attribute Keys (gen_ai.* namespace)
// These follow the OpenTelemetry semantic conventions for GenAI
// and are compatible with both OTEL and Datadog backends.
const (
	// Provider and Model Attributes
	AttrProviderName = "gen_ai.provider.name"
	AttrRequestModel = "gen_ai.request.model"

	// Request Parameter Attributes
	AttrMaxTokens        = "gen_ai.request.max_tokens"
	AttrTemperature      = "gen_ai.request.temperature"
	AttrTopP             = "gen_ai.request.top_p"
	AttrStopSequences    = "gen_ai.request.stop_sequences"
	AttrPresencePenalty  = "gen_ai.request.presence_penalty"
	AttrFrequencyPenalty = "gen_ai.request.frequency_penalty"
	AttrParallelToolCall = "gen_ai.request.parallel_tool_calls"
	AttrRequestUser      = "gen_ai.request.user"
	AttrBestOf           = "gen_ai.request.best_of"
	AttrEcho             = "gen_ai.request.echo"
	AttrLogitBias        = "gen_ai.request.logit_bias"
	AttrLogProbs         = "gen_ai.request.logprobs"
	AttrN                = "gen_ai.request.n"
	AttrSeed             = "gen_ai.request.seed"
	AttrSuffix           = "gen_ai.request.suffix"
	AttrDimensions       = "gen_ai.request.dimensions"
	AttrEncodingFormat   = "gen_ai.request.encoding_format"
	AttrLanguage         = "gen_ai.request.language"
	AttrPrompt           = "gen_ai.request.prompt"
	AttrResponseFormat   = "gen_ai.request.response_format"
	AttrFormat           = "gen_ai.request.format"
	AttrVoice            = "gen_ai.request.voice"
	AttrMultiVoiceConfig = "gen_ai.request.multi_voice_config"
	AttrInstructions     = "gen_ai.request.instructions"
	AttrSpeed            = "gen_ai.request.speed"
	AttrMessageCount     = "gen_ai.request.message_count"

	// Response Attributes
	AttrResponseID       = "gen_ai.response.id"
	AttrResponseModel    = "gen_ai.response.model"
	AttrFinishReason     = "gen_ai.response.finish_reason"
	AttrSystemFprint     = "gen_ai.response.system_fingerprint"
	AttrServiceTier      = "gen_ai.response.service_tier"
	AttrCreated          = "gen_ai.response.created"
	AttrObject           = "gen_ai.response.object"
	AttrTimeToFirstToken = "gen_ai.response.time_to_first_token"
	AttrTotalChunks      = "gen_ai.response.total_chunks"

	// Plugin Attributes (for aggregated streaming post-hook spans)
	AttrPluginInvocations     = "plugin.invocation_count"
	AttrPluginAvgDurationMs   = "plugin.avg_duration_ms"
	AttrPluginTotalDurationMs = "plugin.total_duration_ms"
	AttrPluginErrorCount      = "plugin.error_count"

	// Usage Attributes
	AttrPromptTokens     = "gen_ai.usage.prompt_tokens"
	AttrCompletionTokens = "gen_ai.usage.completion_tokens"
	AttrTotalTokens      = "gen_ai.usage.total_tokens"
	AttrInputTokens      = "gen_ai.usage.input_tokens"
	AttrOutputTokens     = "gen_ai.usage.output_tokens"
	AttrUsageCost        = "gen_ai.usage.cost"

	// Error Attributes
	AttrError     = "gen_ai.error"
	AttrErrorType = "gen_ai.error.type"
	AttrErrorCode = "gen_ai.error.code"

	// Input/Output Attributes
	AttrInputText      = "gen_ai.input.text"
	AttrInputMessages  = "gen_ai.input.messages"
	AttrInputSpeech    = "gen_ai.input.speech"
	AttrInputEmbedding = "gen_ai.input.embedding"
	AttrOutputMessages = "gen_ai.output.messages"

	// Bifrost Context Attributes
	AttrVirtualKeyID    = "gen_ai.virtual_key_id"
	AttrVirtualKeyName  = "gen_ai.virtual_key_name"
	AttrSelectedKeyID   = "gen_ai.selected_key_id"
	AttrSelectedKeyName = "gen_ai.selected_key_name"
	AttrTeamID          = "gen_ai.team_id"
	AttrTeamName        = "gen_ai.team_name"
	AttrCustomerID      = "gen_ai.customer_id"
	AttrCustomerName    = "gen_ai.customer_name"
	AttrNumberOfRetries = "gen_ai.number_of_retries"
	AttrFallbackIndex   = "gen_ai.fallback_index"

	// Responses API Request Attributes
	AttrPromptCacheKey      = "gen_ai.request.prompt_cache_key"
	AttrReasoningEffort     = "gen_ai.request.reasoning_effort"
	AttrReasoningSummary    = "gen_ai.request.reasoning_summary"
	AttrReasoningGenSummary = "gen_ai.request.reasoning_generate_summary"
	AttrSafetyIdentifier    = "gen_ai.request.safety_identifier"
	AttrStore               = "gen_ai.request.store"
	AttrTextVerbosity       = "gen_ai.request.text_verbosity"
	AttrTextFormatType      = "gen_ai.request.text_format_type"
	AttrTopLogProbs         = "gen_ai.request.top_logprobs"
	AttrToolChoiceType      = "gen_ai.request.tool_choice_type"
	AttrToolChoiceName      = "gen_ai.request.tool_choice_name"
	AttrTools               = "gen_ai.request.tools"
	AttrTruncation          = "gen_ai.request.truncation"

	// Responses API Response Attributes
	AttrRespInclude          = "gen_ai.responses.include"
	AttrRespMaxOutputTokens  = "gen_ai.responses.max_output_tokens"
	AttrRespMaxToolCalls     = "gen_ai.responses.max_tool_calls"
	AttrRespMetadata         = "gen_ai.responses.metadata"
	AttrRespPreviousRespID   = "gen_ai.responses.previous_response_id"
	AttrRespPromptCacheKey   = "gen_ai.responses.prompt_cache_key"
	AttrRespReasoningText    = "gen_ai.responses.reasoning"
	AttrRespReasoningEffort  = "gen_ai.responses.reasoning_effort"
	AttrRespReasoningGenSum  = "gen_ai.responses.reasoning_generate_summary"
	AttrRespSafetyIdentifier = "gen_ai.responses.safety_identifier"
	AttrRespStore            = "gen_ai.responses.store"
	AttrRespTemperature      = "gen_ai.responses.temperature"
	AttrRespTextVerbosity    = "gen_ai.responses.text_verbosity"
	AttrRespTextFormatType   = "gen_ai.responses.text_format_type"
	AttrRespTopLogProbs      = "gen_ai.responses.top_logprobs"
	AttrRespTopP             = "gen_ai.responses.top_p"
	AttrRespToolChoiceType   = "gen_ai.responses.tool_choice_type"
	AttrRespToolChoiceName   = "gen_ai.responses.tool_choice_name"
	AttrRespTruncation       = "gen_ai.responses.truncation"
	AttrRespTools            = "gen_ai.responses.tools"

	// Batch Operation Attributes
	AttrBatchID             = "gen_ai.batch.id"
	AttrBatchStatus         = "gen_ai.batch.status"
	AttrBatchObject         = "gen_ai.batch.object"
	AttrBatchEndpoint       = "gen_ai.batch.endpoint"
	AttrBatchInputFileID    = "gen_ai.batch.input_file_id"
	AttrBatchOutputFileID   = "gen_ai.batch.output_file_id"
	AttrBatchErrorFileID    = "gen_ai.batch.error_file_id"
	AttrBatchCompletionWin  = "gen_ai.batch.completion_window"
	AttrBatchCreatedAt      = "gen_ai.batch.created_at"
	AttrBatchExpiresAt      = "gen_ai.batch.expires_at"
	AttrBatchRequestsCount  = "gen_ai.batch.requests_count"
	AttrBatchDataCount      = "gen_ai.batch.data_count"
	AttrBatchResultsCount   = "gen_ai.batch.results_count"
	AttrBatchHasMore        = "gen_ai.batch.has_more"
	AttrBatchMetadata       = "gen_ai.batch.metadata"
	AttrBatchLimit          = "gen_ai.batch.limit"
	AttrBatchAfter          = "gen_ai.batch.after"
	AttrBatchBeforeID       = "gen_ai.batch.before_id"
	AttrBatchAfterID        = "gen_ai.batch.after_id"
	AttrBatchPageToken      = "gen_ai.batch.page_token"
	AttrBatchPageSize       = "gen_ai.batch.page_size"
	AttrBatchCountTotal     = "gen_ai.batch.request_counts.total"
	AttrBatchCountCompleted = "gen_ai.batch.request_counts.completed"
	AttrBatchCountFailed    = "gen_ai.batch.request_counts.failed"
	AttrBatchFirstID        = "gen_ai.batch.first_id"
	AttrBatchLastID         = "gen_ai.batch.last_id"
	AttrBatchInProgressAt   = "gen_ai.batch.in_progress_at"
	AttrBatchFinalizingAt   = "gen_ai.batch.finalizing_at"
	AttrBatchCompletedAt    = "gen_ai.batch.completed_at"
	AttrBatchFailedAt       = "gen_ai.batch.failed_at"
	AttrBatchExpiredAt      = "gen_ai.batch.expired_at"
	AttrBatchCancellingAt   = "gen_ai.batch.cancelling_at"
	AttrBatchCancelledAt    = "gen_ai.batch.cancelled_at"
	AttrBatchNextCursor     = "gen_ai.batch.next_cursor"

	// Transcription Response Attributes
	AttrInputTokenDetailsText  = "gen_ai.usage.input_token_details.text_tokens"
	AttrInputTokenDetailsAudio = "gen_ai.usage.input_token_details.audio_tokens"

	// File Operation Attributes
	AttrFileID             = "gen_ai.file.id"
	AttrFileObject         = "gen_ai.file.object"
	AttrFileFilename       = "gen_ai.file.filename"
	AttrFilePurpose        = "gen_ai.file.purpose"
	AttrFileBytes          = "gen_ai.file.bytes"
	AttrFileCreatedAt      = "gen_ai.file.created_at"
	AttrFileStatus         = "gen_ai.file.status"
	AttrFileStorageBackend = "gen_ai.file.storage_backend"
	AttrFileDataCount      = "gen_ai.file.data_count"
	AttrFileHasMore        = "gen_ai.file.has_more"
	AttrFileDeleted        = "gen_ai.file.deleted"
	AttrFileContentType    = "gen_ai.file.content_type"
	AttrFileContentBytes   = "gen_ai.file.content_bytes"
	AttrFileLimit          = "gen_ai.file.limit"
	AttrFileAfter          = "gen_ai.file.after"
	AttrFileOrder          = "gen_ai.file.order"
)
