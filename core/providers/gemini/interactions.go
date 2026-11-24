package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// The Interactions API (POST /interactions) is Google's stateful generation surface: a run is
// stored under an id that a later turn names as previous_interaction_id, it can run in the
// background, it serves agents (Deep Research, Antigravity) as well as models, and its output is
// a list of steps rather than candidates. Bifrost maps it onto the Responses schema, whose
// lifecycle (previous_response_id, background, store, retrieve, cancel, delete) is the same
// shape, so governance, logging, pricing and fallbacks work unchanged. The converters in this
// file are pure: no logging, no HTTP.

const geminiInteractionsPath = "/interactions"

// Interactions step types.
const (
	GeminiInteractionsStepUserInput      = "user_input"
	GeminiInteractionsStepModelOutput    = "model_output"
	GeminiInteractionsStepThought        = "thought"
	GeminiInteractionsStepFunctionCall   = "function_call"
	GeminiInteractionsStepFunctionResult = "function_result"
)

// Interactions run statuses.
const (
	GeminiInteractionStatusInProgress           = "in_progress"
	GeminiInteractionStatusRequiresAction       = "requires_action"
	GeminiInteractionStatusCompleted            = "completed"
	GeminiInteractionStatusFailed               = "failed"
	GeminiInteractionStatusCancelled            = "cancelled"
	GeminiInteractionStatusIncomplete           = "incomplete"
	GeminiInteractionStatusRequiresContinuation = "requires_continuation"
)

// Interactions SSE event types.
const (
	GeminiInteractionsEventCreated      = "interaction.created"
	GeminiInteractionsEventStatusUpdate = "interaction.status_update"
	GeminiInteractionsEventCompleted    = "interaction.completed"
	GeminiInteractionsEventStepStart    = "step.start"
	GeminiInteractionsEventStepDelta    = "step.delta"
	GeminiInteractionsEventStepStop     = "step.stop"
	GeminiInteractionsEventError        = "error"
)

// Interactions step.delta types Bifrost maps; any other delta is carried raw.
const (
	geminiInteractionsDeltaText             = "text"
	geminiInteractionsDeltaThoughtSummary   = "thought_summary"
	geminiInteractionsDeltaThoughtSignature = "thought_signature"
	geminiInteractionsDeltaArguments        = "arguments_delta"
)

// geminiInteractionsAgentPrefixes name the agents the Interactions API serves. An agent is sent
// in the body's "agent" field, not "model", and runs only on this API.
var geminiInteractionsAgentPrefixes = []string{geminiDeepResearchAgentPrefix, "antigravity-"}

const geminiDeepResearchAgentPrefix = "deep-research-"

// applyGeminiInteractionsAgentConfig carries model settings over to an agent request. Google
// applies generation_config only with model, so a setting with an agent_config equivalent moves
// there (Deep Research's thinking summaries), the default tool_choice is dropped, and any other
// setting is refused rather than silently ignored.
func applyGeminiInteractionsAgentConfig(out *GeminiInteractionsRequest, config *GeminiInteractionsGenerationConfig) error {
	unsupported := ""
	switch {
	case config.MaxOutputTokens != nil:
		unsupported = "max_output_tokens"
	case config.Temperature != nil:
		unsupported = "temperature"
	case config.Seed != nil:
		unsupported = "seed"
	case len(config.StopSequences) > 0:
		unsupported = "stop_sequences"
	case config.ThinkingLevel != nil:
		unsupported = "reasoning effort"
	case len(config.ToolChoice) > 0 && string(config.ToolChoice) != `"auto"`:
		unsupported = "tool_choice"
	case config.ThinkingSummaries != nil && !strings.HasPrefix(out.Agent, geminiDeepResearchAgentPrefix):
		unsupported = "reasoning summary"
	}
	if unsupported != "" {
		return providerUtils.InvalidRequestErrorf("%s is not supported on the agent %q; it applies to models only", unsupported, out.Agent)
	}
	if config.ThinkingSummaries == nil {
		return nil
	}
	raw, err := providerUtils.MarshalSorted(map[string]string{"type": "deep-research", "thinking_summaries": *config.ThinkingSummaries})
	if err != nil {
		return err
	}
	out.AgentConfig = raw
	return nil
}

// newGeminiStatelessResponseID mints the response id of an Interactions run Google did not store. It
// has the generateContent prefix because it means the same: nothing is stored to continue from.
func newGeminiStatelessResponseID() string {
	return "resp_" + schemas.GetRandomString(50)
}

// isGenerateContentResponseID reports whether id was minted by Bifrost, for a generateContent
// Responses turn or a run Google did not store. No interaction exists for it, so Google could only
// reject it.
func isGenerateContentResponseID(id string) bool {
	return strings.HasPrefix(id, "resp_")
}

// trimGoogleModelPrefix removes a google/ publisher prefix.
func trimGoogleModelPrefix(model string) string {
	const prefix = "google/"
	model = strings.TrimSpace(model)
	if len(model) >= len(prefix) && strings.EqualFold(model[:len(prefix)], prefix) {
		return model[len(prefix):]
	}
	return model
}

// IsGeminiInteractionsAgent reports whether model names an Interactions agent rather than a model,
// with or without a google/ prefix.
func IsGeminiInteractionsAgent(model string) bool {
	return isGeminiInteractionsAgentName(trimGoogleModelPrefix(model))
}

