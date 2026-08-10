package schemas

// BifrostGuardrailDebug carries request-scoped guardrail execution metadata.
type BifrostGuardrailDebug struct {
	JudgeCalls []BifrostGuardrailJudgeCall `json:"judge_calls,omitempty"`
}

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

// Clone returns an owned snapshot of the guardrail debug data.
func (d *BifrostGuardrailDebug) Clone() *BifrostGuardrailDebug {
	if d == nil || len(d.JudgeCalls) == 0 {
		return nil
	}
	clone := &BifrostGuardrailDebug{
		JudgeCalls: make([]BifrostGuardrailJudgeCall, len(d.JudgeCalls)),
	}
	for index, call := range d.JudgeCalls {
		clone.JudgeCalls[index] = call
		clone.JudgeCalls[index].RuleID = cloneUint(call.RuleID)
		clone.JudgeCalls[index].PromptTokensDetails = cloneChatPromptTokensDetails(call.PromptTokensDetails)
		clone.JudgeCalls[index].CompletionTokensDetails = cloneChatCompletionTokensDetails(call.CompletionTokensDetails)
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

// GuardrailDebugFromContext returns typed guardrail debug data stored on ctx.
func GuardrailDebugFromContext(ctx *BifrostContext) (*BifrostGuardrailDebug, bool) {
	if ctx == nil {
		return nil, false
	}
	debug, ok := ctx.Value(BifrostContextKeyGuardrailDebug).(*BifrostGuardrailDebug)
	if !ok || debug == nil || len(debug.JudgeCalls) == 0 {
		return nil, false
	}
	return debug.Clone(), true
}

// SetGuardrailDebugOnContext stores non-empty guardrail debug data on ctx.
func SetGuardrailDebugOnContext(ctx *BifrostContext, debug *BifrostGuardrailDebug) bool {
	if ctx == nil || debug == nil || len(debug.JudgeCalls) == 0 {
		return false
	}
	ctx.SetValue(BifrostContextKeyGuardrailDebug, debug.Clone())
	return true
}

// AppendGuardrailJudgeCallOnContext appends one guardrail judge call to ctx.
func AppendGuardrailJudgeCallOnContext(ctx *BifrostContext, call BifrostGuardrailJudgeCall) bool {
	if ctx == nil || call.TotalTokens == 0 && call.PromptTokens == 0 && call.CompletionTokens == 0 {
		return false
	}
	current, _ := GuardrailDebugFromContext(ctx)
	if current == nil {
		current = &BifrostGuardrailDebug{}
	}
	current.JudgeCalls = append(current.JudgeCalls, call)
	return SetGuardrailDebugOnContext(ctx, current)
}
