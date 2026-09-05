package schemas

// BifrostGuardrailMetadata carries request-scoped guardrail execution metadata.
type BifrostGuardrailMetadata struct {
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

// Clone returns an owned snapshot of the guardrail metadata.
func (d *BifrostGuardrailMetadata) Clone() *BifrostGuardrailMetadata {
	if d == nil || len(d.JudgeCalls) == 0 {
		return nil
	}
	clone := &BifrostGuardrailMetadata{
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

// GuardrailMetadataFromContext returns typed guardrail metadata stored on ctx.
func GuardrailMetadataFromContext(ctx *BifrostContext) (*BifrostGuardrailMetadata, bool) {
	if ctx == nil {
		return nil, false
	}
	metadata, ok := ctx.Value(BifrostContextKeyGuardrailMetadata).(*BifrostGuardrailMetadata)
	if !ok || metadata == nil || len(metadata.JudgeCalls) == 0 {
		return nil, false
	}
	return metadata.Clone(), true
}

// SetGuardrailMetadataOnContext stores non-empty guardrail metadata on ctx.
func SetGuardrailMetadataOnContext(ctx *BifrostContext, metadata *BifrostGuardrailMetadata) bool {
	if ctx == nil || metadata == nil || len(metadata.JudgeCalls) == 0 {
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
	current, _ := GuardrailMetadataFromContext(ctx)
	if current == nil {
		current = &BifrostGuardrailMetadata{}
	}
	current.JudgeCalls = append(current.JudgeCalls, call)
	return SetGuardrailMetadataOnContext(ctx, current)
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