// isGeminiInteractionsAgentName is IsGeminiInteractionsAgent for a name already without google/.
func isGeminiInteractionsAgentName(model string) bool {
	for _, prefix := range geminiInteractionsAgentPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// geminiInteractionsAgent returns the agent request runs, "" when it runs a model. Google hosts agents
// under names callers choose, so a name outside the known families is an agent only when the request
// says so: the typed route's agent field, or an agent field on /v1/responses, both kept in
// ExtraParams. The name returned is always the request's model, which is what governance checked; a
// field naming anything else (a fallback to a model) leaves the request a model request.
func geminiInteractionsAgent(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) string {
	if request == nil {
		return ""
	}
	model := trimGoogleModelPrefix(request.Model)
	if isGeminiInteractionsAgentName(model) {
		return model
	}
	if request.Params == nil || len(request.Params.ExtraParams) == 0 {
		return ""
	}
	named, ok := schemas.SafeExtractString(request.Params.ExtraParams["agent"])
	if !ok || named == "" {
		return ""
	}
	_, named = schemas.ParseModelString(named, "")
	named = trimGoogleModelPrefix(named)
	if named == model {
		return model
	}
	// A key alias renamed the model after the caller named the agent.
	if alias := schemas.GetResolvedAlias(ctx); alias != nil && trimGoogleModelPrefix(alias.Key) == named {
		return model
	}
	return ""
}

// IsGeminiInteractionsAgentRequest reports whether request runs an Interactions agent, one of the
// known families or one the request names as an agent.
func IsGeminiInteractionsAgentRequest(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) bool {
	return geminiInteractionsAgent(ctx, request) != ""
}

// ShouldUseGeminiInteractions reports whether a Responses request must run on the Interactions
// API instead of generateContent: the caller came in on the Interactions route, or the request
// needs what only Interactions offers (a stored previous turn, a background run, an agent, a tool
// search Google runs). Every other request keeps generateContent, so the common case pays a
// context read, three checks and a pass over its tools.
func ShouldUseGeminiInteractions(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) bool {
	if ctx != nil {
		if v, ok := ctx.Value(schemas.BifrostContextKeyInteractionsAPI).(bool); ok && v {
			return true
		}
	}
	if request == nil {
		return false
	}
	if p := request.Params; p != nil {
		if p.PreviousResponseID != nil && *p.PreviousResponseID != "" {
			return true
		}
		if p.Background != nil && *p.Background {
			return true
		}
		if hasGeminiServerToolSearch(p.Tools) {
			return true
		}
	}
	return IsGeminiInteractionsAgentRequest(ctx, request)
}

// =============================================================================
// WIRE TYPES
// =============================================================================

// GeminiInteractionsRequest is the body of POST /interactions. Agent-only and rarely used fields
// are kept as raw JSON so a native caller's body survives a typed round trip.
type GeminiInteractionsRequest struct {
	Model                 string                              `json:"model,omitempty"`
	Agent                 string                              `json:"agent,omitempty"`
	Input                 *GeminiInteractionsInput            `json:"input,omitempty"`
	SystemInstruction     *string                             `json:"system_instruction,omitempty"`
	Tools                 []GeminiInteractionsTool            `json:"tools,omitempty"`
	GenerationConfig      *GeminiInteractionsGenerationConfig `json:"generation_config,omitempty"`
	ResponseFormat        json.RawMessage                     `json:"response_format,omitempty"`
	PreviousInteractionID *string                             `json:"previous_interaction_id,omitempty"`
	Stream                *bool                               `json:"stream,omitempty"`
	Store                 *bool                               `json:"store,omitempty"`
	Background            *bool                               `json:"background,omitempty"`
	ServiceTier           *string                             `json:"service_tier,omitempty"`
	SafetySettings        json.RawMessage                     `json:"safety_settings,omitempty"`
	Labels                map[string]string                   `json:"labels,omitempty"`
	AgentConfig           json.RawMessage                     `json:"agent_config,omitempty"`
	Environment           json.RawMessage                     `json:"environment,omitempty"`
	EnvironmentID         *string                             `json:"environment_id,omitempty"`
	WebhookConfig         json.RawMessage                     `json:"webhook_config,omitempty"`
	MaxTotalTokens        json.RawMessage                     `json:"max_total_tokens,omitempty"`
	ContinuationToken     *string                             `json:"continuation_token,omitempty"`

	Fallbacks   []string               `json:"fallbacks,omitempty"` // Bifrost extension, never sent upstream
	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams implements providerUtils.RequestBodyWithExtraParams.
func (r *GeminiInteractionsRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// IsStreamingRequested implements the router's StreamingRequest interface.
func (r *GeminiInteractionsRequest) IsStreamingRequested() bool {
	return r != nil && r.Stream != nil && *r.Stream
}

// geminiInteractionsNativeOnlyFields are request fields with no Responses equivalent. They reach
// Gemini and Vertex through the raw body; a translated attempt on any other provider refuses them.
func (r *GeminiInteractionsRequest) nativeOnlyFields() []string {
	var fields []string
	if r.Agent != "" {
		fields = append(fields, "agent")
	}
	for _, field := range [...]struct {
		name string
		raw  json.RawMessage
	}{
		{"agent_config", r.AgentConfig},
		{"environment", r.Environment},
		{"webhook_config", r.WebhookConfig},
		{"max_total_tokens", r.MaxTotalTokens},
		{"safety_settings", r.SafetySettings},
	} {
		if len(field.raw) > 0 && !bytes.Equal(field.raw, []byte("null")) {
			fields = append(fields, field.name)
		}
	}
	if r.EnvironmentID != nil {
		fields = append(fields, "environment_id")
	}
	if r.ContinuationToken != nil && *r.ContinuationToken != "" {
		fields = append(fields, "continuation_token")
	}
	// An audio or image output format has no Responses form, so another provider would answer in
	// text only and drop it.
	if len(r.ResponseFormat) > 0 && !bytes.Equal(r.ResponseFormat, []byte("null")) && !geminiInteractionsResponseFormatTextOnly(r.ResponseFormat) {
		fields = append(fields, "response_format")
	}
	return fields
}

// GeminiInteractionsInput is the request input: a string, one content block or step, or a list
// of them.
type GeminiInteractionsInput struct {
	Text  *string
	Items []GeminiInteractionsItem
}

// MarshalJSON writes the string or the list.
func (in GeminiInteractionsInput) MarshalJSON() ([]byte, error) {
	if in.Text != nil {
		return providerUtils.MarshalSorted(*in.Text)
	}
	if in.Items == nil {
		return []byte("[]"), nil
	}
	return providerUtils.MarshalSorted(in.Items)
}

// UnmarshalJSON reads a string, a single object, or an array.
func (in *GeminiInteractionsInput) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	// Decode into a fresh value so a reused receiver never keeps the other form.
	switch trimmed[0] {
	case '"':
		var s string
		if err := sonic.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*in = GeminiInteractionsInput{Text: &s}
		return nil
	case '{':
		var item GeminiInteractionsItem
		if err := sonic.Unmarshal(trimmed, &item); err != nil {
			return err
		}
		*in = GeminiInteractionsInput{Items: []GeminiInteractionsItem{item}}
		return nil
	case '[':
		var items []GeminiInteractionsItem
		if err := sonic.Unmarshal(trimmed, &items); err != nil {
			return err
		}
		*in = GeminiInteractionsInput{Items: items}
		return nil
	}
	return fmt.Errorf("interactions input must be a string, an object, or an array")
}

// GeminiInteractionsItem is one content block (text, image, audio, video, document) or one step
// (user_input, model_output, thought, function_call, function_result, and the built-in tool
// call/result steps). The two share a "type" discriminator, so one struct holds either.
type GeminiInteractionsItem struct {
	Type string `json:"type"`

	// Content block fields.
	Text        *string         `json:"text,omitempty"`
	Data        *string         `json:"data,omitempty"`
	URI         *string         `json:"uri,omitempty"`
	MimeType    *string         `json:"mime_type,omitempty"`
	Resolution  *string         `json:"resolution,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`

	// Step fields.
	Content   []GeminiInteractionsItem `json:"content,omitempty"` // user_input, model_output
	Summary   []GeminiInteractionsItem `json:"summary,omitempty"` // thought
	Signature *string                  `json:"signature,omitempty"`
	ID        *string                  `json:"id,omitempty"`
	Name      *string                  `json:"name,omitempty"`
	Arguments json.RawMessage          `json:"arguments,omitempty"`
	CallID    *string                  `json:"call_id,omitempty"`
	Result    json.RawMessage          `json:"result,omitempty"`
	IsError   *bool                    `json:"is_error,omitempty"`

	ServerName *string `json:"server_name,omitempty"` // mcp_server_tool_call, mcp_server_tool_result
	SearchType *string `json:"search_type,omitempty"` // google_search_call
}

// GeminiInteractionsTool is one tool. Function tools are flat; built-in tools carry their own
// settings, kept raw since Bifrost only renames them.
type GeminiInteractionsTool struct {
	Type        string                          `json:"type"`
	Name        *string                         `json:"name,omitempty"`
	Description *string                         `json:"description,omitempty"`
	Parameters  *schemas.ToolFunctionParameters `json:"parameters,omitempty"`

	// tool retrieval: defer_loading on function and mcp_server, execution on tool_search
	DeferLoading     *bool   `json:"defer_loading,omitempty"`
	ShortDescription *string `json:"short_description,omitempty"` // a deferred function's search text
	Execution        *string `json:"execution,omitempty"`         // "client" on a caller-run tool_search

	// file_search
	FileSearchStoreNames []string        `json:"file_search_store_names,omitempty"`
	TopK                 *int            `json:"top_k,omitempty"`
	MetadataFilter       json.RawMessage `json:"metadata_filter,omitempty"`

	// mcp_server
	URL          *string                          `json:"url,omitempty"`
	Headers      map[string]string                `json:"headers,omitempty"`
	AllowedTools []GeminiInteractionsAllowedTools `json:"allowed_tools,omitempty"`

	// google_search
	SearchTypes []string `json:"search_types,omitempty"`

	// computer_use
	Environment                 *string         `json:"environment,omitempty"`
	ExcludedPredefinedFunctions json.RawMessage `json:"excluded_predefined_functions,omitempty"`

	EnablePromptInjectionDetection *bool `json:"enable_prompt_injection_detection,omitempty"`
}

// GeminiInteractionsGenerationConfig is generation_config.
type GeminiInteractionsGenerationConfig struct {
	MaxOutputTokens   *int            `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	Seed              *int            `json:"seed,omitempty"`
	StopSequences     []string        `json:"stop_sequences,omitempty"`
	ThinkingLevel     *string         `json:"thinking_level,omitempty"`
	ThinkingSummaries *string         `json:"thinking_summaries,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
}

// GeminiInteractionsToolChoiceConfig is the object form of generation_config.tool_choice.
type GeminiInteractionsToolChoiceConfig struct {
	Mode         *string                         `json:"mode,omitempty"`
	AllowedTools *GeminiInteractionsAllowedTools `json:"allowed_tools,omitempty"`
}

// GeminiInteractionsAllowedTools narrows a tool choice to named tools.
type GeminiInteractionsAllowedTools struct {
	Mode  *string  `json:"mode,omitempty"`
	Tools []string `json:"tools,omitempty"`
}

// GeminiInteractionsResponseFormat is the single-object form of response_format.
type GeminiInteractionsResponseFormat struct {
	Type     string          `json:"type"`
	MimeType *string         `json:"mime_type,omitempty"`
	Schema   json.RawMessage `json:"schema,omitempty"`
}

// GeminiInteraction is the Interaction resource: a create's response and what a GET returns.
type GeminiInteraction struct {
	ID                    string                    `json:"id,omitempty"`
	Object                string                    `json:"object,omitempty"`
	Model                 string                    `json:"model,omitempty"`
	Agent                 string                    `json:"agent,omitempty"`
	Status                string                    `json:"status,omitempty"`
	Created               string                    `json:"created,omitempty"`
	Updated               string                    `json:"updated,omitempty"`
	Steps                 []GeminiInteractionsItem  `json:"steps,omitempty"`
	Usage                 *GeminiInteractionsUsage  `json:"usage,omitempty"`
	ServiceTier           *string                   `json:"service_tier,omitempty"`
	PreviousInteractionID *string                   `json:"previous_interaction_id,omitempty"`
	EnvironmentID         *string                   `json:"environment_id,omitempty"`
	ContinuationToken     *string                   `json:"continuation_token,omitempty"`
	Errors                []GeminiDecisionErrorBody `json:"errors,omitempty"`
}

// GeminiInteractionsUsage is the Interactions usage block.
type GeminiInteractionsUsage struct {
	TotalTokens            int                    `json:"total_tokens"`
	TotalInputTokens       int                    `json:"total_input_tokens"`
	TotalOutputTokens      int                    `json:"total_output_tokens"`
	TotalThoughtTokens     int                    `json:"total_thought_tokens,omitempty"`
	TotalCachedTokens      int                    `json:"total_cached_tokens,omitempty"`
	TotalToolUseTokens     int                    `json:"total_tool_use_tokens,omitempty"`
	InputTokensByModality  []GeminiModalityTokens `json:"input_tokens_by_modality,omitempty"`
	OutputTokensByModality []GeminiModalityTokens `json:"output_tokens_by_modality,omitempty"`

	GroundingToolCount []GeminiInteractionsGroundingToolCount `json:"grounding_tool_count,omitempty"`
}

// GeminiInteractionsGroundingToolCount is how many times one grounding tool ran (google_search,
// google_maps), the unit Google bills grounding by.
type GeminiInteractionsGroundingToolCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// GeminiInteractionsEvent is one Interactions SSE event. Which fields are set follows EventType.
type GeminiInteractionsEvent struct {
	EventType         string                   `json:"event_type"`
	EventID           *string                  `json:"event_id,omitempty"`
	Interaction       *GeminiInteraction       `json:"interaction,omitempty"`
	InteractionID     *string                  `json:"interaction_id,omitempty"`
	Status            *string                  `json:"status,omitempty"`
	ContinuationToken *string                  `json:"continuation_token,omitempty"`
	Index             *int                     `json:"index,omitempty"`
	Step              *GeminiInteractionsItem  `json:"step,omitempty"`
	Delta             *GeminiInteractionsDelta `json:"delta,omitempty"`
	Usage             *GeminiInteractionsUsage `json:"usage,omitempty"`
	Metadata          json.RawMessage          `json:"metadata,omitempty"` // step.delta
	Error             *GeminiDecisionErrorBody `json:"error,omitempty"`
}

// deltaUsage is the cumulative usage a step.delta carries at metadata.total_usage, nil when it
// carries none. Metadata is read only here, so any other shape leaves the event as it is.
func (e *GeminiInteractionsEvent) deltaUsage() *GeminiInteractionsUsage {
	if len(e.Metadata) == 0 {
		return nil
	}
	raw := providerUtils.GetJSONField(e.Metadata, "total_usage")
	if !raw.IsObject() {
		return nil
	}
	var usage GeminiInteractionsUsage
	if sonic.Unmarshal([]byte(raw.Raw), &usage) != nil || !usage.counted() {
		return nil
	}
	return &usage
}

// GeminiInteractionsDelta is the delta of a step.delta event.
type GeminiInteractionsDelta struct {
	Type      string                  `json:"type"`
	Text      *string                 `json:"text,omitempty"`
	Content   *GeminiInteractionsItem `json:"content,omitempty"` // thought_summary
	Signature *string                 `json:"signature,omitempty"`
	Arguments json.RawMessage         `json:"arguments,omitempty"` // a string fragment on arguments_delta, the whole object on a built-in call
	Data      *string                 `json:"data,omitempty"`
	URI       *string                 `json:"uri,omitempty"`
	MimeType  *string                 `json:"mime_type,omitempty"`

	// A built-in tool step streams its whole call or result in one delta of its own type.
	Result      json.RawMessage `json:"result,omitempty"`
	IsError     *bool           `json:"is_error,omitempty"`
	Name        *string         `json:"name,omitempty"`
	ServerName  *string         `json:"server_name,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"` // text_annotation_delta
}

// =============================================================================
// BIFROST RESPONSES -> INTERACTIONS REQUEST
// =============================================================================

// ToGeminiInteractionsRequest converts a Responses request into an Interactions body. The model
// is sent as "agent" when it names an agent. Instructions and system or developer messages become
// system_instruction. Input that is all user content is sent as content blocks; input carrying
// assistant turns, reasoning, or tool calls is sent as steps, which is how stateless history is
// replayed. The request is not modified.
func ToGeminiInteractionsRequest(request *schemas.BifrostResponsesRequest) (*GeminiInteractionsRequest, error) {
	return toGeminiInteractionsRequest(request, geminiInteractionsAgent(nil, request))
}

// toGeminiInteractionsRequest is ToGeminiInteractionsRequest with the agent already resolved ("" for a
// model).
func toGeminiInteractionsRequest(request *schemas.BifrostResponsesRequest, agent string) (*GeminiInteractionsRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("responses request is nil")
	}
	out := &GeminiInteractionsRequest{}
	if agent != "" {
		out.Agent = agent
	} else {
		out.Model = NormalizeModelName(request.Model)
	}

	var systemParts []string
	params := request.Params
	if params != nil && params.Instructions != nil && strings.TrimSpace(*params.Instructions) != "" {
		systemParts = append(systemParts, *params.Instructions)
	}

	conversational := make([]schemas.ResponsesMessage, 0, len(request.Input))
	for _, msg := range request.Input {
		if msg.Role != nil && (*msg.Role == schemas.ResponsesInputMessageRoleSystem || *msg.Role == schemas.ResponsesInputMessageRoleDeveloper) &&
			(msg.Type == nil || *msg.Type == schemas.ResponsesMessageTypeMessage) {
			if text := responsesMessageText(msg.Content); text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}
		conversational = append(conversational, msg)
	}
	if len(systemParts) > 0 {
		joined := strings.Join(systemParts, "\n\n")
		out.SystemInstruction = &joined
	}

	clientSearchName := ""
	if params != nil {
		clientSearchName = geminiClientToolSearchName(params.Tools)
	}
	input, err := toGeminiInteractionsInput(conversational, clientSearchName)
	if err != nil {
		return nil, err
	}
	out.Input = input

	if params == nil {
		return out, nil
	}

	if params.PreviousResponseID != nil && isGenerateContentResponseID(*params.PreviousResponseID) {
		return nil, providerUtils.InvalidRequestErrorf("previous_response_id %q names a response Google did not store (it ran on generateContent or with store false), so there is no interaction to continue from; start the conversation with background, an agent or the /genai interactions route to get an interaction id", *params.PreviousResponseID)
	}
	out.PreviousInteractionID = params.PreviousResponseID
	out.Store = params.Store
	out.Background = params.Background
	if params.ServiceTier != nil {
		switch *params.ServiceTier {
		case schemas.BifrostServiceTierFlex, schemas.BifrostServiceTierPriority:
			tier := string(*params.ServiceTier)
			out.ServiceTier = &tier
		case schemas.BifrostServiceTierDefault:
			tier := "standard"
			out.ServiceTier = &tier
		}
	}

	// Interactions has no top_p. Its default, 1, changes nothing; any other value is refused rather
	// than ignored.
	if params.TopP != nil && *params.TopP != 1 {
		return nil, providerUtils.InvalidRequestErrorf("the interactions API has no top_p; remove it or use temperature")
	}

	tools, err := toGeminiInteractionsTools(params.Tools)
	if err != nil {
		return nil, err
	}
	// Responses caps built-in calls with max_tool_calls, and Interactions has no such cap.
	if params.MaxToolCalls != nil {
		for _, tool := range tools {
			if tool.Type != "function" && tool.Execution == nil { // a client tool_search is the caller's own call
				return nil, providerUtils.InvalidRequestErrorf("the interactions API cannot apply max_tool_calls to a %q tool; remove it or route the request to a provider that serves it", tool.Type)
			}
		}
	}
	callerSearches := schemas.ClientToolSearchFunctionName(params.Tools, request.Input) != ""
	out.Tools = applyGeminiInteractionsDeferredTools(tools, request.Model, callerSearches)

	config := &GeminiInteractionsGenerationConfig{
		MaxOutputTokens: params.MaxOutputTokens,
		Temperature:     params.Temperature,
	}
	if r := params.Reasoning; r != nil {
		if r.Effort != nil {
			config.ThinkingLevel = geminiInteractionsThinkingLevel(*r.Effort, request.Model)
		}
		summary := r.Summary
		if summary == nil {
			summary = r.GenerateSummary
		}
		if summary != nil {
			value := "auto"
			if *summary == "none" {
				value = "none"
			}
			config.ThinkingSummaries = &value
		}
	}
	if params.ToolChoice != nil {
		choice, err := toGeminiInteractionsToolChoice(params.ToolChoice)
		if err != nil {
			return nil, err
		}
		config.ToolChoice = choice
	}
	extra := make(map[string]interface{}, len(params.ExtraParams))
	for k, v := range params.ExtraParams {
		switch k {
		case "seed":
			if n, ok := schemas.SafeExtractInt(v); ok {
				config.Seed = &n
				continue
			}
		case "stop", "stop_sequences":
			if seqs, ok := schemas.SafeExtractStringSlice(v); ok {
				config.StopSequences = seqs
				continue
			}
		case "continuation_token":
			// A typed field, so the token reaches Google whether or not extra params pass through.
			if token, ok := schemas.SafeExtractString(v); ok && token != "" {
				out.ContinuationToken = &token
				continue
			}
		case "stream":
			// The operation decides streaming; a merged-in value would contradict it.
			continue
		case "model":
			// Routing chose the model; a merged-in value would replace it unchecked.
			continue
		case "agent":
			// Says whether the model is an agent (geminiInteractionsAgent); never sent as it is, so
			// it cannot name an agent governance did not check.
			continue
		case "environment":
			// Top-level and typed too: an Antigravity continuation needs it.
			if v != nil {
				if raw, err := providerUtils.MarshalSorted(v); err == nil {
					out.Environment = raw
					continue
				}
			}
		}
		extra[k] = v
	}
	if config.MaxOutputTokens != nil || config.Temperature != nil || config.Seed != nil || len(config.StopSequences) > 0 ||
		config.ThinkingLevel != nil || config.ThinkingSummaries != nil || len(config.ToolChoice) > 0 {
		if out.Agent != "" {
			if err := applyGeminiInteractionsAgentConfig(out, config); err != nil {
				return nil, err
			}
		} else {
			out.GenerationConfig = config
		}
	}
	if len(extra) > 0 {
		out.ExtraParams = extra
	}

	if params.Text != nil && params.Text.Format != nil {
		format, err := toGeminiInteractionsResponseFormat(params.Text.Format)
		if err != nil {
			return nil, err
		}
		out.ResponseFormat = format
	}
	return out, nil
}

// geminiInteractionsThinkingLevel maps a Responses reasoning effort to thinking_level, clamped to a
// level the model accepts (Google refuses a level the model does not implement). Agents are left
// as asked; the per-model table covers Gemini models only.
func geminiInteractionsThinkingLevel(effort, model string) *string {
	var level string
	switch effort {
	case "none", "minimal":
		level = "minimal"
	case "low", "medium", "high", "xhigh":
		level = effort
	case "max":
		level = "xhigh"
	default:
		return nil
	}
	if !IsGeminiInteractionsAgent(model) && isGemini3Plus(model) {
		level = clampThinkingLevel(level, model)
	}
	return &level
}

func toGeminiInteractionsToolChoice(choice *schemas.ResponsesToolChoice) (json.RawMessage, error) {
	if choice.ResponsesToolChoiceStr != nil {
		mode := ""
		switch *choice.ResponsesToolChoiceStr {
		case "auto":
			mode = "auto"
		case "none":
			mode = "none"
		case "required", "any":
			mode = "any"
		default:
			return nil, nil
		}
		return providerUtils.MarshalSorted(mode)
	}
	s := choice.ResponsesToolChoiceStruct
	if s == nil {
		return nil, nil
	}
	switch s.Type {
	case schemas.ResponsesToolChoiceTypeNone, schemas.ResponsesToolChoiceTypeAuto:
		return providerUtils.MarshalSorted(string(s.Type))
	case schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
		return providerUtils.MarshalSorted("any")
	case schemas.ResponsesToolChoiceTypeFunction, schemas.ResponsesToolChoiceTypeCustom:
		if s.Name == nil || *s.Name == "" {
			return providerUtils.MarshalSorted("any")
		}
		mode := "any"
		return providerUtils.MarshalSorted(GeminiInteractionsToolChoiceConfig{
			AllowedTools: &GeminiInteractionsAllowedTools{Mode: &mode, Tools: []string{*s.Name}},
		})
	case schemas.ResponsesToolChoiceTypeAllowedTools:
		mode := "auto"
		if s.Mode != nil && *s.Mode == "required" {
			mode = "any"
		}
		names := make([]string, 0, len(s.Tools))
		for _, t := range s.Tools {
			// Interactions names an allowed tool by its name only; an entry with none (a built-in
			// tool such as web_search) cannot be expressed, and dropping it would lift the restriction.
			if t.Name == nil || *t.Name == "" {
				return nil, providerUtils.InvalidRequestErrorf("the interactions API restricts tool_choice allowed_tools by tool name only; a %q entry has no name", t.Type)
			}
			names = append(names, *t.Name)
		}
		return providerUtils.MarshalSorted(GeminiInteractionsToolChoiceConfig{
			AllowedTools: &GeminiInteractionsAllowedTools{Mode: &mode, Tools: names},
		})
	case "":
		return nil, nil
	}
	// A hosted tool has no name to restrict by, and dropping the choice would let the model skip it.
	return nil, providerUtils.InvalidRequestErrorf("the interactions API cannot force a %q tool; remove tool_choice or route the request to a provider that serves it", s.Type)
}

func toGeminiInteractionsResponseFormat(format *schemas.ResponsesTextConfigFormat) (json.RawMessage, error) {
	switch format.Type {
	case "json_schema":
		mime := "application/json"
		out := GeminiInteractionsResponseFormat{Type: "text", MimeType: &mime}
		if format.JSONSchema != nil {
			schema, err := providerUtils.MarshalSorted(format.JSONSchema)
			if err != nil {
				return nil, fmt.Errorf("response format schema could not be encoded: %w", err)
			}
			out.Schema = schema
		}
		return providerUtils.MarshalSorted(out)
	case "json_object":
		mime := "application/json"
		return providerUtils.MarshalSorted(GeminiInteractionsResponseFormat{Type: "text", MimeType: &mime})
	}
	return nil, nil
}

// geminiInteractionsMCPHeaders returns the headers sent to an MCP server. An authorization token
// travels as a Bearer header unless the caller set Authorization itself; the caller's map is
// copied, never modified.
func geminiInteractionsMCPHeaders(mcp *schemas.ResponsesToolMCP) map[string]string {
	hasToken := mcp.Authorization != nil && *mcp.Authorization != ""
	if mcp.Headers == nil && !hasToken {
		return nil
	}
	headers := make(map[string]string, 1)
	if mcp.Headers != nil {
		maps.Copy(headers, *mcp.Headers)
	}
	if hasToken {
		for name := range headers {
			if strings.EqualFold(name, "Authorization") {
				return headers
			}
		}
		headers["Authorization"] = "Bearer " + *mcp.Authorization
	}
	return headers
}

// toGeminiInteractionsTools maps Responses tools to Interactions tools. Tools with no Interactions
// counterpart are refused rather than dropped, so a caller is never answered without a tool it
// sent.
func toGeminiInteractionsTools(tools []schemas.ResponsesTool) ([]GeminiInteractionsTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]GeminiInteractionsTool, 0, len(tools))
	for _, tool := range tools {
		switch tool.Type {
		case schemas.ResponsesToolTypeFunction:
			native := GeminiInteractionsTool{Type: "function", Name: tool.Name, Description: tool.Description, DeferLoading: tool.DeferLoading}
			if tool.ResponsesToolFunction != nil {
				native.Parameters = tool.ResponsesToolFunction.Parameters
			}
			out = append(out, native)
		case schemas.ResponsesToolTypeToolSearch:
			out = append(out, toGeminiInteractionsToolSearchTool(tool))
		case schemas.ResponsesToolTypeWebSearch, schemas.ResponsesToolTypeWebSearchPreview:
			native := GeminiInteractionsTool{Type: "google_search"}
			if ws := tool.ResponsesToolWebSearch; ws != nil {
				// Google Search always reaches the live web and takes no filters, so a restriction
				// is refused rather than dropped.
				if ws.ExternalWebAccess != nil && !*ws.ExternalWebAccess {
					return nil, providerUtils.InvalidRequestErrorf("the interactions API always searches the live web; remove external_web_access false or route the request to a provider that serves it")
				}
				if f := ws.Filters; f != nil && (len(f.AllowedDomains) > 0 || len(f.BlockedDomains) > 0 || f.TimeRangeFilter != nil) {
					return nil, providerUtils.InvalidRequestErrorf("the interactions API cannot apply web_search filters; remove them or route the request to a provider that serves them")
				}
				for _, contentType := range ws.SearchContentTypes {
					switch contentType {
					case geminiSearchContentTypeText:
						native.SearchTypes = append(native.SearchTypes, "web_search")
					case geminiSearchContentTypeImage:
						native.SearchTypes = append(native.SearchTypes, "image_search")
					default:
						return nil, providerUtils.InvalidRequestErrorf("the interactions API has no %q search content type", contentType)
					}
				}
			}
			out = append(out, native)
		case schemas.ResponsesToolTypeWebFetch:
			// url_context takes no settings, so a domain restriction is refused rather than dropped.
			if wf := tool.ResponsesToolWebFetch; wf != nil && wf.Filters != nil && (len(wf.Filters.AllowedDomains) > 0 || len(wf.Filters.BlockedDomains) > 0) {
				return nil, providerUtils.InvalidRequestErrorf("the interactions API cannot apply web_fetch domain filters; remove them or route the request to a provider that serves them")
			}
			out = append(out, GeminiInteractionsTool{Type: "url_context"})
		case schemas.ResponsesToolTypeCodeInterpreter:
			if ci := tool.ResponsesToolCodeInterpreter; ci != nil && namesCodeInterpreterContainer(ci.Container) {
				return nil, providerUtils.InvalidRequestErrorf("the interactions API runs code in a sandbox of its own, with no container id or files; remove them from code_interpreter or route the request to a provider that serves them")
			}
			out = append(out, GeminiInteractionsTool{Type: "code_execution"})
		case schemas.ResponsesToolTypeFileSearch:
			native := GeminiInteractionsTool{Type: "file_search"}
			if fs := tool.ResponsesToolFileSearch; fs != nil {
				// Dropping a filter would search documents the caller excluded, so it is refused.
				if fs.Filters != nil {
					return nil, providerUtils.InvalidRequestErrorf("the interactions API cannot apply file_search filters; remove them or route the request to a provider that serves them")
				}
				if fs.RankingOptions != nil {
					return nil, providerUtils.InvalidRequestErrorf("the interactions API has no file_search ranking_options; remove them or route the request to a provider that serves them")
				}
				native.FileSearchStoreNames = fs.VectorStoreIDs
				native.TopK = fs.MaxNumResults
			}
			out = append(out, native)
		case schemas.ResponsesToolTypeMCP:
			mcp := tool.ResponsesToolMCP
			if mcp == nil || mcp.ServerURL == nil {
				return nil, providerUtils.InvalidRequestErrorf("the interactions API reaches an MCP server by its url; send server_url on the mcp tool")
			}
			// Google runs an MCP server's tools itself with no approval step, so only a tool the
			// caller lets run without approval can be sent. Responses defaults to "always".
			if a := mcp.RequireApproval; a == nil || a.Always != nil || a.Setting == nil || *a.Setting != "never" {
				return nil, providerUtils.InvalidRequestErrorf("the interactions API runs MCP tools without an approval step; send require_approval \"never\" on the mcp tool, or route the request to a provider that asks for approval")
			}
			label := mcp.ServerLabel
			native := GeminiInteractionsTool{Type: "mcp_server", Name: &label, URL: mcp.ServerURL, DeferLoading: tool.DeferLoading}
			native.Headers = geminiInteractionsMCPHeaders(mcp)
			allowed, err := toGeminiInteractionsMCPAllowedTools(mcp.AllowedTools)
			if err != nil {
				return nil, err
			}
			native.AllowedTools = allowed
			out = append(out, native)
		case schemas.ResponsesToolTypeComputerUsePreview, schemas.ResponsesToolTypeComputer:
			environment := ""
			if tool.ResponsesToolComputerUsePreview != nil {
				environment = tool.ResponsesToolComputerUsePreview.Environment
			}
			out = append(out, GeminiInteractionsTool{Type: "computer_use", Environment: schemas.Ptr(geminiInteractionsComputerEnvironment(environment))})
		default:
			return nil, providerUtils.InvalidRequestErrorf("the interactions API has no %q tool; route the request to a provider that serves it", tool.Type)
		}
	}
	return out, nil
}

