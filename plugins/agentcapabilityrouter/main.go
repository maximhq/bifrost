// Package agentcapabilityrouter classifies agent work by role and capability
// before Bifrost's routing plugin selects a provider and physical model.
package agentcapabilityrouter

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const routingEngineName = "capability-router"

// Plugin deterministically rewrites configured dynamic aliases to capability
// lanes. It does not select providers, keys, physical models, or fallbacks.
type Plugin struct {
	config resolvedConfig
}

var _ schemas.LLMPlugin = (*Plugin)(nil)

// Init validates configuration and returns an immutable plugin instance.
func Init(config *Config) (*Plugin, error) {
	resolved, err := resolveConfig(config)
	if err != nil {
		return nil, fmt.Errorf("invalid %s config: %w", PluginName, err)
	}
	return &Plugin{config: resolved}, nil
}

// GetName implements schemas.BasePlugin.
func (*Plugin) GetName() string { return PluginName }

// Cleanup implements schemas.BasePlugin. The plugin owns no external resources.
func (*Plugin) Cleanup() error { return nil }

// PreRequestHook classifies supported chat and Responses requests once per
// top-level request. Only configured automatic aliases are managed.
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if req == nil || (req.ChatRequest == nil && req.ResponsesRequest == nil) {
		return nil
	}

	_, requestedModel, _ := req.GetRequestFields()
	role, managed := p.roleForModel(requestedModel)
	if !managed || !p.config.ActiveRoles[role] {
		return nil
	}

	classification := classify(extractAgentSignals(req, p.config.HistoryMessages), p.config)
	capability := classification.Capability
	if classification.Confidence < p.config.ConfidenceThreshold {
		capability = CapabilityGeneral
	}
	lane := "agent-" + role + "-" + capability
	message := fmt.Sprintf(
		"requested=%s role=%s capability=%s confidence=%.2f lane=%s shadow=%t signals=%s",
		requestedModel,
		role,
		capability,
		classification.Confidence,
		lane,
		p.config.ShadowMode,
		strings.Join(classification.Signals, ","),
	)
	if ctx != nil {
		ctx.Log(schemas.LogLevelInfo, message)
		ctx.AppendRoutingEngineLog(routingEngineName, schemas.LogLevelInfo, message)
	}
	if p.config.ShadowMode {
		return nil
	}

	req.SetModel(lane)
	if ctx != nil {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, routingEngineName)
	}
	return nil
}

// PreLLMHook is a no-op. Routing decisions belong in PreRequestHook so they are
// stable across retries and fallbacks.
func (*Plugin) PreLLMHook(
	_ *schemas.BifrostContext,
	req *schemas.BifrostRequest,
) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook is a no-op because the plugin does not transform provider output.
func (*Plugin) PostLLMHook(
	_ *schemas.BifrostContext,
	resp *schemas.BifrostResponse,
	bifrostErr *schemas.BifrostError,
) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

func (p *Plugin) roleForModel(model string) (string, bool) {
	switch model {
	case p.config.Aliases.Main:
		return roleMain, true
	case p.config.Aliases.Worker:
		return roleWorker, true
	default:
		return "", false
	}
}
