package heimdall

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Eligibility codes. A session has exactly one; the first check that fails decides it.
const (
	CodeEligible          = "eligible"
	CodeUnknownSession    = "unknown_session"    // no rows found
	CodeNoToolCalls       = "no_tool_calls"      // nothing behavioural to test
	CodeContentHidden     = "content_hidden"     // content logging was off
	CodeIncomplete        = "incomplete"         // the last row did not succeed, or has no output
	CodeUnsupportedAPI    = "unsupported_api"    // not Chat Completions
	CodeMultiUserTurn     = "multi_user_turn"    // user messages after the first assistant reply
	CodeNonTextContent    = "non_text_content"   // images, audio or files in the seed or a tool result
	CodeLargePayload      = "large_payload"      // content may be partial
	CodeHostedTool        = "hosted_tool"        // the provider would run the tool
	CodeInjectedTool      = "injected_tool"      // tool calls that the history does not show
	CodeMixedRoute        = "mixed_route"        // rows used more than one provider and model
	CodeRouteMismatch     = "route_mismatch"     // not the run's baseline route
	CodeUnsupportedParams = "unsupported_params" // a param that changes behaviour and is not on the allow-list
	CodeToolsChanged      = "tools_changed"      // declared tools differ between rows
	CodeHistoryRewritten  = "history_rewritten"  // a row does not extend the one before it
	CodeUnpairedToolCall  = "unpaired_tool_call" // a call without a result, or a result without a call
	CodeTooLong           = "too_long"           // more than max_steps steps
)

// Eligibility says whether a session can be replayed, and if not, why.
type Eligibility struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Eligible reports whether the session can be replayed.
func (e Eligibility) Eligible() bool { return e.Code == CodeEligible }

func ineligible(code, format string, args ...any) Eligibility {
	return Eligibility{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Object types of the rows that make a chat session.
const (
	objectChat       = "chat_completion"
	objectChatStream = "chat_completion_stream"
)

const statusSuccess = "success"

// checkRow returns the reason a single row rules its session out, or "". It reads only what the row
// carries, so it works on a list row (scalar columns only) and on a full row, where it also checks
// the logged parameters and tools.
func checkRow(row *logstore.Log) (code string) {
	switch {
	case row.ContentHidden:
		return CodeContentHidden
	case row.IsPassthrough || (row.Object != objectChat && row.Object != objectChatStream):
		return CodeUnsupportedAPI
	case row.IsLargePayloadRequest || row.IsLargePayloadResponse:
		return CodeLargePayload
	}
	params, err := decodeParams(row)
	if err != nil {
		return CodeUnsupportedParams
	}
	if params == nil {
		params = &schemas.ChatParameters{}
	}
	if hostedTools(declaredTools(row, params)) || usesHostedFeature(params) {
		return CodeHostedTool
	}
	if usesUnsupportedParam(params) {
		return CodeUnsupportedParams
	}
	return ""
}

// decodeParams reads the logged parameters, which the store hands back as an untyped value.
func decodeParams(row *logstore.Log) (*schemas.ChatParameters, error) {
	if row.ParamsParsed == nil {
		return nil, nil
	}
	b, err := json.Marshal(row.ParamsParsed)
	if err != nil {
		return nil, err
	}
	var params schemas.ChatParameters
	if err := json.Unmarshal(b, &params); err != nil {
		return nil, err
	}
	return &params, nil
}

// declaredTools returns the tools the row offered the model.
func declaredTools(row *logstore.Log, params *schemas.ChatParameters) []schemas.ChatTool {
	if len(row.ToolsParsed) > 0 {
		return row.ToolsParsed
	}
	if params != nil {
		return params.Tools
	}
	return nil
}

func hostedTools(tools []schemas.ChatTool) bool {
	for _, t := range tools {
		if t.Type != schemas.ChatToolTypeFunction || t.Function == nil {
			return true
		}
	}
	return false
}

// usesHostedFeature reports a parameter that makes the provider run something itself.
func usesHostedFeature(p *schemas.ChatParameters) bool {
	return len(p.MCPServers) > 0 ||
		p.Container != nil ||
		p.WebSearchOptions != nil ||
		(p.IncludeServerSideToolInvocations != nil && *p.IncludeServerSideToolInvocations)
}

// usesUnsupportedParam reports a parameter that changes behaviour and is neither replayed nor
// harmless to drop. Parameters with no effect on behaviour (store, user, metadata and the like) pass.
func usesUnsupportedParam(p *schemas.ChatParameters) bool {
	return p.Audio != nil ||
		p.LogitBias != nil ||
		(p.LogProbs != nil && *p.LogProbs) ||
		len(p.Modalities) > 0 ||
		(p.N != nil && *p.N > 1) ||
		p.Prediction != nil ||
		p.TopLogProbs != nil ||
		p.Speed != nil ||
		p.InferenceGeo != nil ||
		p.TaskBudget != nil ||
		len(p.ContextManagement) > 0
}

// replayParams copies the allow-listed fields. Nothing else survives.
func replayParams(p *schemas.ChatParameters) ReplayParams {
	if p == nil {
		return ReplayParams{}
	}
	return ReplayParams{
		Temperature:         p.Temperature,
		TopP:                p.TopP,
		TopK:                p.TopK,
		FrequencyPenalty:    p.FrequencyPenalty,
		PresencePenalty:     p.PresencePenalty,
		Reasoning:           p.Reasoning,
		Verbosity:           p.Verbosity,
		MaxCompletionTokens: p.MaxCompletionTokens,
		Stop:                p.Stop,
		Seed:                p.Seed,
		ResponseFormat:      p.ResponseFormat,
		ToolChoice:          p.ToolChoice,
		ParallelToolCalls:   p.ParallelToolCalls,
	}
}