// toGeminiInteractionsInput builds the input. All-user text with no media is one string; all-user
// content is a list of content blocks; anything with model turns or tool traffic is a list of
// steps.
func toGeminiInteractionsInput(messages []schemas.ResponsesMessage, clientSearchName string) (*GeminiInteractionsInput, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	allUser := true
	for _, msg := range messages {
		if !isResponsesUserMessage(msg) {
			allUser = false
			break
		}
	}
	if allUser {
		var blocks []GeminiInteractionsItem
		for _, msg := range messages {
			converted, err := toGeminiInteractionsContentBlocks(msg.Content)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, converted...)
		}
		if len(blocks) == 1 && blocks[0].Type == GeminiContentTypeText && blocks[0].Text != nil && len(blocks[0].Annotations) == 0 {
			return &GeminiInteractionsInput{Text: blocks[0].Text}, nil
		}
		return &GeminiInteractionsInput{Items: blocks}, nil
	}

	steps := make([]GeminiInteractionsItem, 0, len(messages))
	var callNames map[string]*string // call id -> function name, for outputs that name none
	for _, msg := range messages {
		step, ok, err := toGeminiInteractionsStep(msg, clientSearchName)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		switch step.Type {
		case GeminiInteractionsStepFunctionCall:
			if step.ID != nil && step.Name != nil {
				if callNames == nil {
					callNames = make(map[string]*string)
				}
				callNames[*step.ID] = step.Name
			}
		case GeminiInteractionsStepFunctionResult:
			// Responses outputs carry no name, and Google refuses a result without the one called.
			if (step.Name == nil || *step.Name == "") && step.CallID != nil {
				step.Name = callNames[*step.CallID]
			}
		}
		steps = append(steps, step)
	}
	return &GeminiInteractionsInput{Items: steps}, nil
}

func isResponsesUserMessage(msg schemas.ResponsesMessage) bool {
	if msg.Type != nil && *msg.Type != schemas.ResponsesMessageTypeMessage {
		return false
	}
	return msg.Role == nil || *msg.Role == schemas.ResponsesInputMessageRoleUser
}

// toGeminiInteractionsStep converts one Responses item into a step. ok is false for an item that
// carries nothing to send (an empty reasoning item). clientSearchName is the name of the caller's
// client tool_search, which its tool_search items replay under.
func toGeminiInteractionsStep(msg schemas.ResponsesMessage, clientSearchName string) (GeminiInteractionsItem, bool, error) {
	msgType := schemas.ResponsesMessageTypeMessage
	if msg.Type != nil {
		msgType = *msg.Type
	}
	switch msgType {
	case schemas.ResponsesMessageTypeMessage:
		blocks, err := toGeminiInteractionsContentBlocks(msg.Content)
		if err != nil {
			return GeminiInteractionsItem{}, false, err
		}
		stepType := GeminiInteractionsStepUserInput
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleAssistant {
			stepType = GeminiInteractionsStepModelOutput
		}
		return GeminiInteractionsItem{Type: stepType, Content: blocks}, true, nil

	case schemas.ResponsesMessageTypeReasoning:
		step := GeminiInteractionsItem{Type: GeminiInteractionsStepThought}
		if msg.ResponsesReasoning != nil {
			step.Signature = msg.ResponsesReasoning.EncryptedContent
			for _, s := range msg.ResponsesReasoning.Summary {
				text := s.Text
				step.Summary = append(step.Summary, GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text})
			}
		}
		if step.Signature == nil && len(step.Summary) == 0 {
			return GeminiInteractionsItem{}, false, nil
		}
		return step, true, nil

	case schemas.ResponsesMessageTypeFunctionCall:
		if msg.ResponsesToolMessage == nil {
			return GeminiInteractionsItem{}, false, providerUtils.InvalidRequestErrorf("function_call input item has no call")
		}
		tm := msg.ResponsesToolMessage
		args := json.RawMessage("{}")
		if tm.Arguments != nil && strings.TrimSpace(*tm.Arguments) != "" {
			if !json.Valid([]byte(*tm.Arguments)) {
				return GeminiInteractionsItem{}, false, providerUtils.InvalidRequestErrorf("function_call arguments are not valid JSON")
			}
			args = json.RawMessage(*tm.Arguments)
		}
		return GeminiInteractionsItem{Type: GeminiInteractionsStepFunctionCall, ID: functionCallStepID(msg), Name: tm.Name, Arguments: args}, true, nil

	case schemas.ResponsesMessageTypeFunctionCallOutput:
		if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.CallID == nil {
			return GeminiInteractionsItem{}, false, providerUtils.InvalidRequestErrorf("function_call_output input item has no call_id")
		}
		tm := msg.ResponsesToolMessage
		output := tm.Output
		failed := tm.Error.IsError()
		if failed && output == nil {
			// A failure with no separate output sends its message as the result.
			text := tm.Error.Text()
			output = &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &text}
		}
		result, err := toGeminiInteractionsFunctionResult(output)
		if err != nil {
			return GeminiInteractionsItem{}, false, err
		}
		step := GeminiInteractionsItem{Type: GeminiInteractionsStepFunctionResult, CallID: tm.CallID, Name: tm.Name, Result: result}
		if failed {
			step.IsError = schemas.Ptr(true)
		}
		return step, true, nil

	case schemas.ResponsesMessageTypeToolSearchCall, schemas.ResponsesMessageTypeToolSearchOutput:
		return responsesToolSearchItemToGeminiInteractionStep(msg, clientSearchName)
	}
	// A built-in tool a previous turn ran on the provider's side is history, not input: Google
	// has no step that replays it without the signature it was issued with, so it is dropped.
	if isResponsesServerToolItem(msgType) {
		return GeminiInteractionsItem{}, false, nil
	}
	return GeminiInteractionsItem{}, false, providerUtils.InvalidRequestErrorf("the interactions API cannot take a %q input item; route the request to a provider that accepts it", msgType)
}

// functionCallStepID is the id a function_call item goes out under: its call_id, else the item's own id.
func functionCallStepID(msg schemas.ResponsesMessage) *string {
	if msg.CallID != nil {
		return msg.CallID
	}
	return msg.ID
}

// toGeminiInteractionsFunctionResult renders a function output as the content list the
// Interactions API takes: a text block for a string output, the converted blocks otherwise.
func toGeminiInteractionsFunctionResult(output *schemas.ResponsesToolMessageOutputStruct) (json.RawMessage, error) {
	var blocks []GeminiInteractionsItem
	if output != nil {
		switch {
		case output.ResponsesToolCallOutputStr != nil:
			text := *output.ResponsesToolCallOutputStr
			blocks = []GeminiInteractionsItem{{Type: GeminiContentTypeText, Text: &text}}
		case len(output.ResponsesFunctionToolCallOutputBlocks) > 0:
			converted, err := toGeminiInteractionsContentBlocks(&schemas.ResponsesMessageContent{ContentBlocks: output.ResponsesFunctionToolCallOutputBlocks})
			if err != nil {
				return nil, err
			}
			blocks = converted
		}
	}
	if len(blocks) == 0 {
		empty := ""
		blocks = []GeminiInteractionsItem{{Type: GeminiContentTypeText, Text: &empty}}
	}
	return providerUtils.MarshalSorted(blocks)
}

// toGeminiInteractionsMCPAllowedTools renders an MCP allowlist in the native shape, one entry
// naming the allowed tools. A filter that restricts by anything other than names cannot be
// expressed, so it is refused rather than sending the server unrestricted.
func toGeminiInteractionsMCPAllowedTools(allowed *schemas.ResponsesToolMCPAllowedTools) ([]GeminiInteractionsAllowedTools, error) {
	if allowed == nil {
		return nil, nil
	}
	names := allowed.ToolNames
	if allowed.Filter != nil {
		if allowed.Filter.ReadOnly != nil || len(allowed.Filter.ToolNames) == 0 {
			return nil, providerUtils.InvalidRequestErrorf("the interactions API restricts an MCP server's allowed_tools by tool name only; send allowed_tools as a list of names")
		}
		names = allowed.Filter.ToolNames
	}
	if len(names) == 0 {
		// An empty list denies every tool. Google reads a missing allowlist as "all tools" and
		// documents no way to allow none, so the request is refused rather than widened.
		return nil, providerUtils.InvalidRequestErrorf("an empty mcp allowed_tools list denies every tool, which the interactions API cannot express; remove the mcp tool instead")
	}
	return []GeminiInteractionsAllowedTools{{Tools: names}}, nil
}

// geminiInteractionsMCPAllowedToolNames flattens a native MCP allowlist into the tool names it
// allows. A Responses allowlist can only permit tools, so an entry in mode "none" (which disables
// them) is refused rather than turned into permission.
func geminiInteractionsMCPAllowedToolNames(allowed []GeminiInteractionsAllowedTools) ([]string, error) {
	var names []string
	for _, entry := range allowed {
		if entry.Mode != nil && *entry.Mode == "none" {
			return nil, providerUtils.InvalidRequestErrorf("an mcp_server allowed_tools entry in mode \"none\" cannot be translated for another provider; remove the tools from the server instead")
		}
		names = append(names, entry.Tools...)
	}
	return names, nil
}

// toGeminiInteractionsContentBlocks converts message content into content blocks. Text of any
// kind is a text block; an image or file is the media block its type names.
func toGeminiInteractionsContentBlocks(content *schemas.ResponsesMessageContent) ([]GeminiInteractionsItem, error) {
	if content == nil {
		return nil, nil
	}
	if content.ContentStr != nil {
		text := *content.ContentStr
		return []GeminiInteractionsItem{{Type: GeminiContentTypeText, Text: &text}}, nil
	}
	blocks := make([]GeminiInteractionsItem, 0, len(content.ContentBlocks))
	for _, block := range content.ContentBlocks {
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText, schemas.ResponsesOutputMessageContentTypeText,
			schemas.ResponsesOutputMessageContentTypeReasoning, schemas.ResponsesOutputMessageContentTypeSummaryText:
			if block.Text != nil {
				text := *block.Text
				blocks = append(blocks, GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text})
			}
		case schemas.ResponsesOutputMessageContentTypeRefusal:
			if block.ResponsesOutputMessageContentRefusal != nil {
				text := block.ResponsesOutputMessageContentRefusal.Refusal
				blocks = append(blocks, GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text})
			}
		case schemas.ResponsesInputMessageContentBlockTypeImage:
			item, err := toGeminiInteractionsImage(block)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, item)
		case schemas.ResponsesInputMessageContentBlockTypeFile:
			item, err := toGeminiInteractionsFile(block)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, item)
		case schemas.ResponsesInputMessageContentBlockTypeAudio:
			if block.Audio == nil || block.Audio.Data == "" {
				return nil, providerUtils.InvalidRequestErrorf("input_audio block has no data")
			}
			data := block.Audio.Data
			mime := "audio/" + strings.TrimPrefix(block.Audio.Format, "audio/")
			if block.Audio.Format == "" {
				mime = "audio/wav"
			}
			blocks = append(blocks, GeminiInteractionsItem{Type: GeminiContentTypeAudio, Data: &data, MimeType: &mime})
		default:
			return nil, providerUtils.InvalidRequestErrorf("the interactions API cannot take a %q content block; route the request to a provider that accepts it", block.Type)
		}
	}
	return blocks, nil
}

func toGeminiInteractionsImage(block schemas.ResponsesMessageContentBlock) (GeminiInteractionsItem, error) {
	img := block.ResponsesInputMessageContentBlockImage
	if img == nil || img.ImageURL == nil || *img.ImageURL == "" {
		if block.FileID != nil && *block.FileID != "" {
			uri := *block.FileID
			mime := mimeTypeFromURI(uri)
			item := GeminiInteractionsItem{Type: GeminiContentTypeImage, URI: &uri}
			if mime != "" {
				item.MimeType = &mime
			}
			return item, nil
		}
		return GeminiInteractionsItem{}, providerUtils.InvalidRequestErrorf("input_image block has no image_url")
	}
	item := GeminiInteractionsItem{Type: GeminiContentTypeImage, Resolution: geminiDecisionResolution(img.Detail, GeminiContentTypeImage)}
	url := *img.ImageURL
	if strings.HasPrefix(url, "data:") {
		mediaType, payload, ok := geminiDataURLParts(url)
		if !ok {
			return GeminiInteractionsItem{}, providerUtils.InvalidRequestErrorf("input_image data URL must be base64 and state its media type, such as data:image/png;base64,...")
		}
		item.Data, item.MimeType = &payload, &mediaType
		return item, nil
	}
	item.URI = &url
	if mediaType := mimeTypeFromURI(url); strings.HasPrefix(mediaType, "image/") {
		item.MimeType = &mediaType
	}
	return item, nil
}

func toGeminiInteractionsFile(block schemas.ResponsesMessageContentBlock) (GeminiInteractionsItem, error) {
	file := block.ResponsesInputMessageContentBlockFile
	var mediaType string
	if file != nil && file.FileType != nil {
		mediaType = strings.TrimSpace(*file.FileType)
	}
	item := GeminiInteractionsItem{}
	switch {
	case file != nil && file.FileData != nil && *file.FileData != "":
		data := *file.FileData
		if strings.HasPrefix(data, "data:") {
			mt, payload, ok := geminiDataURLParts(data)
			if !ok {
				return GeminiInteractionsItem{}, providerUtils.InvalidRequestErrorf("input_file data URL must be base64 and state its media type")
			}
			data = payload
			if mediaType == "" {
				mediaType = mt
			}
		}
		item.Data = &data
	case file != nil && file.FileURL != nil && *file.FileURL != "":
		uri := *file.FileURL
		item.URI = &uri
		if mediaType == "" {
			mediaType = mimeTypeFromURI(uri)
		}
	case block.FileID != nil && *block.FileID != "":
		uri := *block.FileID
		item.URI = &uri
		if mediaType == "" {
			mediaType = mimeTypeFromURI(uri)
		}
	default:
		return GeminiInteractionsItem{}, providerUtils.InvalidRequestErrorf("input_file block has no file_data, file_url, or file_id")
	}
	if mediaType == "" {
		mediaType = "application/pdf"
	}
	lower := strings.ToLower(mediaType)
	switch {
	case strings.HasPrefix(lower, "image/"):
		item.Type = GeminiContentTypeImage
	case strings.HasPrefix(lower, "audio/"):
		item.Type = GeminiContentTypeAudio
	case strings.HasPrefix(lower, "video/"):
		item.Type = GeminiContentTypeVideo
	default:
		item.Type = GeminiContentTypeDocument
	}
	item.MimeType = &mediaType
	return item, nil
}

