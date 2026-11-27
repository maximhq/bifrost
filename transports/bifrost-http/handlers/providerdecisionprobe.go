package handlers

import (
	"context"
	"fmt"
	"math"
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
// errors by replacing just its occurrences. A shorter credential is scrubbed
// together with the whole token it occurs in, so a value such as "abc1" never
// survives inside a longer word, and the rest of that word does not show
// where it stood.
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
	// The probe goes through the running client, so it checks the provider and
	// keys the client serves: the live store's, not the config store's.
	stored, ok := h.config.LiveStore.Collections.Providers.Get(string(provider))
	if !ok {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", lib.ErrNotFound))
		return
	}
	config := &stored.Config
	var keys []schemas.Key
	if list, ok := h.config.LiveStore.Views.ProviderKeyLists.Get(string(provider)); ok {
		keys = *list
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
	// A probe pinned to a key sends that key's alias target; two keys can map
	// the same alias to different models, and the catalog keeps only one of them.
	keyID := strings.TrimSpace(payload.KeyID)
	var target string
	if keyID != "" {
		i := slices.IndexFunc(keys, func(key schemas.Key) bool { return key.ID == keyID })
		if i < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("key %q is not configured for provider %q", keyID, provider))
			return
		}
		target = keyAliasTarget(keys[i], model)
	} else {
		target = h.aliasTarget(provider, model)
	}
	if !h.supportsDecisionsAs(provider, model, target) {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("model %q is not served on the decisions endpoint of provider %q", model, provider))
		return
	}

	// A fresh context, so nothing the management request carried (a virtual key,
	// key pins, headers) reaches the probe. Its deadline is the provider's request
	// timeout and covers the wait for queue space as well as the upstream call.
	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Now().Add(decisionProbeTimeout(config)))
	defer bifrostCtx.Cancel()
	// One upstream call: a retry would bill again and replace the error being probed.
	bifrost.PrepareContextForSingleAttemptInternalRequest(bifrostCtx)
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
		result.Error = decisionProbeErrorFrom(bifrostErr, decisionProbeSecrets(config, keys))
		SendJSON(ctx, result)
		return
	}
	if resp == nil || len(resp.Answers) == 0 {
		result.Error = &DecisionProbeError{StatusCode: fasthttp.StatusBadGateway, Message: "the provider returned no answer"}
		SendJSON(ctx, result)
		return
	}
	result.Provider, result.Model = probedRoute(result.Provider, result.Model, resp.ExtraFields.RoutingInfo, resp.Model)
	result.Usage = resp.Usage
	if problem := decisionProbeAnswerProblem(resp.Answers); problem != "" {
		result.Error = &DecisionProbeError{StatusCode: fasthttp.StatusBadGateway, Message: problem}
		SendJSON(ctx, result)
		return
	}
	result.OK = true
	SendJSON(ctx, result)
}

// decisionProbeTimeout is the deadline of a whole probe: the provider's
// configured request timeout, or Bifrost's default when it sets none.
func decisionProbeTimeout(config *configstore.ProviderConfig) time.Duration {
	seconds := schemas.DefaultRequestTimeoutInSeconds
	if config.NetworkConfig != nil && config.NetworkConfig.DefaultRequestTimeoutInSeconds > 0 {
		seconds = config.NetworkConfig.DefaultRequestTimeoutInSeconds
	}
	return time.Duration(seconds) * time.Second
}

// keyAliasTarget returns the model key sends for model, in the order
// aliasTarget resolves it: the key's own alias's ModelName, then its ModelID,
// or model itself when the key defines no alias for it.
func keyAliasTarget(key schemas.Key, model string) string {
	if alias := key.Aliases.ResolveConfig(model); alias != nil {
		if alias.ModelName != nil && *alias.ModelName != "" {
			return *alias.ModelName
		}
		if alias.ModelID != "" {
			return alias.ModelID
		}
	}
	return model
}

// decisionProbeAnswerProbabilityTolerance is how far a probability
// distribution may sum from 1, the rounding the decision emulation accepts.
const decisionProbeAnswerProbabilityTolerance = 0.05

// decisionProbeAnswerProblem says why the answers to the probe's one predicate
// question could not be used, or returns "" when they can. A route only works
// if a caller could act on its answer: a refusal, an answer of another type, or
// a probability that is missing, outside 0..1, or part of a distribution that
// does not sum to about 1 is a failed probe even though the provider replied.
func decisionProbeAnswerProblem(answers []schemas.DecisionAnswer) string {
	if len(answers) != 1 {
		return fmt.Sprintf("the provider returned %d answers to one question", len(answers))
	}
	answer := answers[0]
	switch answer.Type {
	case schemas.DecisionTypePredicate:
	case schemas.DecisionTypeRefusal:
		return "the provider refused the probe question"
	default:
		return fmt.Sprintf("the provider returned a %q answer to a predicate question", answer.Type)
	}
	if answer.Probability == nil {
		return "the predicate answer has no probability"
	}
	if !decisionProbeValidProbability(*answer.Probability) {
		return fmt.Sprintf("the predicate answer's probability %v is outside 0..1", *answer.Probability)
	}
	if len(answer.Probabilities) > 0 {
		var sum float64
		for _, p := range answer.Probabilities {
			if !decisionProbeValidProbability(p.Probability) {
				return fmt.Sprintf("the answer's probability %v is outside 0..1", p.Probability)
			}
			sum += p.Probability
		}
		if math.Abs(sum-1) > decisionProbeAnswerProbabilityTolerance+1e-12 {
			return fmt.Sprintf("the answer's probabilities sum to %v, not about 1", sum)
		}
	}
	return ""
}

