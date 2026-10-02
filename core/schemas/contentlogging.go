package schemas

// Content-logging layers. Each admin-configured level that can turn content logging on or off for a
// request stamps its own decision under its own key: true turns content off, false turns it on, and a
// layer that inherits leaves its key absent. The virtual key's layer is
// BifrostContextKeyGovernanceDisableContentLogging.
//
// Set by governance (org and credential layers) and by core (the provider key) - DO NOT SET THESE
// MANUALLY; write them only through StampContentLoggingDecision, which merges repeated stamps and
// keeps the root-span mark in step.
const (
	BifrostContextKeyBusinessUnitDisableContentLogging  BifrostContextKey = "bifrost-business-unit-disable-content-logging"
	BifrostContextKeyTeamDisableContentLogging          BifrostContextKey = "bifrost-team-disable-content-logging"
	BifrostContextKeyUserDisableContentLogging          BifrostContextKey = "bifrost-user-disable-content-logging"
	BifrostContextKeyProviderKeyDisableContentLogging   BifrostContextKey = "bifrost-provider-key-disable-content-logging"
	BifrostContextKeyAccessProfileDisableContentLogging BifrostContextKey = "bifrost-access-profile-disable-content-logging"

	// BifrostContextKeyCallerContentLoggingResolved (bool) is set once governance has finished
	// stamping every caller-side layer, so "not stamped yet" can be told apart from "stamped, and
	// every layer inherits".
	BifrostContextKeyCallerContentLoggingResolved BifrostContextKey = "bifrost-caller-content-logging-resolved"
)

// contentLoggingTiers orders the layers from the highest priority down. The org hierarchy outranks
// the credential the request came in with: a business unit or team that turns content off cannot be
// reopened by a key that turns it on. Layers sharing a tier are peers, and inside a tier any "off"
// wins.
var contentLoggingTiers = [][]BifrostContextKey{
	{BifrostContextKeyBusinessUnitDisableContentLogging},
	{BifrostContextKeyTeamDisableContentLogging},
	{BifrostContextKeyUserDisableContentLogging},
	{
		BifrostContextKeyProviderKeyDisableContentLogging,
		BifrostContextKeyGovernanceDisableContentLogging,
		BifrostContextKeyAccessProfileDisableContentLogging,
	},
}

func isContentLoggingLayer(key BifrostContextKey) bool {
	for _, tier := range contentLoggingTiers {
		for _, layer := range tier {
			if layer == key {
				return true
			}
		}
	}
	return false
}

// StampContentLoggingDecision records one layer's decision for the request. A layer stamped more than
// once keeps "off" once it has it, which is how a user in several business units, a user holding
// several access profiles, and a request retried onto another provider key all resolve. Keys that are
// not content-logging layers are ignored.
//
// It also keeps AttrBifrostContentLoggingDisabled on the root span in step with the resolved
// decision, because observability connectors are handed the finished trace without the request
// context. The attribute is written true when the layers resolve to off, and written false only to
// undo an earlier true, so a request whose layers never resolve to off leaves the span untouched.
func StampContentLoggingDecision(ctx *BifrostContext, layer BifrostContextKey, disabled bool) {
	if ctx == nil || !isContentLoggingLayer(layer) {
		return
	}
	wasDisabled, _ := ResolveAdminContentLogging(ctx)
	if existing, _ := ctx.Value(layer).(bool); existing {
		disabled = true
	}
	ctx.SetValue(layer, disabled)
	nowDisabled, _ := ResolveAdminContentLogging(ctx)
	if nowDisabled != wasDisabled {
		ctx.SetTraceAttribute(AttrBifrostContentLoggingDisabled, nowDisabled)
	}
}

// ResolveAdminContentLogging walks the tiers from the top and returns the first tier's decision:
// disabled when any layer in it says off, enabled when its layers only say on. decided is false when
// every layer inherits, and the caller falls back to client.disable_content_logging.
func ResolveAdminContentLogging(ctx *BifrostContext) (disabled bool, decided bool) {
	if ctx == nil {
		return false, false
	}
	for _, tier := range contentLoggingTiers {
		tierDecided := false
		for _, layer := range tier {
			value, ok := ctx.Value(layer).(bool)
			if !ok {
				continue
			}
			if value {
				return true, true
			}
			tierDecided = true
		}
		if tierDecided {
			return false, true
		}
	}
	return false, false
}

// MarkCallerContentLoggingResolved records that governance has stamped every caller-side layer it is
// going to stamp for this request.
func MarkCallerContentLoggingResolved(ctx *BifrostContext) {
	if ctx == nil {
		return
	}
	ctx.SetValue(BifrostContextKeyCallerContentLoggingResolved, true)
}

// CallerContentLoggingResolved reports whether governance has finished stamping the caller-side
// layers for this request.
func CallerContentLoggingResolved(ctx *BifrostContext) bool {
	if ctx == nil {
		return false
	}
	resolved, _ := ctx.Value(BifrostContextKeyCallerContentLoggingResolved).(bool)
	return resolved
}