// responsesMessageText joins the text of a message's content.
func responsesMessageText(content *schemas.ResponsesMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	var sb strings.Builder
	for _, block := range content.ContentBlocks {
		if block.Text == nil {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(*block.Text)
	}
	return sb.String()
}

// =============================================================================
// INTERACTIONS REQUEST -> BIFROST RESPONSES (inbound, translated path)
// =============================================================================

// ToBifrostResponsesRequest converts an Interactions body into a Responses request. A
// provider/model prefix on model (or agent) picks the provider; with none, the provider is left
// empty for the route to fill. Agent-only fields have no Responses form: they are kept in
// ExtraParams so the Gemini and Vertex egress can send them back, and refused by
// ValidateTranslatable before any other provider is tried.
func (r *GeminiInteractionsRequest) ToBifrostResponsesRequest() (*schemas.BifrostResponsesRequest, error) {
	if r == nil {
		return nil, fmt.Errorf("interactions request is nil")
	}
	modelName := r.Model
	if modelName == "" {
		modelName = r.Agent
	}
	if modelName == "" {
		return nil, providerUtils.InvalidRequestErrorf("interactions request names neither a model nor an agent")
	}
	provider, model := schemas.ParseModelString(modelName, "")
	out := &schemas.BifrostResponsesRequest{
		Provider:  provider,
		Model:     model,
		Fallbacks: schemas.ParseFallbacks(r.Fallbacks),
	}
	params := &schemas.ResponsesParameters{ExtraParams: map[string]interface{}{}}

	if r.SystemInstruction != nil && *r.SystemInstruction != "" {
		instructions := *r.SystemInstruction
		params.Instructions = &instructions
	}
	if r.Input != nil {
		input, err := geminiInteractionsInputToResponses(r.Input, GeminiInteractionsRequestClientToolSearchName(r.Tools))
		if err != nil {
			return nil, err
		}
		out.Input = input
	}

	params.PreviousResponseID = r.PreviousInteractionID
	params.Store = r.Store
	params.Background = r.Background
	if r.ServiceTier != nil {
		var tier schemas.BifrostServiceTier
		switch *r.ServiceTier {
		case "flex":
			tier = schemas.BifrostServiceTierFlex
		case "priority":
			tier = schemas.BifrostServiceTierPriority
		default:
			tier = schemas.BifrostServiceTierDefault
		}
		params.ServiceTier = &tier
	}

	tools, err := geminiInteractionsToolsToResponses(r.Tools)
	if err != nil {
		return nil, err
	}
	params.Tools = tools

	if gc := r.GenerationConfig; gc != nil {
		params.MaxOutputTokens = gc.MaxOutputTokens
		params.Temperature = gc.Temperature
		if gc.Seed != nil {
			params.ExtraParams["seed"] = *gc.Seed
		}
		if len(gc.StopSequences) > 0 {
			params.ExtraParams["stop"] = gc.StopSequences
		}
		if gc.ThinkingLevel != nil || gc.ThinkingSummaries != nil {
			reasoning := &schemas.ResponsesParametersReasoning{}
			if gc.ThinkingLevel != nil {
				effort := *gc.ThinkingLevel
				reasoning.Effort = &effort
			}
			if gc.ThinkingSummaries != nil {
				summary := "auto"
				if *gc.ThinkingSummaries == "none" {
					summary = "none"
				}
				reasoning.Summary = &summary
			}
			params.Reasoning = reasoning
		}
		if len(gc.ToolChoice) > 0 {
			params.ToolChoice = geminiInteractionsToolChoiceToResponses(gc.ToolChoice)
		}
	}

	if len(r.ResponseFormat) > 0 {
		params.Text = geminiInteractionsResponseFormatToResponses(r.ResponseFormat)
	}

	for name, raw := range map[string]json.RawMessage{
		"safety_settings":  r.SafetySettings,
		"agent_config":     r.AgentConfig,
		"environment":      r.Environment,
		"webhook_config":   r.WebhookConfig,
		"max_total_tokens": r.MaxTotalTokens,
	} {
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			params.ExtraParams[name] = raw
		}
	}
	if r.EnvironmentID != nil {
		params.ExtraParams["environment_id"] = *r.EnvironmentID
	}
	if r.Model == "" && !IsGeminiInteractionsAgent(model) {
		// An agent hosted under the caller's own name is told from a model by this alone.
		params.ExtraParams["agent"] = model
	}
	if r.ContinuationToken != nil {
		params.ExtraParams["continuation_token"] = *r.ContinuationToken
	}
	if len(r.Labels) > 0 {
		labels := make(map[string]any, len(r.Labels))
		for k, v := range r.Labels {
			labels[k] = v
		}
		params.Metadata = &labels
	}
	for k, v := range r.ExtraParams {
		params.ExtraParams[k] = v
	}
	if len(params.ExtraParams) == 0 {
		params.ExtraParams = nil
	}
	out.Params = params
	return out, nil
}

// ValidateTranslatable refuses a request that carries agent-only fields when it is about to be
// translated for a provider other than Gemini or Vertex: those fields have no Responses form and
// would otherwise be dropped without the caller knowing.
func (r *GeminiInteractionsRequest) ValidateTranslatable() error {
	if fields := r.nativeOnlyFields(); len(fields) > 0 {
		return providerUtils.InvalidRequestErrorf("interactions fields %s are served by gemini and vertex only; route the request to one of them", strings.Join(fields, ", "))
	}
	return nil
}

// geminiInteractionsInputToResponses converts the request input. clientSearchName is the name of the
// caller's client tool_search, "" when it declares none: the model's call to it and the caller's
// answer become client tool_search items, so the declarations the answer lists are not read as
// content blocks.
func geminiInteractionsInputToResponses(in *GeminiInteractionsInput, clientSearchName string) ([]schemas.ResponsesMessage, error) {
	if in.Text != nil {
		text := *in.Text
		return []schemas.ResponsesMessage{userResponsesMessage(&schemas.ResponsesMessageContent{ContentStr: &text})}, nil
	}
	var out []schemas.ResponsesMessage
	var pendingUser []schemas.ResponsesMessageContentBlock
	var clientCalls map[string]struct{} // ids of calls to the client search, for results that omit the name
	flushUser := func() {
		if len(pendingUser) == 0 {
			return
		}
		out = append(out, userResponsesMessage(&schemas.ResponsesMessageContent{ContentBlocks: pendingUser}))
		pendingUser = nil
	}
	for _, item := range in.Items {
		if isGeminiInteractionsContentType(item.Type) {
			block, err := geminiInteractionsBlockToResponses(item, true)
			if err != nil {
				return nil, err
			}
			pendingUser = append(pendingUser, block)
			continue
		}
		flushUser()
		if clientSearchName != "" {
			if msg, ok := geminiClientToolSearchStepToResponses(item, clientSearchName, &clientCalls); ok {
				out = append(out, msg)
				continue
			}
		}
		msgs, err := geminiInteractionsStepToResponses(item, true)
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
	}
	flushUser()
	return out, nil
}

func userResponsesMessage(content *schemas.ResponsesMessageContent) schemas.ResponsesMessage {
	role := schemas.ResponsesInputMessageRoleUser
	msgType := schemas.ResponsesMessageTypeMessage
	return schemas.ResponsesMessage{Type: &msgType, Role: &role, Content: content}
}

func isGeminiInteractionsContentType(t string) bool {
	switch t {
	case GeminiContentTypeText, GeminiContentTypeImage, GeminiContentTypeAudio, GeminiContentTypeVideo, GeminiContentTypeDocument:
		return true
	}
	return false
}

// geminiInteractionsBlockToResponses converts one content block. input selects input_* block
// types (a request) over output_text (a model's output).
func geminiInteractionsBlockToResponses(item GeminiInteractionsItem, input bool) (schemas.ResponsesMessageContentBlock, error) {
	switch item.Type {
	case GeminiContentTypeText:
		text := ""
		if item.Text != nil {
			text = *item.Text
		}
		if input {
			return schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: &text}, nil
		}
		return schemas.ResponsesMessageContentBlock{
			Type:                              schemas.ResponsesOutputMessageContentTypeText,
			Text:                              &text,
			ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{Annotations: geminiInteractionsAnnotationsToResponses(item.Annotations)},
		}, nil
	case GeminiContentTypeImage:
		url := geminiInteractionsMediaURL(item)
		if url == "" {
			return schemas.ResponsesMessageContentBlock{}, providerUtils.InvalidRequestErrorf("image block has neither data nor uri")
		}
		block := schemas.ResponsesMessageContentBlock{
			Type:                                   schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: &url},
		}
		if item.Resolution != nil {
			detail := *item.Resolution
			if detail == "ultra_high" {
				detail = "original"
			}
			block.ResponsesInputMessageContentBlockImage.Detail = &detail
		}
		return block, nil
	case GeminiContentTypeAudio, GeminiContentTypeVideo, GeminiContentTypeDocument:
		// Spoken output is an audio block, as generateContent renders it.
		if item.Type == GeminiContentTypeAudio && !input && item.Data != nil {
			format := "wav"
			if item.MimeType != nil && *item.MimeType != "" {
				format = strings.TrimPrefix(*item.MimeType, "audio/")
			}
			return schemas.ResponsesMessageContentBlock{
				Type:  schemas.ResponsesInputMessageContentBlockTypeAudio,
				Audio: &schemas.ResponsesInputMessageContentBlockAudio{Format: format, Data: *item.Data},
			}, nil
		}
		file := &schemas.ResponsesInputMessageContentBlockFile{FileType: item.MimeType}
		switch {
		case item.Data != nil:
			data := *item.Data
			if item.MimeType != nil {
				data = "data:" + *item.MimeType + ";base64," + data
			}
			file.FileData = &data
		case item.URI != nil:
			uri := *item.URI
			file.FileURL = &uri
		default:
			return schemas.ResponsesMessageContentBlock{}, providerUtils.InvalidRequestErrorf("%s block has neither data nor uri", item.Type)
		}
		return schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesInputMessageContentBlockTypeFile, ResponsesInputMessageContentBlockFile: file}, nil
	}
	return schemas.ResponsesMessageContentBlock{}, providerUtils.InvalidRequestErrorf("unsupported interactions content block type %q", item.Type)
}

// geminiInteractionsFunctionResultError is the error of a function_result that reports is_error.
func geminiInteractionsFunctionResultError(step GeminiInteractionsItem) *schemas.ResponsesToolMessageError {
	text := geminiInteractionsResultText(step.Result)
	if text == "" {
		text = "function call failed"
	}
	return &schemas.ResponsesToolMessageError{ResponsesToolMessageErrorStr: &text}
}

func geminiInteractionsMediaURL(item GeminiInteractionsItem) string {
	if item.Data != nil {
		mime := "application/octet-stream"
		if item.MimeType != nil {
			mime = *item.MimeType
		}
		return "data:" + mime + ";base64," + *item.Data
	}
	if item.URI != nil {
		return *item.URI
	}
	return ""
}

// geminiInteractionsStepToResponses converts one step into Responses items. input is true for a
// request's history, false for a run's output. Built-in tool steps have no Responses item that
// holds them losslessly and are skipped on input; a request that needs them goes to Gemini raw.
func geminiInteractionsStepToResponses(step GeminiInteractionsItem, input bool) ([]schemas.ResponsesMessage, error) {
	switch step.Type {
	case GeminiInteractionsStepUserInput:
		blocks, err := geminiInteractionsBlocksToResponses(step.Content, true)
		if err != nil {
			return nil, err
		}
		return []schemas.ResponsesMessage{userResponsesMessage(&schemas.ResponsesMessageContent{ContentBlocks: blocks})}, nil

	case GeminiInteractionsStepModelOutput:
		blocks, err := geminiInteractionsBlocksToResponses(step.Content, false)
		if err != nil {
			return nil, err
		}
		role := schemas.ResponsesInputMessageRoleAssistant
		msgType := schemas.ResponsesMessageTypeMessage
		msg := schemas.ResponsesMessage{Type: &msgType, Role: &role, Content: &schemas.ResponsesMessageContent{ContentBlocks: blocks}}
		if !input {
			status := "completed"
			msg.Status = &status
		}
		return []schemas.ResponsesMessage{msg}, nil

	case GeminiInteractionsStepThought:
		msgType := schemas.ResponsesMessageTypeReasoning
		reasoning := &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{}, EncryptedContent: step.Signature}
		for _, s := range step.Summary {
			if s.Text != nil {
				reasoning.Summary = append(reasoning.Summary, schemas.ResponsesReasoningSummary{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: *s.Text})
			}
		}
		return []schemas.ResponsesMessage{{Type: &msgType, ResponsesReasoning: reasoning}}, nil

	case GeminiInteractionsStepFunctionCall:
		msgType := schemas.ResponsesMessageTypeFunctionCall
		args := "{}"
		if len(step.Arguments) > 0 && !bytes.Equal(step.Arguments, []byte("null")) {
			args = string(step.Arguments)
		}
		msg := schemas.ResponsesMessage{
			Type: &msgType,
			ID:   step.ID,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    step.ID,
				Name:      step.Name,
				Arguments: &args,
			},
		}
		if !input {
			status := "completed"
			msg.Status = &status
		}
		return []schemas.ResponsesMessage{msg}, nil

	case GeminiInteractionsStepFunctionResult:
		msgType := schemas.ResponsesMessageTypeFunctionCallOutput
		output, err := geminiInteractionsResultToResponsesOutput(step.Result)
		if err != nil {
			return nil, err
		}
		tool := &schemas.ResponsesToolMessage{CallID: step.CallID, Name: step.Name, Output: output}
		if step.IsError != nil && *step.IsError {
			tool.Error = geminiInteractionsFunctionResultError(step)
		}
		return []schemas.ResponsesMessage{{Type: &msgType, ResponsesToolMessage: tool}}, nil

	case GeminiInteractionsStepToolSearchCall, GeminiInteractionsStepToolSearchResult:
		return []schemas.ResponsesMessage{geminiToolSearchStepToResponses(step, input)}, nil
	}
	if input {
		return nil, providerUtils.InvalidRequestErrorf("interactions %q steps are served by gemini and vertex only; route the request to one of them", step.Type)
	}
	return nil, nil
}

func geminiInteractionsBlocksToResponses(items []GeminiInteractionsItem, input bool) ([]schemas.ResponsesMessageContentBlock, error) {
	blocks := make([]schemas.ResponsesMessageContentBlock, 0, len(items))
	for _, item := range items {
		block, err := geminiInteractionsBlockToResponses(item, input)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

// geminiInteractionsResultToResponsesOutput reads a function_result's result: a string, an
// object (sent as its JSON text), or a list of content blocks.
func geminiInteractionsResultToResponsesOutput(raw json.RawMessage) (*schemas.ResponsesToolMessageOutputStruct, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		empty := ""
		return &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &empty}, nil
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := sonic.Unmarshal(trimmed, &s); err != nil {
			return nil, err
		}
		return &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &s}, nil
	case '[':
		var items []GeminiInteractionsItem
		if err := sonic.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		// A lone text block is the common case; send it as a plain string output.
		if len(items) == 1 && items[0].Type == GeminiContentTypeText && items[0].Text != nil {
			text := *items[0].Text
			return &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &text}, nil
		}
		blocks, err := geminiInteractionsBlocksToResponses(items, true)
		if err != nil {
			return nil, err
		}
		return &schemas.ResponsesToolMessageOutputStruct{ResponsesFunctionToolCallOutputBlocks: blocks}, nil
	}
	text := string(trimmed)
	return &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &text}, nil
}

func geminiInteractionsToolsToResponses(tools []GeminiInteractionsTool) ([]schemas.ResponsesTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]schemas.ResponsesTool, 0, len(tools))
	for _, tool := range tools {
		switch tool.Type {
		case "function":
			// short_description has no Responses field; a provider that searches deferred tools
			// reads the description instead.
			out = append(out, schemas.ResponsesTool{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  tool.Name,
				Description:           tool.Description,
				DeferLoading:          tool.DeferLoading,
				ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: tool.Parameters},
			})
		case geminiInteractionsToolSearch:
			search := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeToolSearch}
			if tool.Execution != nil && *tool.Execution == schemas.ToolSearchExecutionClient {
				search.Name, search.Description = tool.Name, tool.Description
				search.ResponsesToolToolSearch = &schemas.ResponsesToolToolSearch{Execution: tool.Execution, Parameters: tool.Parameters}
			}
			out = append(out, search)
		case "google_search":
			search := &schemas.ResponsesToolWebSearch{}
			for _, searchType := range tool.SearchTypes {
				switch searchType {
				case "web_search":
					search.SearchContentTypes = append(search.SearchContentTypes, geminiSearchContentTypeText)
				case "image_search":
					search.SearchContentTypes = append(search.SearchContentTypes, geminiSearchContentTypeImage)
				default:
					return nil, providerUtils.InvalidRequestErrorf("a google_search search_types value %q has no Responses equivalent; route the request to gemini or vertex", searchType)
				}
			}
			out = append(out, schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebSearch, ResponsesToolWebSearch: search})
		case "url_context":
			out = append(out, schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebFetch})
		case "code_execution":
			out = append(out, schemas.ResponsesTool{Type: schemas.ResponsesToolTypeCodeInterpreter,
				ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Container: map[string]interface{}{"type": "auto"}}}) // OpenAI requires one
		case "file_search":
			if len(tool.MetadataFilter) > 0 && !bytes.Equal(tool.MetadataFilter, []byte("null")) {
				// Dropping the filter would search documents the caller excluded.
				return nil, providerUtils.InvalidRequestErrorf("a file_search metadata_filter has no Responses equivalent; route the request to gemini or vertex")
			}
			out = append(out, schemas.ResponsesTool{
				Type:                    schemas.ResponsesToolTypeFileSearch,
				ResponsesToolFileSearch: &schemas.ResponsesToolFileSearch{VectorStoreIDs: tool.FileSearchStoreNames, MaxNumResults: tool.TopK},
			})
		case "mcp_server":
			// Google runs MCP tools with no approval step; Responses would default to "always".
			mcp := &schemas.ResponsesToolMCP{
				ServerURL:       tool.URL,
				RequireApproval: &schemas.ResponsesToolMCPAllowedToolsApprovalSetting{Setting: schemas.Ptr("never")},
			}
			if tool.Name != nil {
				mcp.ServerLabel = *tool.Name
			}
			if len(tool.Headers) > 0 {
				headers := tool.Headers
				mcp.Headers = &headers
			}
			names, err := geminiInteractionsMCPAllowedToolNames(tool.AllowedTools)
			if err != nil {
				return nil, err
			}
			if len(names) > 0 {
				mcp.AllowedTools = &schemas.ResponsesToolMCPAllowedTools{ToolNames: names}
			}
			out = append(out, schemas.ResponsesTool{Type: schemas.ResponsesToolTypeMCP, DeferLoading: tool.DeferLoading, ResponsesToolMCP: mcp})
		case "computer_use":
			// Another provider would offer the actions the caller excluded.
			if excluded := bytes.TrimSpace(tool.ExcludedPredefinedFunctions); len(excluded) > 0 && !bytes.Equal(excluded, []byte("null")) && !bytes.Equal(excluded, []byte("[]")) {
				return nil, providerUtils.InvalidRequestErrorf("computer_use excluded_predefined_functions has no Responses equivalent; route the request to gemini or vertex")
			}
			// Another provider would run the tool without the check the caller asked for.
			if tool.EnablePromptInjectionDetection != nil && *tool.EnablePromptInjectionDetection {
				return nil, providerUtils.InvalidRequestErrorf("computer_use enable_prompt_injection_detection has no Responses equivalent; route the request to gemini or vertex")
			}
			environment := "browser"
			if tool.Environment != nil && *tool.Environment != "" {
				environment = *tool.Environment
			}
			out = append(out, schemas.ResponsesTool{
				Type:                            schemas.ResponsesToolTypeComputerUsePreview,
				ResponsesToolComputerUsePreview: &schemas.ResponsesToolComputerUsePreview{Environment: environment},
			})
		default:
			return nil, providerUtils.InvalidRequestErrorf("interactions %q tools are served by gemini and vertex only; route the request to one of them", tool.Type)
		}
	}
	return out, nil
}

