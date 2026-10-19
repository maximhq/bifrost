package schemas

// Routing classification metadata lifecycle
//
// BifrostRoutingMetadata is the request-scoped accounting handoff for internal
// calls made by the routing plugin: a semantic classification embed, an llm
// classifier chat completion, a decision-model call, or a semantic embed followed by
// one configured classifier fallback. It is not general routing-decision
// metadata such as the selected tier, rule, provider, or model.
//
// Classification runs once in PreRequestHook, before provider execution, while
// PostLLMHook runs for every retry and configured fallback. The routing plugin
// therefore stores an owned snapshot on BifrostContext and only the primary
// provider's first physical attempt may claim it. Successful initial attempts
// also copy that snapshot to the response's routing metadata compatibility field so normal
// CalculateCost processing can include it.
//
// The context snapshot is necessary because a provider may return only an
// error. Governance and logging use it for error-path accounting when
// CountTowardBudgets is enabled. When a response exists, dedicated routing
// telemetry records every internal classifier call regardless of that flag.
// Routing metadata intentionally does not belong on BifrostErrorExtraFields:
// request-scoped sidecar state stays on the context, matching cache and
// guardrail metadata handling.

// Clone returns an owned snapshot of routing-classification metadata.
func (d *BifrostRoutingMetadata) Clone() *BifrostRoutingMetadata {
	if d == nil || len(d.Calls) == 0 {
		return nil
	}
	clone := &BifrostRoutingMetadata{
		Calls: make([]BifrostRoutingCall, len(d.Calls)),
	}
	for index, call := range d.Calls {
		clone.Calls[index] = call
		clone.Calls[index].ProviderUsed = cloneString(call.ProviderUsed)
		clone.Calls[index].ModelUsed = cloneString(call.ModelUsed)
		clone.Calls[index].InputTokens = cloneInt(call.InputTokens)
		clone.Calls[index].OutputTokens = cloneInt(call.OutputTokens)
	}
	return clone
}

// RoutingMetadataFromContext returns routing-classification metadata stored on ctx.
func RoutingMetadataFromContext(ctx *BifrostContext) (*BifrostRoutingMetadata, bool) {
	if ctx == nil {
		return nil, false
	}
	metadata, ok := ctx.Value(BifrostContextKeyRoutingMetadata).(*BifrostRoutingMetadata)
	if !ok || metadata == nil || len(metadata.Calls) == 0 {
		return nil, false
	}
	return metadata.Clone(), true
}

// InitialAttemptRoutingMetadataFromContext returns routing-classification metadata
// only for the primary provider's first physical attempt. Classification runs
// once in PreRequestHook, while PostLLMHook runs for every retry and fallback;
// this gate makes the initial attempt the single owner of that sidecar call.
func InitialAttemptRoutingMetadataFromContext(ctx *BifrostContext) (*BifrostRoutingMetadata, bool) {
	if ctx == nil {
		return nil, false
	}
	if fallbackIndex, _ := ctx.Value(BifrostContextKeyFallbackIndex).(int); fallbackIndex != 0 {
		return nil, false
	}
	if retryNumber, _ := ctx.Value(BifrostContextKeyNumberOfRetries).(int); retryNumber != 0 {
		return nil, false
	}
	return RoutingMetadataFromContext(ctx)
}

// AppendRoutingCallOnContext appends one billable routing-classification call
// to ctx. A request may append a semantic embed and one configured classifier
// fallback, so this adds each call rather than replacing the first and losing
// its usage.
func AppendRoutingCallOnContext(ctx *BifrostContext, call BifrostRoutingCall) bool {
	if ctx == nil || !validRoutingCall(call) {
		return false
	}
	current, _ := RoutingMetadataFromContext(ctx)
	if current == nil {
		current = &BifrostRoutingMetadata{}
	}
	current.Calls = append(current.Calls, call)
	ctx.SetValue(BifrostContextKeyRoutingMetadata, current.Clone())
	return true
}

// validRoutingCall rejects missing or negative usage before request accounting.
func validRoutingCall(call BifrostRoutingCall) bool {
	return call.ProviderUsed != nil && call.ModelUsed != nil && call.InputTokens != nil &&
		*call.InputTokens >= 0 && (call.OutputTokens == nil || *call.OutputTokens >= 0)
}
