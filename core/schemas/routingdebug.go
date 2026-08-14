package schemas

// Routing classification debug lifecycle
//
// BifrostRoutingDebug is the request-scoped accounting handoff for internal
// calls made by the routing plugin: a semantic classification embed, an llm
// classifier chat completion, or both when semantic classification produces
// no tier and the llm fallback runs. It is not general routing-decision
// metadata such as the selected tier, rule, provider, or model.
//
// Classification runs once in PreRequestHook, before provider execution, while
// PostLLMHook runs for every retry and configured fallback. The routing plugin
// therefore stores an owned snapshot on BifrostContext and only the primary
// provider's first physical attempt may claim it. Successful initial attempts
// also copy that snapshot to BifrostResponseExtraFields.RoutingDebug so normal
// CalculateCost processing can include it.
//
// The context snapshot is necessary because a provider may return only an
// error. Governance and logging use it for error-path accounting when
// CountTowardBudgets is enabled. When a response exists, dedicated routing
// telemetry records every internal classifier call regardless of that flag.
// RoutingDebug intentionally does not belong on BifrostErrorExtraFields:
// request-scoped sidecar state stays on the context, matching cache and
// guardrail debug handling.

// Clone returns an owned snapshot of routing-classification debug data.
func (d *BifrostRoutingDebug) Clone() *BifrostRoutingDebug {
	if d == nil || len(d.Calls) == 0 {
		return nil
	}
	clone := &BifrostRoutingDebug{
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

// RoutingDebugFromContext returns routing-classification debug data stored on ctx.
func RoutingDebugFromContext(ctx *BifrostContext) (*BifrostRoutingDebug, bool) {
	if ctx == nil {
		return nil, false
	}
	debug, ok := ctx.Value(BifrostContextKeyRoutingDebug).(*BifrostRoutingDebug)
	if !ok || debug == nil || len(debug.Calls) == 0 {
		return nil, false
	}
	return debug.Clone(), true
}

// InitialAttemptRoutingDebugFromContext returns routing-classification debug
// only for the primary provider's first physical attempt. Classification runs
// once in PreRequestHook, while PostLLMHook runs for every retry and fallback;
// this gate makes the initial attempt the single owner of that sidecar call.
func InitialAttemptRoutingDebugFromContext(ctx *BifrostContext) (*BifrostRoutingDebug, bool) {
	if ctx == nil {
		return nil, false
	}
	if fallbackIndex, _ := ctx.Value(BifrostContextKeyFallbackIndex).(int); fallbackIndex != 0 {
		return nil, false
	}
	if retryNumber, _ := ctx.Value(BifrostContextKeyNumberOfRetries).(int); retryNumber != 0 {
		return nil, false
	}
	return RoutingDebugFromContext(ctx)
}

// AppendRoutingCallOnContext appends one billable routing-classification call
// to ctx. A request may append up to two calls — a semantic classification
// embed and, only when semantic classification produced no tier, an llm
// classifier completion — so a second call adds to the first rather than
// replacing it, unlike a single-slot overwrite that would silently drop
// whichever call wrote first.
func AppendRoutingCallOnContext(ctx *BifrostContext, call BifrostRoutingCall) bool {
	if ctx == nil || !validRoutingCall(call) {
		return false
	}
	current, _ := RoutingDebugFromContext(ctx)
	if current == nil {
		current = &BifrostRoutingDebug{}
	}
	current.Calls = append(current.Calls, call)
	ctx.SetValue(BifrostContextKeyRoutingDebug, current.Clone())
	return true
}

func validRoutingCall(call BifrostRoutingCall) bool {
	return call.ProviderUsed != nil && call.ModelUsed != nil && call.InputTokens != nil &&
		*call.InputTokens >= 0 && (call.OutputTokens == nil || *call.OutputTokens >= 0)
}