func geminiInteractionsToolChoiceToResponses(raw json.RawMessage) *schemas.ResponsesToolChoice {
	var mode string
	if err := sonic.Unmarshal(raw, &mode); err == nil {
		return geminiInteractionsModeToResponses(mode)
	}
	var cfg GeminiInteractionsToolChoiceConfig
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	if cfg.AllowedTools != nil && cfg.AllowedTools.Mode != nil && *cfg.AllowedTools.Mode == "none" {
		return geminiInteractionsModeToResponses("none")
	}
	if cfg.AllowedTools != nil && len(cfg.AllowedTools.Tools) > 0 {
		if len(cfg.AllowedTools.Tools) == 1 && cfg.AllowedTools.Mode != nil && *cfg.AllowedTools.Mode == "any" {
			name := cfg.AllowedTools.Tools[0]
			return &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeFunction, Name: &name}}
		}
		choiceMode := "auto"
		if cfg.AllowedTools.Mode != nil && *cfg.AllowedTools.Mode == "any" {
			choiceMode = "required"
		}
		defs := make([]schemas.ResponsesToolChoiceAllowedToolDef, 0, len(cfg.AllowedTools.Tools))
		for _, name := range cfg.AllowedTools.Tools {
			n := name
			defs = append(defs, schemas.ResponsesToolChoiceAllowedToolDef{Type: "function", Name: &n})
		}
		return &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeAllowedTools, Mode: &choiceMode, Tools: defs}}
	}
	if cfg.Mode != nil {
		return geminiInteractionsModeToResponses(*cfg.Mode)
	}
	return nil
}

func geminiInteractionsModeToResponses(mode string) *schemas.ResponsesToolChoice {
	var value string
	switch mode {
	case "auto", "validated":
		value = "auto"
	case "none":
		value = "none"
	case "any":
		value = "required"
	default:
		return nil
	}
	return &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: &value}
}

// geminiInteractionsResponseFormatTextOnly reports whether a response_format (one format or a
// list) asks for text output and nothing else.
func geminiInteractionsResponseFormatTextOnly(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var formats []GeminiInteractionsResponseFormat
		if sonic.Unmarshal(trimmed, &formats) != nil || len(formats) == 0 {
			return false
		}
		for _, f := range formats {
			if f.Type != "text" {
				return false
			}
		}
		return true
	}
	var format GeminiInteractionsResponseFormat
	return sonic.Unmarshal(trimmed, &format) == nil && format.Type == "text"
}

func geminiInteractionsResponseFormatToResponses(raw json.RawMessage) *schemas.ResponsesTextConfig {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var formats []GeminiInteractionsResponseFormat
		if err := sonic.Unmarshal(trimmed, &formats); err != nil {
			return nil
		}
		for _, f := range formats {
			if f.Type == "text" {
				return geminiInteractionsTextFormatToResponses(f)
			}
		}
		return nil
	}
	var format GeminiInteractionsResponseFormat
	if err := sonic.Unmarshal(trimmed, &format); err != nil || format.Type != "text" {
		return nil
	}
	return geminiInteractionsTextFormatToResponses(format)
}

func geminiInteractionsTextFormatToResponses(f GeminiInteractionsResponseFormat) *schemas.ResponsesTextConfig {
	if f.MimeType == nil || *f.MimeType != "application/json" {
		return nil
	}
	if len(f.Schema) == 0 {
		return &schemas.ResponsesTextConfig{Format: &schemas.ResponsesTextConfigFormat{Type: "json_object"}}
	}
	var schema schemas.ResponsesTextConfigFormatJSONSchema
	if err := sonic.Unmarshal(f.Schema, &schema); err != nil {
		return nil
	}
	name := "response"
	return &schemas.ResponsesTextConfig{Format: &schemas.ResponsesTextConfigFormat{Type: "json_schema", Name: &name, JSONSchema: &schema}}
}

// =============================================================================
// INTERACTION -> BIFROST RESPONSES RESPONSE
// =============================================================================

// ToBifrostResponsesResponse converts an Interaction into a Responses response. Model output,
// thoughts, and function calls become message, reasoning, and function_call items; built-in tool
// steps have no lossless Responses item and are kept in ProviderExtraFields["steps"] alongside
// the continuation token, so a native caller loses nothing.
func (i *GeminiInteraction) ToBifrostResponsesResponse() *schemas.BifrostResponsesResponse {
	return i.toBifrostResponsesResponse("")
}

// toBifrostResponsesResponse is ToBifrostResponsesResponse with the model requested, which an answer that
// names neither a model nor an agent ran. Search units depend on it.
func (i *GeminiInteraction) toBifrostResponsesResponse(requestModel string) *schemas.BifrostResponsesResponse {
	if i == nil {
		return nil
	}
	// Google gives a run it did not store (store: false) no id.
	id := i.ID
	if id == "" {
		id = newGeminiStatelessResponseID()
	}
	resp := &schemas.BifrostResponsesResponse{
		ID:        &id,
		Object:    "response",
		CreatedAt: parseGeminiInteractionTime(i.Created),
		Model:     i.Model,
		Output:    []schemas.ResponsesMessage{},
	}
	if resp.Model == "" {
		resp.Model = i.Agent
	}
	if resp.Model == "" {
		resp.Model = requestModel
	}
	resp.PreviousResponseID = i.PreviousInteractionID
	status := geminiInteractionStatusToResponses(i.Status)
	resp.Status = &status
	if status == schemas.ResponsesResponseStatusCompleted || status == schemas.ResponsesResponseStatusIncomplete {
		completed := parseGeminiInteractionTime(i.Updated)
		if completed == 0 {
			completed = resp.CreatedAt
		}
		resp.CompletedAt = &completed
	}
	switch i.Status {
	case GeminiInteractionStatusIncomplete:
		resp.IncompleteDetails = &schemas.ResponsesResponseIncompleteDetails{Reason: "max_output_tokens"}
	case GeminiInteractionStatusRequiresContinuation:
		// The run stopped at a checkpoint: the next request sends continuation_token (surfaced in
		// provider_extra_fields) to carry on from it.
		resp.IncompleteDetails = &schemas.ResponsesResponseIncompleteDetails{Reason: GeminiInteractionStatusRequiresContinuation}
	}
	if len(i.Errors) > 0 {
		resp.Error = &schemas.ResponsesResponseError{Code: i.Errors[0].Code, Message: i.Errors[0].Message}
	}
	if i.ServiceTier != nil && *i.ServiceTier != "" && *i.ServiceTier != "standard" {
		tier := schemas.BifrostServiceTier(*i.ServiceTier)
		resp.ServiceTier = &tier
	}

	var nativeSteps []GeminiInteractionsItem
	var suggestions []string
	var toolItems map[string][]int // call id -> output indexes; only built when a built-in tool ran
	for idx, step := range mcpSteps(i.Steps) {
		if step.Type == GeminiInteractionsStepUserInput || step.Type == GeminiInteractionsStepFunctionResult {
			continue
		}
		if isGeminiServerToolCall(step.Type) {
			items := geminiServerToolCallToResponses(step)
			for j := range items {
				if items[j].ID == nil || *items[j].ID == "" {
					itemID := geminiInteractionItemID(id, idx) + "_" + strconv.Itoa(j)
					items[j].ID = &itemID
				}
				if step.ID != nil {
					if toolItems == nil {
						toolItems = map[string][]int{}
					}
					toolItems[*step.ID] = append(toolItems[*step.ID], len(resp.Output)+j)
				}
			}
			resp.Output = append(resp.Output, items...)
			continue
		}
		if isGeminiServerToolResult(step.Type) {
			suggestions = append(suggestions, geminiSearchSuggestions(step)...)
			if step.CallID != nil {
				indexes := toolItems[*step.CallID]
				items := make([]*schemas.ResponsesMessage, 0, len(indexes))
				for _, k := range indexes {
					items = append(items, &resp.Output[k])
				}
				applyGeminiServerToolResult(items, step)
			}
			if step.Type == GeminiInteractionsStepCodeExecutionResult && geminiCodeExecutionHasArtifact(step.Result) {
				// A document or other artifact has no code interpreter output, so the whole step is kept.
				nativeSteps = append(nativeSteps, step)
			}
			continue
		}
		msgs, err := geminiInteractionsStepToResponses(step, false)
		if err != nil || len(msgs) == 0 {
			nativeSteps = append(nativeSteps, step)
			continue
		}
		for j := range msgs {
			if msgs[j].ID == nil {
				itemID := geminiInteractionItemID(id, idx)
				msgs[j].ID = &itemID
			}
		}
		resp.Output = append(resp.Output, msgs...)
	}
	hasEnvironment := i.EnvironmentID != nil && *i.EnvironmentID != ""
	if len(nativeSteps) > 0 || i.ContinuationToken != nil || i.Agent != "" || hasEnvironment || len(suggestions) > 0 {
		resp.ProviderExtraFields = map[string]interface{}{}
		if len(nativeSteps) > 0 {
			resp.ProviderExtraFields["steps"] = nativeSteps
		}
		if i.ContinuationToken != nil {
			resp.ProviderExtraFields["continuation_token"] = *i.ContinuationToken
		}
		if i.Agent != "" {
			resp.ProviderExtraFields["agent"] = i.Agent
		}
		if hasEnvironment {
			// The next request sends it as environment to reuse the sandbox.
			resp.ProviderExtraFields["environment_id"] = *i.EnvironmentID
		}
		if len(suggestions) > 0 {
			resp.ProviderExtraFields["search_suggestions"] = suggestions
		}
	}
	resp.Usage = i.Usage.toResponsesUsage(resp.Model)
	return resp
}

func geminiInteractionItemID(interactionID string, index int) string {
	return "item_" + interactionID + "_" + strconv.Itoa(index)
}

func geminiInteractionStatusToResponses(status string) string {
	switch status {
	case GeminiInteractionStatusCompleted, GeminiInteractionStatusRequiresAction:
		// requires_action is a finished turn whose function calls wait for results, which is
		// what a completed Responses response carrying function_call items means.
		return schemas.ResponsesResponseStatusCompleted
	case GeminiInteractionStatusFailed:
		return schemas.ResponsesResponseStatusFailed
	case GeminiInteractionStatusCancelled:
		return schemas.ResponsesResponseStatusCancelled
	case GeminiInteractionStatusIncomplete, GeminiInteractionStatusRequiresContinuation:
		return schemas.ResponsesResponseStatusIncomplete
	case "":
		return schemas.ResponsesResponseStatusCompleted
	}
	return schemas.ResponsesResponseStatusInProgress
}

func parseGeminiInteractionTime(value string) int {
	if value == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return int(t.Unix())
}

// toResponsesUsage maps Interactions usage. Thought tokens are billed as output, as on
// generateContent, and reported as reasoning tokens. Tool use prompt tokens (what a code run or a
// fetched page feeds back to the model) are billed as input and counted in total_tokens, so they
// are input here. Google Search runs from grounding_tool_count are the per-query search fee.
// total is the run's token count: the one reported, else the sum of its parts.
func (u *GeminiInteractionsUsage) total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.TotalInputTokens + u.TotalToolUseTokens + u.TotalOutputTokens + u.TotalThoughtTokens
}

// counted reports whether u counts any tokens, so an empty snapshot never replaces an earlier one.
func (u *GeminiInteractionsUsage) counted() bool {
	return u != nil && u.total() > 0
}

func (u *GeminiInteractionsUsage) toResponsesUsage(model string) *schemas.ResponsesResponseUsage {
	if u == nil {
		return nil
	}
	out := &schemas.ResponsesResponseUsage{
		InputTokens:         u.TotalInputTokens + u.TotalToolUseTokens,
		OutputTokens:        u.TotalOutputTokens + u.TotalThoughtTokens,
		TotalTokens:         u.total(),
		InputTokensDetails:  &schemas.ResponsesResponseInputTokens{CachedReadTokens: u.TotalCachedTokens},
		OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{ReasoningTokens: u.TotalThoughtTokens},
	}
	input, output := geminiInteractionsModalities(u.InputTokensByModality), geminiInteractionsModalities(u.OutputTokensByModality)
	out.InputTokensDetails.TextTokens, out.InputTokensDetails.AudioTokens, out.InputTokensDetails.ImageTokens = input.text, input.audio, input.image
	out.OutputTokensDetails.TextTokens, out.OutputTokensDetails.AudioTokens = output.text, output.audio
	if output.image > 0 {
		out.OutputTokensDetails.ImageTokens = schemas.Ptr(output.image)
	}
	if n := geminiInteractionsSearchUnits(u.GroundingToolCount, model); n > 0 {
		out.OutputTokensDetails.NumSearchQueries = schemas.Ptr(n)
		out.ToolUsage = &schemas.ToolUsage{WebSearch: &schemas.WebSearchToolUsage{NumRequests: n}}
	}
	return out
}

// =============================================================================
// BIFROST RESPONSES RESPONSE -> INTERACTION (translated path back to the client)
// =============================================================================

// ToGeminiInteraction renders a Responses response as an Interaction, for an Interactions caller
// whose request ran on a provider other than Gemini or Vertex.
func ToGeminiInteraction(resp *schemas.BifrostResponsesResponse) *GeminiInteraction {
	return ToGeminiInteractionWithClientSearch(resp, "")
}

// ToGeminiInteractionWithClientSearch is ToGeminiInteraction for a request that declared a client
// tool_search: a client search item that comes back without a name takes clientSearchName.
func ToGeminiInteractionWithClientSearch(resp *schemas.BifrostResponsesResponse, clientSearchName string) *GeminiInteraction {
	if resp == nil {
		return nil
	}
	out := &GeminiInteraction{Object: "interaction", Model: resp.Model, Steps: []GeminiInteractionsItem{}}
	if resp.ID != nil {
		out.ID = *resp.ID
	}
	if resp.CreatedAt > 0 {
		out.Created = time.Unix(int64(resp.CreatedAt), 0).UTC().Format(time.RFC3339)
		out.Updated = out.Created
	}
	if resp.CompletedAt != nil && *resp.CompletedAt > 0 {
		out.Updated = time.Unix(int64(*resp.CompletedAt), 0).UTC().Format(time.RFC3339)
	}
	out.PreviousInteractionID = resp.PreviousResponseID

	hasFunctionCall := false
	for _, item := range resp.Output {
		for _, step := range responsesItemToGeminiInteractionSteps(item, clientSearchName) {
			if step.Type == GeminiInteractionsStepFunctionCall {
				hasFunctionCall = true
			}
			out.Steps = append(out.Steps, step)
		}
	}
	out.Status = responsesStatusToGeminiInteraction(resp.Status, hasFunctionCall)
	if resp.Error != nil {
		out.Errors = []GeminiDecisionErrorBody{{Code: resp.Error.Code, Message: resp.Error.Message}}
	}
	out.Usage = responsesUsageToGeminiInteractions(resp.Usage)
	return out
}

func responsesStatusToGeminiInteraction(status *string, hasFunctionCall bool) string {
	if status == nil || *status == "" || *status == schemas.ResponsesResponseStatusCompleted {
		if hasFunctionCall {
			return GeminiInteractionStatusRequiresAction
		}
		return GeminiInteractionStatusCompleted
	}
	switch *status {
	case schemas.ResponsesResponseStatusFailed:
		return GeminiInteractionStatusFailed
	case schemas.ResponsesResponseStatusCancelled:
		return GeminiInteractionStatusCancelled
	case schemas.ResponsesResponseStatusIncomplete:
		return GeminiInteractionStatusIncomplete
	}
	return GeminiInteractionStatusInProgress
}

// responsesItemToGeminiInteractionSteps renders one output item as its steps: one for a message,
// a thought or a function call, a call and a result for a built-in tool.
func responsesItemToGeminiInteractionSteps(item schemas.ResponsesMessage, clientSearchName string) []GeminiInteractionsItem {
	if item.Type != nil && isResponsesServerToolItem(*item.Type) {
		return responsesServerToolItemToGeminiInteractionSteps(item)
	}
	if item.Type != nil && isResponsesToolSearchItem(*item.Type) {
		return responsesToolSearchItemToGeminiInteractionSteps(item, clientSearchName)
	}
	step, ok := responsesItemToGeminiInteractionStep(item)
	if !ok {
		return nil
	}
	return []GeminiInteractionsItem{step}
}

