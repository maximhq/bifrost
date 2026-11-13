package handlers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// The probe asks one fixed predicate about a short fixed input: the smallest
// request every decision provider accepts, so a failure points at the route
// rather than at the request.
const (
	decisionProbeInput        = "The sky is blue."
	decisionProbeQuestionName = "probe"
	decisionProbeInstructions = "Does the input describe the sky?"
)

// decisionProbeMinSecretLength is the shortest credential scrubbed from probe
// errors by value. Real keys are far longer; a shorter value would mangle the
// message without hiding anything worth hiding.
const decisionProbeMinSecretLength = 8

// decisionProbeURLPattern finds URLs in an error message so their userinfo and
// query values can be masked.
var decisionProbeURLPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)

// decisionProbeRequest is the body of POST /api/providers/{provider}/decisions/probe.
type decisionProbeRequest struct {
	Model string `json:"model"`
	KeyID string `json:"key_id,omitempty"` // optional: probe through this key instead of the load-balanced pool
}

// DecisionProbeResponse reports one probe of a decision route. A probe that
// reached the provider is answered with 200 whether or not the route works;
// OK and Error tell the two apart.
type DecisionProbeResponse struct {
	OK        bool                     `json:"ok"`
	Provider  schemas.ModelProvider    `json:"provider"` // provider that answered, or was attempted
	Model     string                   `json:"model"`    // model that answered as the provider reported it, or the model attempted
	LatencyMS int64                    `json:"latency_ms"`
	Usage     *schemas.BifrostLLMUsage `json:"usage,omitempty"`
	Error     *DecisionProbeError      `json:"error,omitempty"`
}

// DecisionProbeError is the failure of a probe, with credentials removed.
type DecisionProbeError struct {
	StatusCode int     `json:"status_code"`
	Type       *string `json:"type,omitempty"`
	Code       *string `json:"code,omitempty"`
	Message    string  `json:"message"`
}

// probeDecision handles POST /api/providers/{provider}/decisions/probe. It
// sends one fixed decision request to exactly the given provider and model so
// an operator can check a route before relying on it. The request skips the
// plugin pipeline (routing rules, governance, caching, logging) and carries no
// fallbacks, so the answer, or the error, comes from that route alone.
func (h *ProviderHandler) probeDecision(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}
	config, err := h.inMemoryStore.GetProviderConfigRaw(provider)
	if err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider config: %v", err))
		return
	}

	var payload decisionProbeRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	model := strings.TrimSpace(payload.Model)
	if model == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model is required")
		return
	}
	if !h.supportsDecisions(provider, model) {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("model %q is not served on the decisions endpoint of provider %q", model, provider))
		return
	}
	keyID := strings.TrimSpace(payload.KeyID)
	if keyID != "" && !slices.ContainsFunc(config.Keys, func(key schemas.Key) bool { return key.ID == keyID }) {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("key %q is not configured for provider %q", keyID, provider))
		return
	}

	// A fresh context, so nothing the management request carried (a virtual key,
	// key pins, headers) reaches the probe; the provider's own request timeout
	// bounds it.
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer bifrostCtx.Cancel()
	bifrost.PrepareContextForInternalRequest(bifrostCtx)
	// One upstream call: a retry would bill again and replace the error being probed.
	bifrostCtx.SetValue(schemas.BifrostContextKeyDisableRetries, true)
	if keyID != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyAPIKeyID, keyID)
	}

	start := time.Now()
	resp, bifrostErr := h.client.DecisionRequest(bifrostCtx, &schemas.BifrostDecisionRequest{
		Provider: provider,
		Model:    model,
		Input:    schemas.DecisionInput{Text: schemas.Ptr(decisionProbeInput)},
		Questions: []schemas.DecisionQuestion{{
			Type:         schemas.DecisionTypePredicate,
			Name:         schemas.Ptr(decisionProbeQuestionName),
			Instructions: schemas.NewDecisionText(decisionProbeInstructions),
		}},
	})
	result := DecisionProbeResponse{Provider: provider, Model: model, LatencyMS: time.Since(start).Milliseconds()}

	if bifrostErr != nil {
		result.Provider, result.Model = probedRoute(result.Provider, result.Model, bifrostErr.ExtraFields.RoutingInfo, "")
		result.Error = decisionProbeErrorFrom(bifrostErr, decisionProbeSecrets(config))
		SendJSON(ctx, result)
		return
	}
	if resp == nil || len(resp.Answers) == 0 {
		result.Error = &DecisionProbeError{StatusCode: fasthttp.StatusBadGateway, Message: "the provider returned no answer"}
		SendJSON(ctx, result)
		return
	}
	result.OK = true
	result.Provider, result.Model = probedRoute(result.Provider, result.Model, resp.ExtraFields.RoutingInfo, resp.Model)
	result.Usage = resp.Usage
	SendJSON(ctx, result)
}

// probedRoute names the provider and model that served the probe: the
// provider core routed to, and the model the provider reported, else the
// model core sent it (an alias's target), else the model requested.
func probedRoute(provider schemas.ModelProvider, model string, routing schemas.RoutingInfo, reportedModel string) (schemas.ModelProvider, string) {
	if routing.Provider != "" {
		provider = routing.Provider
	}
	switch {
	case reportedModel != "":
		model = reportedModel
	case routing.Model != "":
		model = routing.Model
	}
	return provider, model
}

// decisionProbeErrorFrom renders a failed probe for the caller. It applies the
// client sanitizer every error response gets, then, in every returned string
// (type, code, and message), removes the provider's credentials by value and
// masks userinfo and query values in any URL, since an upstream error can echo
// the key or the URL it was sent to.
func decisionProbeErrorFrom(bifrostErr *schemas.BifrostError, secrets []string) *DecisionProbeError {
	sanitized := lib.SanitizeBifrostErrorForClient(bifrostErr)
	scrub := func(text string) string {
		for _, secret := range secrets {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
		return decisionProbeURLPattern.ReplaceAllStringFunc(text, lib.RedactURLCredentials)
	}
	scrubPtr := func(text *string) *string {
		if text == nil {
			return nil
		}
		return schemas.Ptr(scrub(*text))
	}
	out := &DecisionProbeError{StatusCode: sanitized.EffectiveHTTPStatus(), Type: scrubPtr(sanitized.Type)}
	if field := sanitized.Error; field != nil {
		if field.Type != nil {
			out.Type = scrubPtr(field.Type)
		}
		out.Code = scrubPtr(field.Code)
		out.Message = field.Message
		if out.Message == "" && field.Error != nil {
			out.Message = field.Error.Error()
		}
	}
	out.Message = scrub(out.Message)
	return out
}

// decisionProbeSecrets lists the provider's credential values long enough to
// scrub by value: its key values and its extra header values.
func decisionProbeSecrets(config *configstore.ProviderConfig) []string {
	var secrets []string
	add := func(value string) {
		if len(value) >= decisionProbeMinSecretLength {
			secrets = append(secrets, value)
		}
	}
	for i := range config.Keys {
		add(config.Keys[i].Value.GetValue())
	}
	if config.NetworkConfig != nil {
		for _, header := range config.NetworkConfig.ExtraHeaders {
			add(header.GetValue())
		}
	}
	return secrets
}
