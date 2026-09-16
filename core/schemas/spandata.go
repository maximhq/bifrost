package schemas

// Typed payload for LLM-call spans, replacing the map[string]any attribute bag on
// the hot path. Producers fill fields; connectors read fields and render their own
// wire format. Nothing here is pre-serialized — a connector that needs JSON
// marshals at its own edge, so the cost is only paid when that connector is loaded.
//
// Wire-key namespacing (prompt_token_details.* for chat vs input_token_details.*
// for Responses) is a rendering concern and lives in each connector's mapper, not
// here. RequestType is what a mapper switches on.
//
// The per-family params are the request's own *Parameters structs, held by
// pointer rather than projected into span-local copies. Anything the old
// attribute map derived from them (joined stop sequences, joined prompt arrays,
// formatted logit bias, joined multi-voice names) is a rendering concern too.

// LLMSpanData is the typed payload of an LLM-call span.
type LLMSpanData struct {
	// Identity
	Provider      ModelProvider
	RequestType   RequestType
	RequestModel  string
	ResponseModel string
	Alias         string // originally requested model when a fallback or alias changed it

	// Response envelope
	ResponseID        string
	Object            string
	SystemFingerprint string
	ServiceTier       string
	Created           int64
	FinishReasons     []string

	// Content. Typed, never pre-marshalled.
	MessageCount   int
	InputMessages  []MessageSummary
	OutputMessages []MessageSummary
	ReasoningText  string

	// Normalized across every request family via BifrostResponse.NormalizedUsage,
	// so the tracer and the logging plugin can never disagree on token counts.
	Usage *BifrostLLMUsage

	// Bifrost's computed cost, from CalculateCostBreakdown — the same call the
	// logging plugin makes, so a span and its log row cannot disagree. Distinct
	// from Usage.Cost, which is provider-reported and nil for most providers;
	// this supersedes it and falls back to it when the catalog cannot price the
	// model, so it may alias Usage.Cost. Provenance is load-bearing: pricing
	// itself branches on whether the provider reported a cost.
	Cost *BifrostCost

	// Streaming
	TimeToFirstChunkMs *float64
	TotalChunks        int

	Error *SpanError

	// Exactly one non-nil, mirroring BifrostRequest's discriminated union. These
	// alias the request's own params; treat them as read-only.
	//
	// Batch and file spans are not modelled here — they are cold-path and their
	// attributes stay in Extra until there is a reason to type them.
	Chat           *ChatParameters
	Responses      *ResponsesParameters
	TextCompletion *TextCompletionParameters
	Embedding      *EmbeddingParameters
	Speech         *SpeechParameters
	Transcription  *TranscriptionParameters

	// Params the Responses API echoes back on the response. Distinct from
	// Responses above so a mapper can emit gen_ai.response.* for these and
	// gen_ai.request.* for the request side.
	ResponsesEcho *ResponsesParameters

	// Request inputs that live on the request rather than its params.
	TextPrompt     *TextCompletionInput
	EmbeddingInput *EmbeddingInput
	SpeechInput    *SpeechInput

	// ExtraParams, x-bf-dim-* dimensions, and cold-path families. Anything with
	// an Attr* constant belongs in a field, not here.
	Extra map[string]any
}

// SpanError is the error detail stamped on a failed LLM span. It reuses
// ErrorField rather than re-flattening message/type/code, and deliberately does
// not hold the whole BifrostError — ExtraFields there carries raw request and
// response payloads that must not be retained on a span by accident.
type SpanError struct {
	Detail     *ErrorField
	StatusCode *int
	Type       string // classified type from ClassifyErrorType
}