// responsesItemToGeminiInteractionStep renders a message, reasoning or function call item as a step.
func responsesItemToGeminiInteractionStep(item schemas.ResponsesMessage) (GeminiInteractionsItem, bool) {
	itemType := schemas.ResponsesMessageTypeMessage
	if item.Type != nil {
		itemType = *item.Type
	}
	switch itemType {
	case schemas.ResponsesMessageTypeMessage:
		var content []GeminiInteractionsItem
		if item.Content != nil {
			if item.Content.ContentStr != nil {
				text := *item.Content.ContentStr
				content = append(content, GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text})
			}
			for _, block := range item.Content.ContentBlocks {
				if block.Text != nil {
					text := *block.Text
					textItem := GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text}
					if block.ResponsesOutputMessageContentText != nil {
						textItem.Annotations = responsesAnnotationsToGeminiInteractions(block.ResponsesOutputMessageContentText.Annotations)
					}
					content = append(content, textItem)
				} else if block.ResponsesOutputMessageContentRefusal != nil {
					text := block.ResponsesOutputMessageContentRefusal.Refusal
					content = append(content, GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text})
				} else if block.Type == schemas.ResponsesInputMessageContentBlockTypeAudio || block.Type == schemas.ResponsesInputMessageContentBlockTypeImage || block.Type == schemas.ResponsesInputMessageContentBlockTypeFile {
					// Generated media keeps its block, as the input side renders it.
					media, err := toGeminiInteractionsContentBlocks(&schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{block}})
					if err == nil {
						content = append(content, media...)
					}
				}
			}
		}
		return GeminiInteractionsItem{Type: GeminiInteractionsStepModelOutput, Content: content}, true
	case schemas.ResponsesMessageTypeReasoning:
		step := GeminiInteractionsItem{Type: GeminiInteractionsStepThought}
		if item.ResponsesReasoning != nil {
			for _, s := range item.ResponsesReasoning.Summary {
				text := s.Text
				step.Summary = append(step.Summary, GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text})
			}
		}
		// Another provider's encrypted reasoning is not a Gemini signature; it is not sent.
		return step, true
	case schemas.ResponsesMessageTypeFunctionCall:
		if item.ResponsesToolMessage == nil {
			return GeminiInteractionsItem{}, false
		}
		tm := item.ResponsesToolMessage
		args := json.RawMessage("{}")
		if tm.Arguments != nil && json.Valid([]byte(*tm.Arguments)) {
			args = json.RawMessage(*tm.Arguments)
		}
		id := tm.CallID
		if id == nil {
			id = item.ID
		}
		return GeminiInteractionsItem{Type: GeminiInteractionsStepFunctionCall, ID: id, Name: tm.Name, Arguments: args}, true
	}
	return GeminiInteractionsItem{}, false
}

func responsesUsageToGeminiInteractions(u *schemas.ResponsesResponseUsage) *GeminiInteractionsUsage {
	if u == nil {
		return nil
	}
	out := &GeminiInteractionsUsage{
		TotalInputTokens:  u.InputTokens,
		TotalOutputTokens: u.OutputTokens,
		TotalTokens:       u.TotalTokens,
	}
	if u.OutputTokensDetails != nil && u.OutputTokensDetails.ReasoningTokens > 0 {
		out.TotalThoughtTokens = u.OutputTokensDetails.ReasoningTokens
		out.TotalOutputTokens -= u.OutputTokensDetails.ReasoningTokens
		if out.TotalOutputTokens < 0 {
			out.TotalOutputTokens = 0
		}
	}
	if u.InputTokensDetails != nil {
		out.TotalCachedTokens = u.InputTokensDetails.CachedReadTokens
	}
	searches := 0
	switch {
	case u.ToolUsage != nil && u.ToolUsage.WebSearch != nil:
		searches = u.ToolUsage.WebSearch.NumRequests
	case u.OutputTokensDetails != nil && u.OutputTokensDetails.NumSearchQueries != nil:
		searches = *u.OutputTokensDetails.NumSearchQueries
	}
	if searches > 0 {
		out.GroundingToolCount = []GeminiInteractionsGroundingToolCount{{Type: geminiInteractionsGoogleSearch, Count: searches}}
	}
	return out
}

// =============================================================================
// INTERACTIONS SSE -> BIFROST RESPONSES STREAM
// =============================================================================

// GeminiInteractionsStreamState follows one Interactions stream and turns its events into
// Responses stream events. Steps are addressed by index; each maps to one output item.
type GeminiInteractionsStreamState struct {
	statelessID string // response id minted when Google names none

	ID        string
	Model     string
	CreatedAt int
	started   bool
	steps     map[int]*geminiInteractionsStreamStep
	// RawReplay marks a stream whose frames go out as Google sent them.
	RawReplay   bool
	lostCalls   map[int]struct{}                // steps of the calls a resumed stream started inside
	lostResults map[string]struct{}             // calls answered by a built-in result a resumed stream has only the result of
	nativeSteps []GeminiInteractionsItem        // code results a Responses item cannot hold
	heldCalls   []*geminiInteractionsStreamStep // finished calls named "server:tool", until their result tells what they are
	releasing   bool                            // held calls are streaming as function calls
	outputIndex int
	Usage       *GeminiInteractionsUsage
	Status      string
	Completed   bool

	// ContinuationToken is the latest token the stream reported, for a run that stops at a
	// checkpoint (requires_continuation).
	ContinuationToken string
	// EnvironmentID is the agent sandbox the run uses; a follow-up turn sends it to reuse it.
	EnvironmentID string
	suggestions   []string // search widget markup the finished search steps carried
	// ServiceTier is the tier the stream reported serving the run at.
	ServiceTier *string
	// done holds every finished output item by output index. interaction.completed carries usage
	// but no steps, so the final response is assembled from these.
	done map[int]schemas.ResponsesMessage
	// tools holds the items of a built-in call until its result step closes them, by call id.
	tools map[string][]geminiInteractionsStreamToolItem
	// ClientToolSearchName is the name of the request's client tool_search; a function call to it
	// streams as a client tool_search_call. Empty when the request has none.
	ClientToolSearchName string
}

// geminiInteractionsStreamToolItem is one open item of a built-in tool call.
type geminiInteractionsStreamToolItem struct {
	outputIndex int
	item        schemas.ResponsesMessage
}

type geminiInteractionsStreamStep struct {
	stepType    string
	outputIndex int
	itemID      string
	callID      string
	name        string
	arguments   strings.Builder
	startArgs   string   // arguments the step.start carried, used when no fragment follows
	summaries   []string // thought summary items, each its own part
	signature   string
	held        bool                                   // a function_call named "server:tool", see heldCalls
	search      bool                                   // a function call to the client tool_search, streamed as a tool_search_call
	blocks      []schemas.ResponsesMessageContentBlock // an answer's parts in arrival order; the open text part's is filled when it closes
	textPart    *geminiInteractionsTextPart            // the text part written last, kept open for its citations
	tool        GeminiInteractionsItem                 // a built-in call or result step, filled in by its delta
}

// geminiInteractionsTextPart is one text part of an answer step. Media between two texts keeps them apart.
type geminiInteractionsTextPart struct {
	index       int
	text        strings.Builder
	annotations []schemas.ResponsesOutputMessageContentTextAnnotation
}

// NewGeminiInteractionsStreamState starts a stream state. model is the requested model, used
// until the stream names its own.
func NewGeminiInteractionsStreamState(model string) *GeminiInteractionsStreamState {
	return &GeminiInteractionsStreamState{
		Model: model, CreatedAt: int(time.Now().Unix()),
		steps: map[int]*geminiInteractionsStreamStep{}, done: map[int]schemas.ResponsesMessage{}, tools: map[string][]geminiInteractionsStreamToolItem{},
	}
}

// responseID is the id the stream's envelopes carry: the run's, or one minted for a run Google did not
// store, which has none. ID itself stays empty then, so the run is never polled.
func (s *GeminiInteractionsStreamState) responseID() string {
	if s.ID != "" {
		return s.ID
	}
	if s.statelessID == "" {
		s.statelessID = newGeminiStatelessResponseID()
	}
	return s.statelessID
}

func (s *GeminiInteractionsStreamState) response(status string) *schemas.BifrostResponsesResponse {
	id := s.responseID()
	resp := &schemas.BifrostResponsesResponse{ID: &id, Object: "response", CreatedAt: s.CreatedAt, Model: s.Model, Status: &status, Output: []schemas.ResponsesMessage{}}
	return resp
}

// ToBifrostResponsesStream converts one event. It returns no events for one that has no
// Responses counterpart (a status update between steps); the caller still has the raw event.
func (s *GeminiInteractionsStreamState) ToBifrostResponsesStream(event *GeminiInteractionsEvent) ([]*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError) {
	if event == nil {
		return nil, nil
	}
	var out []*schemas.BifrostResponsesStreamResponse
	ensureStarted := func() {
		if s.started {
			return
		}
		s.started = true
		out = append(out,
			&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeCreated, Response: s.response(schemas.ResponsesResponseStatusInProgress)},
			&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeInProgress, Response: s.response(schemas.ResponsesResponseStatusInProgress)},
		)
	}

	switch event.EventType {
	case GeminiInteractionsEventCreated:
		if event.Interaction != nil {
			s.absorbInteraction(event.Interaction)
		}
		ensureStarted()

	case GeminiInteractionsEventStatusUpdate:
		if event.InteractionID != nil && s.ID == "" {
			s.ID = *event.InteractionID
		}
		if event.Status != nil {
			s.Status = *event.Status
		}
		if event.ContinuationToken != nil && *event.ContinuationToken != "" {
			s.ContinuationToken = *event.ContinuationToken
		}
		// Usage on a status update is a snapshot of the run so far; the newest replaces the last.
		if event.Usage.counted() {
			s.Usage = event.Usage
		}

	case GeminiInteractionsEventStepStart:
		ensureStarted()
		if event.Index == nil || event.Step == nil {
			return out, nil
		}
		out = append(out, s.startStep(*event.Index, event.Step)...)
		out = append(out, s.applyStartContent(*event.Index, event.Step)...)

	case GeminiInteractionsEventStepDelta:
		ensureStarted()
		if usage := event.deltaUsage(); usage != nil {
			s.Usage = usage
		}
		if event.Index == nil || event.Delta == nil {
			return out, nil
		}
		out = append(out, s.applyDelta(*event.Index, event.Delta)...)

	case GeminiInteractionsEventStepStop:
		// step.stop can carry a usage snapshot too; the newest replaces the last.
		if event.Usage.counted() {
			s.Usage = event.Usage
		}
		if event.Index == nil {
			return out, nil
		}
		out = append(out, s.stopStep(*event.Index)...)

	case GeminiInteractionsEventCompleted:
		ensureStarted()
		if event.Interaction != nil {
			s.absorbInteraction(event.Interaction)
		}
		// Close any step the stream left open, and any built-in call whose result never came.
		out = append(out, s.closeOpenSteps(geminiInteractionStatusToResponses(s.Status) == schemas.ResponsesResponseStatusInProgress)...)
		s.Completed = true
		final := s.response(geminiInteractionStatusToResponses(s.Status))
		final.Output = s.streamedOutput()
		if event.Interaction != nil {
			full := event.Interaction.ToBifrostResponsesResponse()
			if full != nil && len(full.Output) > 0 {
				markGeminiClientToolSearchCalls(full.Output, s.ClientToolSearchName)
				final.Output = full.Output
			}
			s.recoverLost(mcpSteps(event.Interaction.Steps))
			if full != nil {
				final.ProviderExtraFields = full.ProviderExtraFields
				final.Error = full.Error
				final.IncompleteDetails = full.IncompleteDetails
				final.ServiceTier = full.ServiceTier
			}
		}
		if (len(s.lostCalls) > 0 || len(s.lostResults) > 0) && !s.RawReplay {
			message := "last_event_id resumes inside a function call, whose name and leading arguments came before it; retrieve the response, or resume from an earlier event"
			if len(s.lostCalls) == 0 {
				message = "last_event_id resumes between a built-in tool call and its result, and the call came before it; retrieve the response, or resume from an earlier event"
			}
			return nil, &schemas.BifrostError{IsBifrostError: false, StatusCode: schemas.Ptr(400), Error: &schemas.ErrorField{
				Type:    schemas.Ptr("invalid_request_error"),
				Message: message,
			}}
		}
		if final.ServiceTier == nil && s.ServiceTier != nil && *s.ServiceTier != "standard" {
			tier := schemas.BifrostServiceTier(*s.ServiceTier)
			final.ServiceTier = &tier
		}
		s.addProviderFields(final)
		if final.IncompleteDetails == nil && s.Status == GeminiInteractionStatusRequiresContinuation {
			final.IncompleteDetails = &schemas.ResponsesResponseIncompleteDetails{Reason: GeminiInteractionStatusRequiresContinuation}
		}
		final.Usage = s.Usage.toResponsesUsage(s.Model)
		eventType := schemas.ResponsesStreamResponseTypeCompleted
		if *final.Status == schemas.ResponsesResponseStatusInProgress {
			// The stream is over and the run is not: hand it off, as a stream that ends early does.
			return append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeInProgress, Response: final}), nil
		}
		completedAt := int(time.Now().Unix())
		final.CompletedAt = &completedAt
		switch *final.Status {
		case schemas.ResponsesResponseStatusFailed, schemas.ResponsesResponseStatusCancelled:
			eventType = schemas.ResponsesStreamResponseTypeFailed
		case schemas.ResponsesResponseStatusIncomplete:
			eventType = schemas.ResponsesStreamResponseTypeIncomplete
		}
		out = append(out, &schemas.BifrostResponsesStreamResponse{Type: eventType, Response: final})

	case GeminiInteractionsEventError:
		bifrostErr := &schemas.BifrostError{IsBifrostError: false, Error: &schemas.ErrorField{Message: "interactions stream error"}}
		if event.Error != nil {
			bifrostErr.Error.Message = event.Error.Message
			if event.Error.Code != "" {
				code := event.Error.Code
				bifrostErr.Error.Code = &code
			}
		}
		return out, bifrostErr
	}
	return out, nil
}

// recoverLost forgets what a resumed stream started inside and a completion's steps list whole: function
// calls, by the stream's index, which does not count the input a stored run's steps can start with, and
// built-in results, by the call they answer.
func (s *GeminiInteractionsStreamState) recoverLost(steps []GeminiInteractionsItem) {
	if len(s.lostCalls) == 0 && len(s.lostResults) == 0 {
		return
	}
	produced := make([]GeminiInteractionsItem, 0, len(steps))
	var called map[string]struct{}
	for _, step := range steps {
		if step.Type != GeminiInteractionsStepUserInput && step.Type != GeminiInteractionsStepFunctionResult {
			produced = append(produced, step)
		}
		switch {
		case step.ID != nil && isGeminiServerToolCall(step.Type):
			if called == nil {
				called = map[string]struct{}{}
			}
			called[*step.ID] = struct{}{}
		case step.CallID != nil && isGeminiServerToolResult(step.Type):
			if _, ok := called[*step.CallID]; ok {
				delete(s.lostResults, *step.CallID)
			}
		}
	}
	for index := range s.lostCalls {
		if index < 0 || index >= len(produced) {
			continue
		}
		if step := produced[index]; step.Type == GeminiInteractionsStepFunctionCall && step.ID != nil && *step.ID != "" && step.Name != nil && *step.Name != "" {
			delete(s.lostCalls, index)
		}
	}
}

func (s *GeminiInteractionsStreamState) absorbInteraction(i *GeminiInteraction) {
	if i.ID != "" {
		s.ID = i.ID
	}
	if i.Model != "" {
		s.Model = i.Model
	} else if i.Agent != "" {
		s.Model = i.Agent
	}
	if created := parseGeminiInteractionTime(i.Created); created > 0 {
		s.CreatedAt = created
	}
	if i.Status != "" {
		s.Status = i.Status
	}
	if i.Usage.counted() {
		s.Usage = i.Usage
	}
	if i.ContinuationToken != nil && *i.ContinuationToken != "" {
		s.ContinuationToken = *i.ContinuationToken
	}
	if i.EnvironmentID != nil && *i.EnvironmentID != "" {
		s.EnvironmentID = *i.EnvironmentID
	}
	if i.ServiceTier != nil && *i.ServiceTier != "" {
		s.ServiceTier = i.ServiceTier
	}
}

// addProviderFields adds the run's checkpoint token, sandbox id and search widgets to a final or handoff
// response.
func (s *GeminiInteractionsStreamState) addProviderFields(resp *schemas.BifrostResponsesResponse) {
	if s.ContinuationToken == "" && s.EnvironmentID == "" && len(s.suggestions) == 0 && len(s.nativeSteps) == 0 {
		return
	}
	if resp.ProviderExtraFields == nil {
		resp.ProviderExtraFields = map[string]interface{}{}
	}
	if _, ok := resp.ProviderExtraFields["continuation_token"]; !ok && s.ContinuationToken != "" {
		resp.ProviderExtraFields["continuation_token"] = s.ContinuationToken
	}
	if s.EnvironmentID != "" {
		resp.ProviderExtraFields["environment_id"] = s.EnvironmentID
	}
	if _, ok := resp.ProviderExtraFields["search_suggestions"]; !ok && len(s.suggestions) > 0 {
		resp.ProviderExtraFields["search_suggestions"] = s.suggestions
	}
	if _, ok := resp.ProviderExtraFields["steps"]; !ok && len(s.nativeSteps) > 0 {
		resp.ProviderExtraFields["steps"] = s.nativeSteps
	}
}