func decisionProbeValidProbability(p float64) bool {
	return !math.IsNaN(p) && p >= 0 && p <= 1
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
// the key or the URL it was sent to. URLs are masked first: a short credential
// replaced inside a URL's scheme (a header value "http" inside "https") would
// otherwise stop the URL from matching and leave its userinfo and query in the
// text. Credentials are removed longest first: a shorter one that overlaps a
// longer one would otherwise cut it apart and leave the rest of the longer one
// in the text.
func decisionProbeErrorFrom(bifrostErr *schemas.BifrostError, secrets []string) *DecisionProbeError {
	sanitized := lib.SanitizeBifrostErrorForClient(bifrostErr)
	secrets = slices.Clone(secrets)
	slices.SortStableFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	scrub := func(text string) string {
		text = decisionProbeURLPattern.ReplaceAllStringFunc(text, redactDecisionProbeURL)
		for _, secret := range secrets {
			if len(secret) >= decisionProbeMinSecretLength {
				text = strings.ReplaceAll(text, secret, "[redacted]")
			} else {
				text = redactContainingToken(text, secret)
			}
		}
		return text
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

// decisionProbeSecrets lists the provider's credential values to scrub: each
// key's value and the secrets its provider config authenticates with (client
// secrets, private keys, AWS access and secret keys and session tokens, Vertex
// service account credentials, an inline identity token, OAuth extra token
// parameters), plus the extra header values. Bearer tokens minted from these
// live in each provider's own token cache, which the handler cannot read, so
// they are not listed. Empty values are skipped, since scrubbing "" would
// insert the marker between every character.
func decisionProbeSecrets(config *configstore.ProviderConfig, keys []schemas.Key) []string {
	var secrets []string
	add := func(value string) {
		if value != "" {
			secrets = append(secrets, value)
		}
	}
	addPtr := func(value *schemas.SecretVar) {
		if value != nil {
			add(value.GetValue())
		}
	}
	for i := range keys {
		key := &keys[i]
		add(key.Value.GetValue())
		if c := key.AzureKeyConfig; c != nil {
			addPtr(c.ClientSecret)
		}
		if c := key.VertexKeyConfig; c != nil {
			credentials := c.AuthCredentials.GetValue()
			add(credentials)
			// A service account JSON is never echoed whole; its secret fields might be.
			var fields map[string]any
			if sonic.UnmarshalString(credentials, &fields) == nil {
				for _, name := range []string{"private_key", "private_key_id", "client_secret", "refresh_token"} {
					if value, ok := fields[name].(string); ok {
						add(value)
					}
				}
			}
		}
		if c := key.BedrockKeyConfig; c != nil {
			add(c.AccessKey.GetValue())
			add(c.SecretKey.GetValue())
			addPtr(c.SessionToken)
		}
		if c := key.BedrockMantleKeyConfig; c != nil {
			add(c.AccessKey.GetValue())
			add(c.SecretKey.GetValue())
			addPtr(c.SessionToken)
		}
		if c := key.SageMakerKeyConfig; c != nil {
			add(c.AccessKey.GetValue())
			add(c.SecretKey.GetValue())
			addPtr(c.SessionToken)
		}
		if c := key.DatabricksKeyConfig; c != nil {
			addPtr(c.ClientSecret)
		}
		if c := key.OAuthKeyConfig; c != nil {
			addPtr(c.ClientSecret)
			addPtr(c.PrivateKey)
			for _, value := range c.ExtraParams {
				add(value)
			}
		}
		if c := key.AnthropicKeyConfig; c != nil && c.WorkloadIdentity != nil && c.WorkloadIdentity.IdentitySource != nil {
			addPtr(c.WorkloadIdentity.IdentitySource.Token)
		}
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
// that does not, which that helper returns unchanged, keeps only its scheme:
// a malformed URL has no reliable boundary between userinfo, host, and path (a
// password can hold '@' and '/', and so can a path), so everything after the
// scheme is replaced rather than guessed at. So is a match holding a second
// "://": URLs joined without whitespace ("https://a/p,https://user:pw@b") are
// one match, and url.Parse reads the second URL's userinfo as part of the
// first one's path, where lib.RedactURLCredentials does not look.
func redactDecisionProbeURL(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return "redacted"
	}
	if !strings.Contains(rest, "://") {
		if _, err := url.Parse(raw); err == nil {
			return lib.RedactURLCredentials(raw)
		}
	}
	return scheme + "://redacted"
}

// redactContainingToken replaces each occurrence of token in text, together
// with the run of letters, digits, '-' and '_' around it, so the credential is
// removed whether it stands alone or sits inside a longer token.
func redactContainingToken(text, token string) string {
	var out strings.Builder
	last := 0
	for {
		i := strings.Index(text[last:], token)
		if i < 0 {
			break
		}
		start := last + i
		end := start + len(token)
		for start > last && isDecisionProbeTokenByte(text[start-1]) {
			start--
		}
		for end < len(text) && isDecisionProbeTokenByte(text[end]) {
			end++
		}
		out.WriteString(text[last:start])
		out.WriteString("[redacted]")
		last = end
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
