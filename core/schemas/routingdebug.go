package schemas

// Routing classification debug lifecycle
//
// BifrostRoutingDebug is the request-scoped accounting handoff for internal
// calls made by the routing plugin. It currently records the semantic embedding
// call used for complexity classification and is the envelope for other
// complexity-classifier calls; it is not general routing-decision metadata such
// as the selected tier, rule, provider, or model.
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
// telemetry records the internal classifier call regardless of that flag.
// RoutingDebug intentionally does not belong on BifrostErrorExtraFields:
// request-scoped sidecar state stays on the context, matching cache and
// guardrail debug handling.

// Clone returns an owned snapshot of routing-classification debug data.
func (d *BifrostRoutingDebug) Clone() *BifrostRoutingDebug {
	if d == nil {
		return nil
	}
	clone := *d
	clone.ProviderUsed = cloneString(d.ProviderUsed)
	clone.ModelUsed = cloneString(d.ModelUsed)
	clone.InputTokens = cloneInt(d.InputTokens)
	return &clone
}

// RoutingDebugFromContext returns routing-classification debug data stored on ctx.
func RoutingDebugFromContext(ctx *BifrostContext) (*BifrostRoutingDebug, bool) {
	if ctx == nil {
		return nil, false
	}
	debug, ok := ctx.Value(BifrostContextKeyRoutingDebug).(*BifrostRoutingDebug)
	if !ok || !validRoutingDebug(debug) {
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

// SetRoutingDebugOnContext stores routing-classification debug data on ctx.
func SetRoutingDebugOnContext(ctx *BifrostContext, debug *BifrostRoutingDebug) bool {
	if ctx == nil || !validRoutingDebug(debug) {
		return false
	}
	ctx.SetValue(BifrostContextKeyRoutingDebug, debug.Clone())
	return true
}

func validRoutingDebug(debug *BifrostRoutingDebug) bool {
	return debug != nil && debug.ProviderUsed != nil && debug.ModelUsed != nil && debug.InputTokens != nil && *debug.InputTokens >= 0
}