func (s *GeminiInteractionsStreamState) startStep(index int, step *GeminiInteractionsItem) []*schemas.BifrostResponsesStreamResponse {
	st := &geminiInteractionsStreamStep{stepType: step.Type, outputIndex: s.outputIndex, itemID: geminiInteractionItemID(s.responseID(), index)}
	var item *schemas.ResponsesMessage
	switch step.Type {
	case GeminiInteractionsStepModelOutput:
		msgType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant
		status := "in_progress"
		item = &schemas.ResponsesMessage{ID: &st.itemID, Type: &msgType, Role: &role, Status: &status, Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{}}}
	case GeminiInteractionsStepThought:
		msgType := schemas.ResponsesMessageTypeReasoning
		item = &schemas.ResponsesMessage{ID: &st.itemID, Type: &msgType, ResponsesReasoning: &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{}}}
		if step.Signature != nil {
			st.signature = *step.Signature
		}
	case GeminiInteractionsStepFunctionCall:
		if namesMCPServer(step.Name) && step.ID != nil && !s.releasing {
			// An MCP server call Google ran, or a function whose name looks like one: its result tells.
			st.tool, st.held, st.outputIndex = *step, true, -1
			s.steps[index] = st
			return nil
		}
		msgType := schemas.ResponsesMessageTypeFunctionCall
		status := "in_progress"
		if step.ID != nil {
			st.callID = *step.ID
			st.itemID = *step.ID
		}
		if step.Name != nil {
			st.name = *step.Name
		}
		if raw := bytes.TrimSpace(step.Arguments); len(raw) > 0 && raw[0] == '{' {
			st.startArgs = string(raw)
		}
		empty := ""
		callID, name := st.callID, st.name
		item = &schemas.ResponsesMessage{ID: &st.itemID, Type: &msgType, Status: &status, ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: &callID, Name: &name, Arguments: &empty}}
		if s.ClientToolSearchName != "" && st.name == s.ClientToolSearchName {
			// A client search item carries its arguments whole, as an object, so none stream as deltas.
			st.search = true
			toClientToolSearchCall(item)
			item.Arguments = nil
		}
	default:
		// A built-in tool step opens its items once its call is known (see stopStep); any other
		// step with no Responses item is not streamed.
		st.tool = *step
		s.steps[index] = st
		st.outputIndex = -1
		return nil
	}
	s.steps[index] = st
	s.outputIndex++
	outIdx := st.outputIndex
	return []*schemas.BifrostResponsesStreamResponse{{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &outIdx, Item: item}}
}

// applyStartContent streams the content a step.start already carries (a text prefix, a whole
// thought summary) through the same path as its deltas, so it is neither dropped from the stream
// nor from a final output built from streamed steps.
func (s *GeminiInteractionsStreamState) applyStartContent(index int, step *GeminiInteractionsItem) []*schemas.BifrostResponsesStreamResponse {
	var out []*schemas.BifrostResponsesStreamResponse
	switch step.Type {
	case GeminiInteractionsStepModelOutput:
		// All its text is one part, so it streams as one delta, which the native frame rides.
		text, sent := joinedTextBlocks(step.Content), false
		for i := range step.Content {
			block := &step.Content[i]
			switch block.Type {
			case GeminiContentTypeText:
				if !sent && text != "" {
					out = append(out, s.applyDelta(index, &GeminiInteractionsDelta{Type: geminiInteractionsDeltaText, Text: &text})...)
					sent = true
				}
				if len(block.Annotations) > 0 {
					out = append(out, s.applyDelta(index, &GeminiInteractionsDelta{Type: geminiInteractionsDeltaAnnotations, Annotations: block.Annotations})...)
				}
			case GeminiContentTypeImage, GeminiContentTypeAudio, GeminiContentTypeVideo, GeminiContentTypeDocument:
				out = append(out, s.applyDelta(index, &GeminiInteractionsDelta{Type: block.Type, Data: block.Data, URI: block.URI, MimeType: block.MimeType})...)
			}
		}
	case GeminiInteractionsStepThought:
		if text := joinedTextBlocks(step.Summary); text != "" {
			out = append(out, s.applyDelta(index, &GeminiInteractionsDelta{Type: geminiInteractionsDeltaThoughtSummary, Content: &GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text}})...)
		}
	}
	return out
}

// joinedTextBlocks is the text of blocks, in order.
func joinedTextBlocks(blocks []GeminiInteractionsItem) string {
	var sb strings.Builder
	for i := range blocks {
		if blocks[i].Type == GeminiContentTypeText && blocks[i].Text != nil {
			sb.WriteString(*blocks[i].Text)
		}
	}
	return sb.String()
}

func (s *GeminiInteractionsStreamState) applyDelta(index int, delta *GeminiInteractionsDelta) []*schemas.BifrostResponsesStreamResponse {
	st, ok := s.steps[index]
	if !ok {
		// A retrieve stream resumed after step.start sends deltas for a step it never announced.
		// Open the step the delta implies first, so its content is not dropped.
		var stepType string
		switch delta.Type {
		case geminiInteractionsDeltaText, GeminiContentTypeImage, GeminiContentTypeAudio, GeminiContentTypeVideo, GeminiContentTypeDocument:
			stepType = GeminiInteractionsStepModelOutput
		case geminiInteractionsDeltaThoughtSummary, geminiInteractionsDeltaThoughtSignature:
			stepType = GeminiInteractionsStepThought
		case geminiInteractionsDeltaArguments:
			// The call's id, name and leading arguments came before the cursor. A call without
			// them cannot be run or answered, so none is opened; see the completed event.
			if s.lostCalls == nil {
				s.lostCalls = map[int]struct{}{}
			}
			s.lostCalls[index] = struct{}{}
			return nil
		default:
			return nil
		}
		out := s.startStep(index, &GeminiInteractionsItem{Type: stepType})
		return append(out, s.applyDelta(index, delta)...)
	}
	if st.held && delta.Type == geminiInteractionsDeltaArguments {
		var fragment string
		if sonic.Unmarshal(delta.Arguments, &fragment) == nil {
			st.arguments.WriteString(fragment)
		}
		return nil
	}
	if delta.Type == st.stepType && (isGeminiServerToolCall(st.stepType) || isGeminiServerToolResult(st.stepType) || isGeminiToolSearchStep(st.stepType) || st.stepType == GeminiInteractionsStepFunctionResult) {
		// The whole call or result arrives in this one delta.
		if delta.Signature != nil && *delta.Signature != "" {
			st.tool.Signature = delta.Signature
		}
		if len(delta.Arguments) > 0 {
			st.tool.Arguments = delta.Arguments
		}
		if len(delta.Result) > 0 {
			st.tool.Result = delta.Result
		}
		if delta.IsError != nil {
			st.tool.IsError = delta.IsError
		}
		if delta.Name != nil {
			st.tool.Name = delta.Name
		}
		if delta.ServerName != nil {
			st.tool.ServerName = delta.ServerName
		}
		return nil
	}
	if st.outputIndex < 0 {
		return nil
	}
	outIdx := st.outputIndex
	itemID := st.itemID
	switch delta.Type {
	case geminiInteractionsDeltaText:
		if delta.Text == nil || st.stepType != GeminiInteractionsStepModelOutput {
			return nil
		}
		var out []*schemas.BifrostResponsesStreamResponse
		if st.needsTextPart() {
			out = st.openTextPart(outIdx, itemID)
		}
		contentIdx := st.textPart.index
		st.textPart.text.WriteString(*delta.Text)
		text := *delta.Text
		out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, OutputIndex: &outIdx, ContentIndex: &contentIdx, ItemID: &itemID, Delta: &text, LogProbs: []schemas.ResponsesOutputMessageContentTextLogProb{}})
		return out
	case geminiInteractionsDeltaThoughtSummary:
		if delta.Content == nil || delta.Content.Text == nil {
			return nil
		}
		// Each delta is a new summary item, whole.
		out := st.summaryDone(outIdx, itemID)
		summaryIdx := len(st.summaries)
		text := *delta.Content.Text
		st.summaries = append(st.summaries, text)
		out = append(out, &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded, OutputIndex: &outIdx, SummaryIndex: &summaryIdx, ItemID: &itemID,
			Part: &schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesOutputMessageContentTypeSummaryText, Text: new(string)},
		}, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta, OutputIndex: &outIdx, SummaryIndex: &summaryIdx, ItemID: &itemID, Delta: &text})
		return out
	case geminiInteractionsDeltaThoughtSignature:
		if delta.Signature != nil {
			st.signature = *delta.Signature
		}
		return nil
	case geminiInteractionsDeltaArguments:
		var fragment string
		if len(delta.Arguments) == 0 || st.stepType != GeminiInteractionsStepFunctionCall || sonic.Unmarshal(delta.Arguments, &fragment) != nil {
			return nil
		}
		st.arguments.WriteString(fragment)
		if st.search {
			return nil
		}
		return []*schemas.BifrostResponsesStreamResponse{{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, OutputIndex: &outIdx, ItemID: &itemID, Delta: &fragment}}
	case geminiInteractionsDeltaAnnotations:
		if st.stepType != GeminiInteractionsStepModelOutput {
			return nil
		}
		annotations := geminiInteractionsAnnotationsToResponses(delta.Annotations)
		if len(annotations) == 0 {
			return nil
		}
		var out []*schemas.BifrostResponsesStreamResponse
		if st.textPart == nil {
			out = st.openTextPart(outIdx, itemID)
		}
		contentIdx := st.textPart.index
		for _, a := range annotations {
			annotationIdx := len(st.textPart.annotations)
			st.textPart.annotations = append(st.textPart.annotations, a)
			annotation := a
			out = append(out, &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded, OutputIndex: &outIdx, ContentIndex: &contentIdx, ItemID: &itemID,
				AnnotationIndex: &annotationIdx, Annotation: &annotation,
			})
		}
		return out
	case GeminiContentTypeImage, GeminiContentTypeAudio, GeminiContentTypeVideo, GeminiContentTypeDocument:
		// Generated media arrives whole in one delta; it rides the message item when it closes.
		if st.stepType != GeminiInteractionsStepModelOutput || (delta.Data == nil && delta.URI == nil) {
			return nil
		}
		if block, err := geminiInteractionsBlockToResponses(GeminiInteractionsItem{Type: delta.Type, Data: delta.Data, URI: delta.URI, MimeType: delta.MimeType}, false); err == nil {
			st.blocks = append(st.blocks, block)
		}
		return nil
	}
	return nil
}

// summaryDone closes the summary item open last, if any.
func (st *geminiInteractionsStreamStep) summaryDone(outIdx int, itemID string) []*schemas.BifrostResponsesStreamResponse {
	if len(st.summaries) == 0 {
		return nil
	}
	summaryIdx := len(st.summaries) - 1
	text := st.summaries[summaryIdx]
	return []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone, OutputIndex: &outIdx, SummaryIndex: &summaryIdx, ItemID: &itemID, Text: &text},
		{Type: schemas.ResponsesStreamResponseTypeReasoningSummaryPartDone, OutputIndex: &outIdx, SummaryIndex: &summaryIdx, ItemID: &itemID, Part: &schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesOutputMessageContentTypeSummaryText, Text: &text}},
	}
}

// openTextPart closes the text part open before, if any, and opens the next one. Callers check needsTextPart
// first: the parameters escape, so calling it on every delta would allocate them on every delta.
func (st *geminiInteractionsStreamStep) openTextPart(outIdx int, itemID string) []*schemas.BifrostResponsesStreamResponse {
	out := st.closeTextPart(outIdx, itemID)
	contentIdx := len(st.blocks)
	st.textPart = &geminiInteractionsTextPart{index: contentIdx}
	st.blocks = append(st.blocks, schemas.ResponsesMessageContentBlock{})
	empty := ""
	return append(out, &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeContentPartAdded, OutputIndex: &outIdx, ContentIndex: &contentIdx, ItemID: &itemID,
		Part: &schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesOutputMessageContentTypeText, Text: &empty, ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{}}},
	})
}

// needsTextPart reports whether text opens a part: none is open, or media came after the one that is.
func (st *geminiInteractionsStreamStep) needsTextPart() bool {
	return st.textPart == nil || st.textPart.index != len(st.blocks)-1
}

// closeTextPart closes the open text part, if any, and puts it in its place among the blocks.
func (st *geminiInteractionsStreamStep) closeTextPart(outIdx int, itemID string) []*schemas.BifrostResponsesStreamResponse {
	part := st.textPart
	if part == nil {
		return nil
	}
	st.textPart = nil
	contentIdx, text, doneText := part.index, part.text.String(), part.text.String()
	annotations := part.annotations
	if annotations == nil {
		annotations = []schemas.ResponsesOutputMessageContentTextAnnotation{}
	}
	block := schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesOutputMessageContentTypeText, Text: &text, ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{Annotations: annotations}}
	st.blocks[part.index] = block
	return []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeOutputTextDone, OutputIndex: &outIdx, ContentIndex: &contentIdx, ItemID: &itemID, Text: &doneText, LogProbs: []schemas.ResponsesOutputMessageContentTextLogProb{}},
		{Type: schemas.ResponsesStreamResponseTypeContentPartDone, OutputIndex: &outIdx, ContentIndex: &contentIdx, ItemID: &itemID, Part: &block},
	}
}

// stopToolStep closes a built-in tool step. A call opens its items now that its arguments are
// known; its result finishes them.
func (s *GeminiInteractionsStreamState) stopToolStep(st *geminiInteractionsStreamStep) []*schemas.BifrostResponsesStreamResponse {
	var out []*schemas.BifrostResponsesStreamResponse
	switch {
	case st.held:
		if st.arguments.Len() > 0 {
			st.tool.Arguments = json.RawMessage(st.arguments.String())
		}
		s.heldCalls = append(s.heldCalls, st)
	case st.stepType == GeminiInteractionsStepFunctionResult:
		if st.tool.CallID == nil {
			return nil
		}
		at := slices.IndexFunc(s.heldCalls, func(call *geminiInteractionsStreamStep) bool { return *call.tool.ID == *st.tool.CallID })
		if at < 0 {
			return nil
		}
		call := s.heldCalls[at]
		s.heldCalls = slices.Delete(s.heldCalls, at, at+1)
		call.held, st.tool = false, mcpResultStep(st.tool)
		call.tool = mcpCallStep(call.tool)
		call.stepType, st.stepType = call.tool.Type, st.tool.Type
		return append(s.stopToolStep(call), s.stopToolStep(st)...)
	case isGeminiServerToolCall(st.stepType):
		items := geminiServerToolCallToResponses(st.tool)
		callID := ""
		if st.tool.ID != nil {
			callID = *st.tool.ID
		}
		open := make([]geminiInteractionsStreamToolItem, 0, len(items))
		for j := range items {
			if items[j].ID == nil || *items[j].ID == "" {
				itemID := st.itemID + "_" + strconv.Itoa(j)
				items[j].ID = &itemID
			}
			outIdx := s.outputIndex
			s.outputIndex++
			// The announced item is a deep copy: the result step later fills the open item in place,
			// and an event already handed to a consumer must not change under it.
			added := cloneGeminiInteractionsResponsesMessage(items[j])
			out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &outIdx, Item: &added})
			open = append(open, geminiInteractionsStreamToolItem{outputIndex: outIdx, item: items[j]})
		}
		if callID == "" {
			return append(out, s.finishTools(open)...)
		}
		s.tools[callID] = append(s.tools[callID], open...)
	case isGeminiServerToolResult(st.stepType):
		s.suggestions = append(s.suggestions, geminiSearchSuggestions(st.tool)...)
		if st.tool.CallID == nil {
			return nil
		}
		open, announced := s.tools[*st.tool.CallID]
		delete(s.tools, *st.tool.CallID)
		if !announced {
			// A resumed stream can start past the call this answers.
			if s.lostResults == nil {
				s.lostResults = map[string]struct{}{}
			}
			s.lostResults[*st.tool.CallID] = struct{}{}
		}
		items := make([]*schemas.ResponsesMessage, 0, len(open))
		for k := range open {
			items = append(items, &open[k].item)
		}
		applyGeminiServerToolResult(items, st.tool)
		if st.stepType == GeminiInteractionsStepCodeExecutionResult && geminiCodeExecutionHasArtifact(st.tool.Result) {
			s.nativeSteps = append(s.nativeSteps, st.tool) // as the unary conversion keeps it
		}
		out = append(out, s.finishTools(open)...)
	case isGeminiToolSearchStep(st.stepType):
		// A search call or result is one whole item, known once its step ends.
		item := geminiToolSearchStepToResponses(st.tool, false)
		if item.ID == nil || *item.ID == "" {
			itemID := st.itemID
			item.ID = &itemID
		}
		outIdx := s.outputIndex
		s.outputIndex++
		added := cloneGeminiInteractionsResponsesMessage(item)
		out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &outIdx, Item: &added})
		out = append(out, s.finishTools([]geminiInteractionsStreamToolItem{{outputIndex: outIdx, item: item}})...)
	}
	return out
}

// cloneGeminiInteractionsResponsesMessage deep-copies an item through its JSON form. Built-in tool
// calls are rare, so the round trip is off the text hot path.
func cloneGeminiInteractionsResponsesMessage(item schemas.ResponsesMessage) schemas.ResponsesMessage {
	raw, err := sonic.Marshal(item)
	if err != nil {
		return item
	}
	var out schemas.ResponsesMessage
	if sonic.Unmarshal(raw, &out) != nil {
		return item
	}
	return out
}

// closeOpenSteps closes what the stream left open as it stands, in the order it opened, so a built-in call
// closes before its result. A run still going may send the rest of a
// function call's arguments, so that call is left open: reported done, it would be run on half of them.
func (s *GeminiInteractionsStreamState) closeOpenSteps(stillRunning bool) []*schemas.BifrostResponsesStreamResponse {
	var out []*schemas.BifrostResponsesStreamResponse
	for _, idx := range slices.Sorted(maps.Keys(s.steps)) {
		if stillRunning && s.steps[idx].stepType == GeminiInteractionsStepFunctionCall {
			continue
		}
		out = append(out, s.stopStep(idx)...)
	}
	if !stillRunning {
		out = append(out, s.releaseHeldCalls()...)
	}
	return append(out, s.closeOpenTools()...)
}

// Handoff ends a stream the upstream closed while a background run is still going. Open items but a
// function call are closed as they stand and the last event is response.in_progress carrying the run id, so a client
// knows to poll the run rather than take the partial output as final.
func (s *GeminiInteractionsStreamState) Handoff() []*schemas.BifrostResponsesStreamResponse {
	out := s.closeOpenSteps(true)
	resp := s.response(schemas.ResponsesResponseStatusInProgress)
	resp.Output = s.streamedOutput()
	resp.Usage = s.Usage.toResponsesUsage(s.Model)
	s.addProviderFields(resp)
	return append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeInProgress, Response: resp})
}

