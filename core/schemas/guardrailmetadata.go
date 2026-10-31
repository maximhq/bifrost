package schemas

import (
	"slices"
	"sync"
)

// BifrostGuardrailMetadata carries request-scoped guardrail execution metadata.
type BifrostGuardrailMetadata struct {
	JudgeCalls []BifrostGuardrailJudgeCall `json:"judge_calls,omitempty"`
	Detections []BifrostGuardrailDetection `json:"detections,omitempty"`
	// Interventions records rules that blocked the request, so a blocked log row
	// names the rule that stopped it instead of only carrying an error message.
	Interventions []BifrostGuardrailIntervention `json:"interventions,omitempty"`
}

// BifrostGuardrailIntervention records one guardrail policy match that blocked
// the request (input, output, or MCP tool call).
type BifrostGuardrailIntervention struct {
	Phase             string   `json:"phase,omitempty"`
	RuleID            *uint    `json:"rule_id,omitempty"`
	RuleName          string   `json:"rule_name,omitempty"`
	GuardrailName     string   `json:"guardrail_name,omitempty"`
	GuardrailProvider string   `json:"guardrail_provider,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	Assessments       []string `json:"assessments,omitempty"` // Sanitized policy categories; never matched text.
}

// BifrostGuardrailDetection records one guardrail policy match whose configured
// action was detect-only, so the request continued unmodified.
type BifrostGuardrailDetection struct {
	Phase             string   `json:"phase,omitempty"`
	RuleID            *uint    `json:"rule_id,omitempty"`
	RuleName          string   `json:"rule_name,omitempty"`
	GuardrailName     string   `json:"guardrail_name,omitempty"`
	GuardrailProvider string   `json:"guardrail_provider,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	Assessments       []string `json:"assessments,omitempty"` // Sanitized policy categories; never matched text.
	Action            string   `json:"action,omitempty"`      // What the guardrail did, or would do when not enforced (GuardrailDetectionAction*).
	Enforced          *bool    `json:"enforced,omitempty"`    // False when an output-monitor rule recorded the match without acting on it.
}

// Guardrail detection actions: what a guardrail did, or would have done when
// its rule only monitors, with the content it matched.
const (
	GuardrailDetectionActionDetect    = "detect"
	GuardrailDetectionActionBlock     = "block"
	GuardrailDetectionActionRedact    = "redact"
	GuardrailDetectionActionTransform = "transform"
)

// BifrostGuardrailJudgeCall records one billable guardrail judge invocation.
type BifrostGuardrailJudgeCall struct {
	Phase                   string                       `json:"phase,omitempty"`
	RuleID                  *uint                        `json:"rule_id,omitempty"`
	RuleName                string                       `json:"rule_name,omitempty"`
	GuardrailName           string                       `json:"guardrail_name,omitempty"`
	GuardrailProvider       string                       `json:"guardrail_provider,omitempty"`
	Action                  string                       `json:"action,omitempty"`
	Reason                  string                       `json:"reason,omitempty"`
	JudgeProvider           ModelProvider                `json:"judge_provider,omitempty"`
	JudgeModel              string                       `json:"judge_model,omitempty"`
	JudgeRequestType        RequestType                  `json:"judge_request_type,omitempty"`
	PromptTokens            int                          `json:"prompt_tokens,omitempty"`
	PromptTokensDetails     *ChatPromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokens        int                          `json:"completion_tokens,omitempty"`
	CompletionTokensDetails *ChatCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	TotalTokens             int                          `json:"total_tokens,omitempty"`
}

// isEmpty reports whether the metadata carries nothing worth storing or emitting.
func (d *BifrostGuardrailMetadata) isEmpty() bool {
	return d == nil || len(d.JudgeCalls) == 0 && len(d.Detections) == 0 && len(d.Interventions) == 0
}

