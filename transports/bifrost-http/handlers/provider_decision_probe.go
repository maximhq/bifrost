package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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
// errors wherever it appears. A shorter credential is scrubbed only where it
// stands as a whole token, so a value such as "abc1" hides the echoed key
// without cutting the same letters out of other words in the message.
const decisionProbeMinSecretLength = 8

// decisionProbeURLPattern finds URLs in an error message so their userinfo and
// query values can be masked. A URL runs to whitespace, a double quote, or an
// angle bracket; an apostrophe is a valid URL character (RFC 3986 sub-delims),
// so it does not end the match and leave the rest of the query unmasked.
var decisionProbeURLPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"<>]+`)

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
// plugin pipeline (routing rules, governance, caching, request logs; the HTTP
// access log still records the call) and carries no fallbacks, so the answer,
// or the error, comes from that route alone.
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
	case routing.ResolvedKeyAlias != nil && routing.ResolvedKeyAlias.ModelID != "":
		model = routing.ResolvedKeyAlias.ModelID
	case routing.Model != "":
		model = routing.Model
	}
	return provider, model
}

// decisionProbeErrorFrom renders a failed probe for the caller. It applies the
// client sanitizer every error response gets, then, in every returned string
// (type, code, and message), removes the provider's credentials by value and
// masks userinfo and query values in any URL, since an upstream error can echo
// the key or the URL it was sent to. Credentials are removed longest first: a
// shorter one that overlaps a longer one would otherwise cut it apart and leave
// the rest of the longer one in the text.
func decisionProbeErrorFrom(bifrostErr *schemas.BifrostError, secrets []string) *DecisionProbeError {
	sanitized := lib.SanitizeBifrostErrorForClient(bifrostErr)
	secrets = slices.Clone(secrets)
	slices.SortStableFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	scrub := func(text string) string {
		for _, secret := range secrets {
			if len(secret) >= decisionProbeMinSecretLength {
				text = strings.ReplaceAll(text, secret, "[redacted]")
			} else {
				text = redactWholeToken(text, secret)
			}
		}
		return decisionProbeURLPattern.ReplaceAllStringFunc(text, redactDecisionProbeURL)
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

// decisionProbeSecrets lists the provider's credential values to scrub: its
// key values and its extra header values. Empty values are skipped, since
// scrubbing "" would insert the marker between every character.
func decisionProbeSecrets(config *configstore.ProviderConfig) []string {
	var secrets []string
	add := func(value string) {
		if value != "" {
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

// redactDecisionProbeURL masks the userinfo and query values of a URL found in
// a probe error. A URL that parses is masked by lib.RedactURLCredentials. One
// that does not, which that helper returns unchanged, is masked by position, so
// a malformed URL cannot carry its credentials through: the userinfo ends at
// the first '@' followed by a well-formed host (see decisionProbeUserinfoEnd),
// and every query value is replaced. The host and path are kept.
func redactDecisionProbeURL(raw string) string {
	if _, err := url.Parse(raw); err == nil {
		return lib.RedactURLCredentials(raw)
	}
	scheme, rest, _ := strings.Cut(raw, "://")
	if at := decisionProbeUserinfoEnd(rest); at >= 0 {
		userinfo := "redacted"
		if strings.Contains(rest[:at], ":") {
			userinfo = "redacted:redacted"
		}
		rest = userinfo + rest[at:]
	}
	address, query, hasQuery := strings.Cut(rest, "?")
	if !hasQuery {
		return scheme + "://" + rest
	}
	query, fragment, hasFragment := strings.Cut(query, "#")
	pairs := strings.Split(query, "&")
	for i, pair := range pairs {
		if name, _, _ := strings.Cut(pair, "="); pair != "" {
			pairs[i] = name + "=redacted"
		}
	}
	out := scheme + "://" + address + "?" + strings.Join(pairs, "&")
	if hasFragment {
		out += "#" + fragment
	}
	return out
}

// decisionProbeHostPattern is a host with an optional port: a name or IPv4
// address, or a bracketed IPv6 address.
var decisionProbeHostPattern = regexp.MustCompile(`^(?:\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(?::[0-9]*)?$`)

// decisionProbeUserinfoEnd returns the index in rest (a URL after "://") of the
// '@' that ends its userinfo, or -1 when it has none. A password may itself
// hold '@' or '/', and a path may hold '@', so the '@' chosen is the first one
// whose text up to the next '/', '?', '#', or the end is a well-formed host.
// When no '@' qualifies, the last '@' before the query or fragment is used, so
// a userinfo is masked even if the host after it is malformed too.
func decisionProbeUserinfoEnd(rest string) int {
	beforeQuery := rest
	if end := strings.IndexAny(rest, "?#"); end >= 0 {
		beforeQuery = rest[:end]
	}
	for i := range len(beforeQuery) {
		if beforeQuery[i] != '@' {
			continue
		}
		host := beforeQuery[i+1:]
		if end := strings.IndexByte(host, '/'); end >= 0 {
			host = host[:end]
		}
		if decisionProbeHostPattern.MatchString(host) {
			return i
		}
	}
	return strings.LastIndexByte(beforeQuery, '@')
}

// redactWholeToken replaces each occurrence of token in text that is not part
// of a longer token, that is, not preceded or followed by a letter, digit,
// '-' or '_'.
func redactWholeToken(text, token string) string {
	var out strings.Builder
	last, from := 0, 0
	for {
		i := strings.Index(text[from:], token)
		if i < 0 {
			break
		}
		i += from
		end := i + len(token)
		if (i == 0 || !isDecisionProbeTokenByte(text[i-1])) && (end == len(text) || !isDecisionProbeTokenByte(text[end])) {
			out.WriteString(text[last:i])
			out.WriteString("[redacted]")
			last, from = end, end
		} else {
			from = i + 1
		}
	}
	if last == 0 {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}

func isDecisionProbeTokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}