// SpanEnrichment carries the governance and identity dimensions. It hangs off
// Span rather than LLMSpanData: these are context-sourced and apply to any span,
// not just LLM calls. Field order mirrors EnrichmentDims so the conformance test
// can pin the two together.
//
// This overlaps routing/rules.GovernanceScope (a 7-field subset) and the fields
// logging carries on its queue message; it is the superset both should adopt.
type SpanEnrichment struct {
	VirtualKeyID     string
	VirtualKeyName   string
	SelectedKeyID    string
	SelectedKeyName  string
	RoutingRuleID    string
	RoutingRuleName  string
	TeamID           string
	TeamName         string
	CustomerID       string
	CustomerName     string
	BusinessUnitID   string
	BusinessUnitName string
	ProjectID        string
	ProjectName      string
	UserID           string
	UserName         string
	UserEmail        string

	TeamIDs           []string
	TeamNames         []string
	CustomerIDs       []string
	CustomerNames     []string
	BusinessUnitIDs   []string
	BusinessUnitNames []string

	RoutingEnginesUsed  []string
	ComplexityTier      string
	ComplexityMechanism string
	ComplexityScore     *float64

	Retries int
	// Nil when the request never carried a fallback index.
	FallbackIndex *int
}

// ---------------------------------------------------------------------------
// Message summaries
//
// Moved here from framework/tracing so the span payload can be typed in
// core/schemas. framework/tracing keeps aliases, so existing importers (the
// Datadog connector among them) compile unchanged.
//
// This is the one genuinely new normalization in this file: it unifies
// ChatMessage and ResponsesMessage into a single shape connectors can render
// without switching on request family. Unlike usage — where BifrostLLMUsage
// already is the canonical normalized type — no such type exists for messages.
// ---------------------------------------------------------------------------

// MessageSummary is a summarized chat or responses message carried on a span.
type MessageSummary struct {
	Role             string                   `json:"role"`
	Content          string                   `json:"content"`
	Attachments      []AttachmentSummary      `json:"attachments,omitempty"`
	ToolCalls        []ToolCallSummary        `json:"tool_calls,omitempty"`
	Reasoning        string                   `json:"reasoning,omitempty"`
	ReasoningDetails []ReasoningDetailSummary `json:"reasoning_details,omitempty"`
	Audio            *AudioSummary            `json:"audio,omitempty"`
	Refusal          string                   `json:"refusal,omitempty"`
	ToolCallID       string                   `json:"tool_call_id,omitempty"`
}

// AttachmentKind classifies a non-text content block.
type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentAudio AttachmentKind = "audio"
	AttachmentFile  AttachmentKind = "file"
)

// AttachmentSummary describes a non-text content block. References (http(s) URLs,
// provider file IDs) are carried verbatim; inline payloads (data: URLs, base64
// audio and file data) are reduced to media type and size unless a connector has
// opted into inline bytes.
//
// Spans outlive the request across an async flush, so copying base64 blobs here
// by default would pin megabytes per request until every connector has drained.
type AttachmentSummary struct {
	Kind      AttachmentKind `json:"kind"`
	MediaType string         `json:"media_type,omitempty"` // image/png, audio/mp3, application/pdf
	URL       string         `json:"url,omitempty"`        // reference only; never a data: URL
	FileID    string         `json:"file_id,omitempty"`
	Filename  string         `json:"filename,omitempty"`
	Detail    string         `json:"detail,omitempty"` // image detail hint: low | high | auto
	Format    string         `json:"format,omitempty"` // audio container: mp3 | wav

	// Inline is true when the source carried bytes rather than a reference.
	// ByteSize is the decoded payload size; 0 for a reference.
	Inline   bool `json:"inline,omitempty"`
	ByteSize int  `json:"byte_size,omitempty"`

	// Data holds the base64 payload, and is populated only when a connector
	// declared it reads inline attachments and the payload is under the cap.
	Data string `json:"data,omitempty"`
}

// ToolCallSummary is a summarized tool call carried on a span.
type ToolCallSummary struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

// ReasoningDetailSummary is a summarized reasoning detail carried on a span.
type ReasoningDetailSummary struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// AudioSummary is summarized audio output carried on a span.
type AudioSummary struct {
	ID         string `json:"id,omitempty"`
	Transcript string `json:"transcript,omitempty"`
}