// Clone returns an owned snapshot of the guardrail metadata.
func (d *BifrostGuardrailMetadata) Clone() *BifrostGuardrailMetadata {
	if d.isEmpty() {
		return nil
	}
	clone := &BifrostGuardrailMetadata{}
	if len(d.JudgeCalls) > 0 {
		clone.JudgeCalls = make([]BifrostGuardrailJudgeCall, len(d.JudgeCalls))
		for index, call := range d.JudgeCalls {
			clone.JudgeCalls[index] = call
			clone.JudgeCalls[index].RuleID = cloneUint(call.RuleID)
			clone.JudgeCalls[index].PromptTokensDetails = cloneChatPromptTokensDetails(call.PromptTokensDetails)
			clone.JudgeCalls[index].CompletionTokensDetails = cloneChatCompletionTokensDetails(call.CompletionTokensDetails)
		}
	}
	if len(d.Detections) > 0 {
		clone.Detections = make([]BifrostGuardrailDetection, len(d.Detections))
		for index, detection := range d.Detections {
			clone.Detections[index] = detection
			clone.Detections[index].RuleID = cloneUint(detection.RuleID)
			clone.Detections[index].Assessments = append([]string(nil), detection.Assessments...)
			if detection.Enforced != nil {
				clone.Detections[index].Enforced = Ptr(*detection.Enforced)
			}
		}
	}
	if len(d.Interventions) > 0 {
		clone.Interventions = make([]BifrostGuardrailIntervention, len(d.Interventions))
		for index, intervention := range d.Interventions {
			clone.Interventions[index] = intervention
			clone.Interventions[index].RuleID = cloneUint(intervention.RuleID)
			clone.Interventions[index].Assessments = append([]string(nil), intervention.Assessments...)
		}
	}
	return clone
}

// cloneChatPromptTokensDetails returns an owned copy of nested prompt token details.
func cloneChatPromptTokensDetails(details *ChatPromptTokensDetails) *ChatPromptTokensDetails {
	if details == nil {
		return nil
	}
	clone := *details
	if details.CachedWriteTokenDetails != nil {
		cachedWriteClone := *details.CachedWriteTokenDetails
		clone.CachedWriteTokenDetails = &cachedWriteClone
	}
	return &clone
}

// cloneChatCompletionTokensDetails returns an owned copy of completion token details.
func cloneChatCompletionTokensDetails(details *ChatCompletionTokensDetails) *ChatCompletionTokensDetails {
	if details == nil {
		return nil
	}
	clone := *details
	clone.CitationTokens = cloneInt(details.CitationTokens)
	clone.NumSearchQueries = cloneInt(details.NumSearchQueries)
	clone.ImageTokens = cloneInt(details.ImageTokens)
	return &clone
}