// finishTools emits output_item.done for built-in tool items and records them.
func (s *GeminiInteractionsStreamState) finishTools(items []geminiInteractionsStreamToolItem) []*schemas.BifrostResponsesStreamResponse {
	out := make([]*schemas.BifrostResponsesStreamResponse, 0, len(items))
	for _, open := range items {
		outIdx := open.outputIndex
		item := open.item
		s.done[outIdx] = item
		out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &outIdx, Item: &item})
	}
	return out
}

// releaseHeldCalls streams the calls no result answered as the function calls they are.
func (s *GeminiInteractionsStreamState) releaseHeldCalls() []*schemas.BifrostResponsesStreamResponse {
	if len(s.heldCalls) == 0 {
		return nil
	}
	var out []*schemas.BifrostResponsesStreamResponse
	held := s.heldCalls
	s.heldCalls, s.releasing = nil, true
	for i, call := range held {
		index := -1 - i // the step's own index is free again and may have been reused
		out = append(out, s.startStep(index, &call.tool)...)
		out = append(out, s.stopStep(index)...)
	}
	s.releasing = false
	return out
}

// closeOpenTools finishes the built-in calls whose result never arrived, as they stand.
func (s *GeminiInteractionsStreamState) closeOpenTools() []*schemas.BifrostResponsesStreamResponse {
	var open []geminiInteractionsStreamToolItem
	for callID, items := range s.tools {
		open = append(open, items...)
		delete(s.tools, callID)
	}
	slices.SortFunc(open, func(a, b geminiInteractionsStreamToolItem) int { return a.outputIndex - b.outputIndex })
	return s.finishTools(open)
}

// streamedOutput is the finished items in output order.
func (s *GeminiInteractionsStreamState) streamedOutput() []schemas.ResponsesMessage {
	out := make([]schemas.ResponsesMessage, 0, len(s.done))
	for idx := 0; idx < s.outputIndex; idx++ {
		if item, ok := s.done[idx]; ok {
			out = append(out, item)
		}
	}
	return out
}

func (s *GeminiInteractionsStreamState) stopStep(index int) []*schemas.BifrostResponsesStreamResponse {
	st, ok := s.steps[index]
	if !ok {
		return nil
	}
	delete(s.steps, index)
	if st.outputIndex < 0 {
		return s.stopToolStep(st)
	}
	outIdx := st.outputIndex
	itemID := st.itemID
	completed := "completed"
	var out []*schemas.BifrostResponsesStreamResponse
	switch st.stepType {
	case GeminiInteractionsStepModelOutput:
		out = append(out, st.closeTextPart(outIdx, itemID)...)
		msgType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant
		blocks := st.blocks
		if blocks == nil {
			blocks = []schemas.ResponsesMessageContentBlock{}
		}
		out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &outIdx, Item: &schemas.ResponsesMessage{ID: &itemID, Type: &msgType, Role: &role, Status: &completed, Content: &schemas.ResponsesMessageContent{ContentBlocks: blocks}}})
	case GeminiInteractionsStepThought:
		reasoning := &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{}}
		if st.signature != "" {
			sig := st.signature
			reasoning.EncryptedContent = &sig
		}
		if len(st.summaries) > 0 {
			summaryIdx := len(st.summaries) - 1
			for _, text := range st.summaries {
				reasoning.Summary = append(reasoning.Summary, schemas.ResponsesReasoningSummary{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: text})
			}
			if st.signature != "" {
				// An item built from its deltas (the log accumulator's) takes the signature from one too.
				sig := st.signature
				out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta, OutputIndex: &outIdx, SummaryIndex: &summaryIdx, ItemID: &itemID, Signature: &sig})
			}
			out = append(out, st.summaryDone(outIdx, itemID)...)
		}
		msgType := schemas.ResponsesMessageTypeReasoning
		out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &outIdx, Item: &schemas.ResponsesMessage{ID: &itemID, Type: &msgType, ResponsesReasoning: reasoning}})
	case GeminiInteractionsStepFunctionCall:
		args := st.arguments.String()
		if args == "" && st.startArgs != "" {
			args = st.startArgs
		}
		if st.arguments.Len() == 0 && st.startArgs != "" && !st.search {
			// The whole arguments came on step.start. They stream here, once no fragment can replace them.
			delta := args
			out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, OutputIndex: &outIdx, ItemID: &itemID, Delta: &delta})
		}
		if args == "" {
			args = "{}"
		}
		msgType := schemas.ResponsesMessageTypeFunctionCall
		callID, name := st.callID, st.name
		item := &schemas.ResponsesMessage{ID: &itemID, Type: &msgType, Status: &completed, ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: &callID, Name: &name, Arguments: &args}}
		if st.search {
			toClientToolSearchCall(item)
		} else {
			doneArgs := args
			out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, OutputIndex: &outIdx, ItemID: &itemID, Arguments: &doneArgs})
		}
		out = append(out, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &outIdx, Item: item})
	}
	for _, ev := range out {
		if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && ev.Item != nil {
			s.done[outIdx] = *ev.Item
		}
	}
	return out
}

// ParseGeminiInteractionsEvent decodes one SSE data payload.
func ParseGeminiInteractionsEvent(data []byte) (*GeminiInteractionsEvent, error) {
	var event GeminiInteractionsEvent
	if err := sonic.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// =============================================================================
// BIFROST RESPONSES STREAM -> INTERACTIONS SSE (translated path back to the client)
// =============================================================================

// BifrostToGeminiInteractionsStreamState renders a Responses stream as Interactions events for a
// caller whose request ran on a provider other than Gemini or Vertex. Each output item becomes a
// step. Steps are numbered as they open on the stream, and the final steps follow that numbering,
// so a live index is the step's position in the finished interaction.
type BifrostToGeminiInteractionsStreamState struct {
	id      string
	model   string
	eventID int
	steps   map[int]string                   // step index -> step type, for steps still open
	index   map[int]int                      // output index -> step index
	next    int                              // next step index; a built-in tool item takes two
	tools   map[int]bool                     // output indexes of built-in tool items, rendered when they finish
	first   map[int]int                      // output index -> its first step index, for every item streamed
	ids     map[string]int                   // item id -> output index, for every item streamed
	done    map[int]schemas.ResponsesMessage // finished built-in tool items, by output index
	usage   *schemas.ResponsesResponseUsage

	ClientToolSearchName string // the request's client tool_search name, for unnamed client search items
}

// NewBifrostToGeminiInteractionsStreamState starts a state for one stream.
func NewBifrostToGeminiInteractionsStreamState() *BifrostToGeminiInteractionsStreamState {
	return &BifrostToGeminiInteractionsStreamState{steps: map[int]string{}, index: map[int]int{}, tools: map[int]bool{}, first: map[int]int{}, ids: map[string]int{}, done: map[int]schemas.ResponsesMessage{}}
}

// identify records which output index item streamed under.
func (s *BifrostToGeminiInteractionsStreamState) identify(item *schemas.ResponsesMessage, outputIndex int) {
	if item.ID != nil && *item.ID != "" {
		s.ids[*item.ID] = outputIndex
	}
}

// inStepOrder is resp with its output ordered as its steps were numbered on the stream. A built-in
// tool is numbered when it finishes, which can be after a later item opened, and a final output can
// omit one that streamed, so an item is found by its id, then by its position when nothing was omitted.
// An omitted tool is put back; items that never streamed keep their order at the end. The provider's
// response is not modified.
func (s *BifrostToGeminiInteractionsStreamState) inStepOrder(resp *schemas.BifrostResponsesResponse) *schemas.BifrostResponsesResponse {
	if resp == nil || (len(resp.Output) == 0 && len(s.done) == 0) {
		return resp
	}
	type placed struct {
		item schemas.ResponsesMessage
		step int
	}
	items := make([]placed, 0, len(resp.Output)+len(s.done))
	claimed := make(map[int]bool, len(resp.Output))
	dense := len(resp.Output) >= len(s.first)
	for i, item := range resp.Output {
		outputIndex, known := i, dense
		if item.ID != nil {
			if at, ok := s.ids[*item.ID]; ok {
				outputIndex, known = at, true
			}
		}
		step := s.next + i
		if first, ok := s.first[outputIndex]; ok && known {
			step, claimed[outputIndex] = first, true
		}
		items = append(items, placed{item, step})
	}
	if !dense {
		for outputIndex, item := range s.done {
			if !claimed[outputIndex] {
				items = append(items, placed{item, s.first[outputIndex]})
			}
		}
	}
	byStep := func(a, b placed) int { return a.step - b.step }
	if len(items) == len(resp.Output) && slices.IsSortedFunc(items, byStep) {
		return resp
	}
	slices.SortStableFunc(items, byStep)
	ordered := *resp
	ordered.Output = make([]schemas.ResponsesMessage, len(items))
	for i := range items {
		ordered.Output[i] = items[i].item
	}
	return &ordered
}

// stepIndex is the step index an output index streams under.
func (s *BifrostToGeminiInteractionsStreamState) stepIndex(outputIndex int) (int, bool) {
	idx, ok := s.index[outputIndex]
	return idx, ok
}

func (s *BifrostToGeminiInteractionsStreamState) nextEventID() *string {
	s.eventID++
	id := "evt_" + strconv.Itoa(s.eventID)
	return &id
}

// ToGeminiInteractionsStreamEvents converts one Responses stream event. Events with no
// Interactions counterpart produce none.
func (s *BifrostToGeminiInteractionsStreamState) ToGeminiInteractionsStreamEvents(resp *schemas.BifrostResponsesStreamResponse) []*GeminiInteractionsEvent {
	if resp == nil {
		return nil
	}
	var out []*GeminiInteractionsEvent
	switch resp.Type {
	case schemas.ResponsesStreamResponseTypeCreated:
		if resp.Response != nil {
			if resp.Response.ID != nil {
				s.id = *resp.Response.ID
			}
			s.model = resp.Response.Model
		}
		out = append(out, &GeminiInteractionsEvent{
			EventType:   GeminiInteractionsEventCreated,
			EventID:     s.nextEventID(),
			Interaction: &GeminiInteraction{ID: s.id, Object: "interaction", Model: s.model, Status: GeminiInteractionStatusInProgress},
		})

	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		if resp.OutputIndex == nil || resp.Item == nil {
			return nil
		}
		if resp.Item.Type != nil && (isResponsesServerToolItem(*resp.Item.Type) || isResponsesToolSearchItem(*resp.Item.Type)) {
			// A built-in tool's call and result are only known once it finishes.
			s.tools[*resp.OutputIndex] = true
			return nil
		}
		step, ok := responsesItemToGeminiInteractionStep(*resp.Item)
		if !ok {
			return nil
		}
		// Start events carry the step's identity only; content arrives as deltas.
		step.Content, step.Summary = nil, nil
		if step.Type == GeminiInteractionsStepFunctionCall {
			step.Arguments = json.RawMessage("{}")
		}
		idx := s.next
		s.next++
		s.index[*resp.OutputIndex] = idx
		s.first[*resp.OutputIndex] = idx
		s.identify(resp.Item, *resp.OutputIndex)
		s.steps[idx] = step.Type
		out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepStart, EventID: s.nextEventID(), Index: &idx, Step: &step})

	case schemas.ResponsesStreamResponseTypeOutputTextDelta, schemas.ResponsesStreamResponseTypeRefusalDelta:
		if resp.OutputIndex == nil || resp.Delta == nil {
			return nil
		}
		idx, ok := s.stepIndex(*resp.OutputIndex)
		if !ok {
			return nil
		}
		text := *resp.Delta
		out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepDelta, EventID: s.nextEventID(), Index: &idx, Delta: &GeminiInteractionsDelta{Type: geminiInteractionsDeltaText, Text: &text}})

	case schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded:
		if resp.OutputIndex == nil || resp.Annotation == nil {
			return nil
		}
		idx, ok := s.stepIndex(*resp.OutputIndex)
		if !ok {
			return nil
		}
		annotations := responsesAnnotationsToGeminiInteractions([]schemas.ResponsesOutputMessageContentTextAnnotation{*resp.Annotation})
		if annotations == nil {
			return nil
		}
		out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepDelta, EventID: s.nextEventID(), Index: &idx, Delta: &GeminiInteractionsDelta{Type: geminiInteractionsDeltaAnnotations, Annotations: annotations}})

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta, schemas.ResponsesStreamResponseTypeReasoningTextDelta:
		if resp.OutputIndex == nil || resp.Delta == nil {
			return nil
		}
		idx, ok := s.stepIndex(*resp.OutputIndex)
		if !ok {
			return nil
		}
		text := *resp.Delta
		out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepDelta, EventID: s.nextEventID(), Index: &idx, Delta: &GeminiInteractionsDelta{Type: geminiInteractionsDeltaThoughtSummary, Content: &GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text}}})

	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		if resp.OutputIndex == nil || resp.Delta == nil {
			return nil
		}
		idx, ok := s.stepIndex(*resp.OutputIndex)
		if !ok {
			return nil
		}
		fragment, err := providerUtils.MarshalSorted(*resp.Delta)
		if err != nil {
			return nil
		}
		out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepDelta, EventID: s.nextEventID(), Index: &idx, Delta: &GeminiInteractionsDelta{Type: geminiInteractionsDeltaArguments, Arguments: fragment}})

	case schemas.ResponsesStreamResponseTypeOutputItemDone:
		if resp.OutputIndex == nil {
			return nil
		}
		if s.tools[*resp.OutputIndex] {
			delete(s.tools, *resp.OutputIndex)
			if resp.Item == nil {
				return nil
			}
			// A built-in tool finished: its call and its result, each a whole step.
			s.first[*resp.OutputIndex] = s.next
			s.done[*resp.OutputIndex] = *resp.Item
			s.identify(resp.Item, *resp.OutputIndex)
			for _, step := range responsesItemToGeminiInteractionSteps(*resp.Item, s.ClientToolSearchName) {
				idx := s.next
				s.next++
				startStep := step
				out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepStart, EventID: s.nextEventID(), Index: &idx, Step: &startStep})
				if step.Type == GeminiInteractionsStepFunctionCall && len(step.Arguments) > 0 {
					// The caller's search is a function call: its arguments stream as a delta, as Google
					// streams them, so a reader that builds the call from deltas keeps them.
					startStep.Arguments = nil
					if fragment, err := providerUtils.MarshalSorted(string(step.Arguments)); err == nil {
						out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepDelta, EventID: s.nextEventID(), Index: &idx, Delta: &GeminiInteractionsDelta{Type: geminiInteractionsDeltaArguments, Arguments: fragment}})
					}
				}
				out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepStop, EventID: s.nextEventID(), Index: &idx})
			}
			return out
		}
		idx, ok := s.stepIndex(*resp.OutputIndex)
		if !ok {
			return nil
		}
		if _, open := s.steps[idx]; !open {
			return nil
		}
		delete(s.steps, idx)
		if resp.Item != nil {
			// Generated media has no delta event on a Responses stream, so it streams here, before
			// the step closes. Text already went out as deltas.
			if step, ok := responsesItemToGeminiInteractionStep(*resp.Item); ok && step.Type == GeminiInteractionsStepModelOutput {
				for _, c := range step.Content {
					if c.Type == GeminiContentTypeText {
						continue
					}
					out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepDelta, EventID: s.nextEventID(), Index: &idx,
						Delta: &GeminiInteractionsDelta{Type: c.Type, Data: c.Data, URI: c.URI, MimeType: c.MimeType}})
				}
			}
		}
		out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepStop, EventID: s.nextEventID(), Index: &idx})

	case schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesStreamResponseTypeIncomplete, schemas.ResponsesStreamResponseTypeFailed:
		for _, idx := range slices.Sorted(maps.Keys(s.steps)) {
			i := idx
			out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventStepStop, EventID: s.nextEventID(), Index: &i})
		}
		s.steps = map[int]string{}
		final := ToGeminiInteractionWithClientSearch(s.inStepOrder(resp.Response), s.ClientToolSearchName)
		if final == nil {
			final = &GeminiInteraction{Object: "interaction", Status: GeminiInteractionStatusCompleted}
		}
		if final.ID == "" {
			final.ID = s.id
		}
		if final.Model == "" {
			final.Model = s.model
		}
		out = append(out, &GeminiInteractionsEvent{EventType: GeminiInteractionsEventCompleted, EventID: s.nextEventID(), Interaction: final})
	}
	return out
}

// ToGeminiInteractionsErrorBody renders an error in the Interactions shape: a string code and a
// message.
func ToGeminiInteractionsErrorBody(err *schemas.BifrostError) *GeminiDecisionError {
	if err == nil {
		return nil
	}
	body := &GeminiDecisionErrorBody{Code: "internal", Message: "internal error"}
	if err.Error != nil {
		if err.Error.Message != "" {
			body.Message = err.Error.Message
		}
		if err.Error.Code != nil && *err.Error.Code != "" {
			body.Code = *err.Error.Code
		} else if err.Error.Type != nil && *err.Error.Type != "" {
			body.Code = *err.Error.Type
		}
		// Vertex repeats the HTTP status as the code; the Interactions code is a word.
		if _, numeric := strconv.Atoi(body.Code); numeric == nil {
			body.Code = "internal"
		}
		// A router failure carries the caller's mistake under its own generic message.
		if invalid, ok := providerUtils.AsBifrostBadRequestError(err.Error.Error); ok {
			body.Code, body.Message = "invalid_request", invalid.Error.Message
		}
	}
	if err.StatusCode != nil {
		if *err.StatusCode == 404 {
			body.Code = "not_found"
		} else if code, ok := geminiInteractionsErrorCodes[*err.StatusCode]; ok && body.Code == "internal" {
			body.Code = code
		}
	}
	return &GeminiDecisionError{Error: body}
}

// geminiInteractionsErrorCodes are the codes Google documents for each status, for an error that names
// none of its own.
var geminiInteractionsErrorCodes = map[int]string{
	400: "invalid_request",
	401: "authentication",
	402: "payment_required",
	403: "permission_denied",
	409: "aborted",
	429: "rate_limit_exceeded",
	501: "unimplemented",
	503: "service_unavailable",
	504: "deadline_exceeded",
}