// cloneUint returns an owned copy of value.
func cloneUint(value *uint) *uint {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// GuardrailMetadataFromContext returns typed guardrail metadata stored on ctx.
func GuardrailMetadataFromContext(ctx *BifrostContext) (*BifrostGuardrailMetadata, bool) {
	if ctx == nil {
		return nil, false
	}
	metadata, ok := ctx.Value(BifrostContextKeyGuardrailMetadata).(*BifrostGuardrailMetadata)
	if !ok || metadata.isEmpty() {
		return nil, false
	}
	return metadata.Clone(), true
}

// SetGuardrailMetadataOnContext stores non-empty guardrail metadata on ctx.
func SetGuardrailMetadataOnContext(ctx *BifrostContext, metadata *BifrostGuardrailMetadata) bool {
	if ctx == nil || metadata.isEmpty() {
		return false
	}
	ctx.SetValue(BifrostContextKeyGuardrailMetadata, metadata.Clone())
	return true
}

// AppendGuardrailJudgeCallOnContext appends one guardrail judge call to ctx.
func AppendGuardrailJudgeCallOnContext(ctx *BifrostContext, call BifrostGuardrailJudgeCall) bool {
	if ctx == nil || call.TotalTokens == 0 && call.PromptTokens == 0 && call.CompletionTokens == 0 {
		return false
	}
	return appendGuardrailMetadataOnContext(ctx, func(metadata *BifrostGuardrailMetadata) {
		metadata.JudgeCalls = append(metadata.JudgeCalls, call)
	})
}

// appendGuardrailMetadataOnContext applies appendTo to an owned copy of ctx's guardrail metadata
// and stores it in one locked step, so concurrent appends of any kind never lose an entry.
func appendGuardrailMetadataOnContext(ctx *BifrostContext, appendTo func(*BifrostGuardrailMetadata)) bool {
	ctx.updateValue(BifrostContextKeyGuardrailMetadata, func(current any) any {
		existing, _ := current.(*BifrostGuardrailMetadata)
		next := existing.Clone()
		if next == nil {
			next = &BifrostGuardrailMetadata{}
		}
		appendTo(next)
		return next
	})
	return true
}

// AppendGuardrailDetectionOnContext appends one detect-only guardrail match to ctx.
func AppendGuardrailDetectionOnContext(ctx *BifrostContext, detection BifrostGuardrailDetection) bool {
	if ctx == nil {
		return false
	}
	return appendGuardrailMetadataOnContext(ctx, func(metadata *BifrostGuardrailMetadata) {
		metadata.Detections = append(metadata.Detections, detection)
	})
}

// DeferredGuardrailResult carries guardrail results that finish after the
// response was delivered (output-monitor rules) to the log writer, which holds
// the request's row until it completes. The result is the request's complete
// guardrail state, input and output, so the writer stores it in place of what
// the row was built with, plus the plugin log lines the evaluation wrote, which
// the writer appends to the row's: the request's own plugin logs were already
// drained to its trace when the response completed. It completes exactly once.
type DeferredGuardrailResult struct {
	mu         sync.Mutex
	completed  bool
	metadata   *BifrostGuardrailMetadata
	redaction  RedactionData
	pluginLogs []PluginLogEntry
	onComplete []func(*BifrostGuardrailMetadata, RedactionData, []PluginLogEntry)
}

// NewDeferredGuardrailResult returns a pending deferred guardrail result.
func NewDeferredGuardrailResult() *DeferredGuardrailResult {
	return &DeferredGuardrailResult{}
}

// Complete publishes the request's final guardrail metadata and redaction data
// and the evaluation's plugin log lines, and runs the registered callbacks on
// the caller's goroutine. Only the first call takes effect, so every exit path
// of the evaluation can call it.
func (r *DeferredGuardrailResult) Complete(metadata *BifrostGuardrailMetadata, redaction RedactionData, pluginLogs []PluginLogEntry) {
	r.mu.Lock()
	if r.completed {
		r.mu.Unlock()
		return
	}
	r.completed = true
	r.metadata = metadata.Clone()
	r.redaction = redaction.Clone()
	r.pluginLogs = slices.Clone(pluginLogs)
	callbacks := r.onComplete
	r.onComplete = nil
	r.mu.Unlock()
	for _, fn := range callbacks {
		fn(r.metadata.Clone(), r.redaction.Clone(), slices.Clone(r.pluginLogs))
	}
}

// OnComplete runs fn with owned copies of the result once it completes, right
// away on the caller's goroutine when it already has. Callbacks run on the
// completing goroutine, so they must not block.
func (r *DeferredGuardrailResult) OnComplete(fn func(metadata *BifrostGuardrailMetadata, redaction RedactionData, pluginLogs []PluginLogEntry)) {
	r.mu.Lock()
	if !r.completed {
		r.onComplete = append(r.onComplete, fn)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	fn(r.metadata.Clone(), r.redaction.Clone(), slices.Clone(r.pluginLogs))
}

// DeferredGuardrailResultFromContext returns the request's pending deferred guardrail result, if any.
func DeferredGuardrailResultFromContext(ctx *BifrostContext) (*DeferredGuardrailResult, bool) {
	if ctx == nil {
		return nil, false
	}
	result, ok := ctx.Value(BifrostContextKeyDeferredGuardrail).(*DeferredGuardrailResult)
	return result, ok && result != nil
}

// AppendGuardrailInterventionOnContext appends one blocking guardrail match to ctx.
func AppendGuardrailInterventionOnContext(ctx *BifrostContext, intervention BifrostGuardrailIntervention) bool {
	if ctx == nil {
		return false
	}
	return appendGuardrailMetadataOnContext(ctx, func(metadata *BifrostGuardrailMetadata) {
		metadata.Interventions = append(metadata.Interventions, intervention)
	})
}

// Deprecated: use BifrostGuardrailMetadata.
type BifrostGuardrailDebug = BifrostGuardrailMetadata

// Deprecated: use GuardrailMetadataFromContext.
func GuardrailDebugFromContext(ctx *BifrostContext) (*BifrostGuardrailMetadata, bool) {
	return GuardrailMetadataFromContext(ctx)
}

// Deprecated: use SetGuardrailMetadataOnContext.
func SetGuardrailDebugOnContext(ctx *BifrostContext, metadata *BifrostGuardrailMetadata) bool {
	return SetGuardrailMetadataOnContext(ctx, metadata)
}
