package handlers

import (
	"encoding/json"
	"fmt"
	"mime"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/modelcatalogresolver"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// RealtimeClientSecretsHandler exposes OpenAI-compatible HTTP routes for
// minting short-lived Realtime client secrets.
type RealtimeClientSecretsHandler struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	routeSpecs   map[string]schemas.RealtimeSessionRoute
}

func NewRealtimeClientSecretsHandler(client *bifrost.Bifrost, config *lib.Config) *RealtimeClientSecretsHandler {
	return &RealtimeClientSecretsHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		routeSpecs:   make(map[string]schemas.RealtimeSessionRoute),
	}
}

func (h *RealtimeClientSecretsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleRequest, middlewares...)
	for _, route := range h.realtimeSessionRoutes() {
		h.routeSpecs[route.Path] = route
		r.POST(route.Path, handler)
	}
}

func (h *RealtimeClientSecretsHandler) findGovernancePlugin() governance.BaseGovernancePlugin {
	basePlugins := h.config.BasePlugins.Load()
	if basePlugins == nil {
		return nil
	}

	for _, plugin := range *basePlugins {
		if governancePlugin, ok := plugin.(governance.BaseGovernancePlugin); ok {
			return governancePlugin
		}
	}

	return nil
}

func (h *RealtimeClientSecretsHandler) handleRequest(ctx *fasthttp.RequestCtx) {
	if !isJSONContentType(string(ctx.Request.Header.ContentType())) {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			"Content-Type must be application/json",
			nil,
		))
		return
	}

	body := append([]byte(nil), ctx.Request.Body()...)
	route, ok := h.routeSpecs[string(ctx.Path())]
	if !ok {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusNotFound,
			"invalid_request_error",
			"unsupported realtime client secret route",
			nil,
		))
		return
	}

	providerKey, model, normalizedBody, err := resolveRealtimeClientSecretTarget(ctx, h.config, route, body)
	if err != nil {
		SendBifrostError(ctx, err)
		return
	}

	logger.Info("[realtime-client-secrets] request: path=%s provider=%s model=%s",
		string(ctx.Path()), providerKey, model)

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore)
	defer cancel()
	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RealtimeRequest)
	if route.DefaultProvider == schemas.OpenAI {
		bifrostCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}
	if governanceUserID, ok := ctx.UserValue(schemas.BifrostContextKeyUserID).(string); ok && governanceUserID != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyUserID, governanceUserID)
	}
	if userName, ok := ctx.UserValue(schemas.BifrostContextKeyUserName).(string); ok && userName != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyUserName, userName)
	}
	if bifrostErr := h.evaluateMintingGovernance(bifrostCtx, providerKey, model); bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	provider := h.client.GetProviderByKey(providerKey)
	if provider == nil {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			"provider not found: "+string(providerKey),
			nil,
		))
		return
	}

	key, keyErr := h.client.SelectKeyForProviderRequestType(bifrostCtx, schemas.RealtimeRequest, providerKey, model)
	if keyErr != nil {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			keyErr.Error(),
			keyErr,
		))
		return
	}

	// Resolve model aliases now that the key is selected so the forwarded body
	// carries the provider's canonical model, matching wsrealtime/webrtc flows.
	if resolved := key.Aliases.Resolve(model); resolved != "" && resolved != model {
		model = resolved
		reparsed, parseErr := schemas.ParseRealtimeClientSecretBody(normalizedBody)
		if parseErr != nil {
			SendBifrostError(ctx, parseErr)
			return
		}
		rewritten, normalizeErr := normalizeRealtimeClientSecretBody(reparsed, model)
		if normalizeErr != nil {
			SendBifrostError(ctx, normalizeErr)
			return
		}
		normalizedBody = rewritten
	}

	sessionProvider, ok := provider.(schemas.RealtimeSessionProvider)
	if !ok {
		SendBifrostError(ctx, realtimeSessionNotSupportedError(providerKey, provider))
		return
	}

	resp, bifrostErr := sessionProvider.CreateRealtimeClientSecret(bifrostCtx, key, normalizedBody)
	if bifrostErr != nil {
		logger.Error("[realtime-client-secrets] upstream error: provider=%s model=%s error=%s",
			providerKey, model, bifrostErr.Error)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	logger.Info("[realtime-client-secrets] upstream success: provider=%s model=%s status=%d",
		providerKey, model, resp.StatusCode)
	// Prefer the settled identity: it is what minting governance evaluated, so the
	// mapping cannot attribute later turns to a different key than the one that was
	// authorized here. The raw-header parse is only a fallback for routes where the
	// settled context does not retain the virtual key at mapping time (observed live
	// for bearer-based mints).
	virtualKey := realtimeMappingVirtualKey(bifrostCtx)
	if virtualKey == "" {
		if presented := governance.ParseVirtualKeyFromFastHTTPRequest(ctx); presented != nil {
			virtualKey = *presented
		}
	}
	if bifrostErr := replaceAndCacheRealtimeEphemeralToken(
		h.handlerStore.GetKVStore(),
		resp,
		key.ID,
		virtualKey,
	); bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	writeRealtimeClientSecretResponse(ctx, resp)
}

func (h *RealtimeClientSecretsHandler) evaluateMintingGovernance(
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
) *schemas.BifrostError {
	governancePlugin := h.findGovernancePlugin()
	if governancePlugin == nil {
		return nil
	}

	// The credential and the user the request was made as travel on the context, which is where
	// evaluation reads them from, so naming them here would only be a second copy to keep in step.
	_, bifrostErr := governancePlugin.Evaluate(bifrostCtx, &governance.EvaluationRequest{
		RequestType: schemas.RealtimeRequest,
		Provider:    providerKey,
		Model:       model,
	})
	return bifrostErr
}

func (h *RealtimeClientSecretsHandler) realtimeSessionRoutes() []schemas.RealtimeSessionRoute {
	routes := []schemas.RealtimeSessionRoute{
		{Path: "/v1/realtime/client_secrets"},
	}

	for _, path := range integrations.OpenAIRealtimeClientSecretPaths("/openai") {
		routes = append(routes, schemas.RealtimeSessionRoute{
			Path:            path,
			DefaultProvider: schemas.OpenAI,
		})
	}
	return routes
}

func resolveRealtimeClientSecretTarget(ctx *fasthttp.RequestCtx, config *lib.Config, route schemas.RealtimeSessionRoute, body []byte) (schemas.ModelProvider, string, []byte, *schemas.BifrostError) {
	root, err := schemas.ParseRealtimeClientSecretBody(body)
	if err != nil {
		return "", "", nil, err
	}

	rawModel, err := schemas.ExtractRealtimeClientSecretModel(root)
	if err != nil {
		return "", "", nil, err
	}

	defaultProvider := route.DefaultProvider
	providerKey, model := schemas.ParseModelString(rawModel, defaultProvider)
	// Model catalog auto-resolution for bare model names on /v1 client secret routes
	if defaultProvider == "" && providerKey == "" && model != "" {
		selected, candidates := modelcatalogresolver.ResolveProviderFromCatalog(nil, config.ModelCatalog, model)
		if selected != "" {
			ctx.SetUserValue(lib.FastHTTPUserValueModelCatalogResolution, &lib.ModelCatalogResolution{
				Model:            model,
				ResolvedProvider: selected,
				AllProviders:     candidates,
			})
			providerKey = selected
		}
	}
	if defaultProvider == "" && providerKey == "" {
		return "", "", nil, newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			"session.model must use provider/model on /v1 realtime client secret routes",
			nil,
		)
	}
	if providerKey == "" || model == "" {
		return "", "", nil, newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			"session.model is required",
			nil,
		)
	}

	// Normalize the forwarded body so the upstream provider sees the bare model
	// (strip provider prefix). Mirrors resolveRealtimeSDPTarget normalization.
	normalizedBody, normalizeErr := normalizeRealtimeClientSecretBody(root, model)
	if normalizeErr != nil {
		return "", "", nil, normalizeErr
	}

	return providerKey, model, normalizedBody, nil
}

func normalizeRealtimeClientSecretBody(root map[string]json.RawMessage, bareModel string) ([]byte, *schemas.BifrostError) {
	normalizedModel, marshalErr := json.Marshal(bareModel)
	if marshalErr != nil {
		return nil, newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to encode normalized model", marshalErr)
	}

	// Classify once, via the same canonical classifier extraction and the
	// provider-layer normalizer use (schemas.IsGATranscriptionSessionBody),
	// so this pass can't disagree with them about which shape a body is —
	// a full realtime session must never take the GA-transcription rewrite
	// branch just because it also enables live input-audio transcription as
	// a sibling feature (checked via root.model/session.model presence, not
	// just whether session.model happens to be set).
	isGATranscription := schemas.IsGATranscriptionSessionBody(root)

	// Normalize session.model if present
	if sessionJSON, ok := root["session"]; ok && len(sessionJSON) > 0 {
		var session map[string]json.RawMessage
		if err := json.Unmarshal(sessionJSON, &session); err == nil {
			if isGATranscription {
				if rewritten, changed, err := rewriteGASessionTranscriptionModel(session, normalizedModel); err != nil {
					return nil, err
				} else if changed {
					root["session"] = rewritten
				}
			} else if _, hasModel := session["model"]; hasModel {
				session["model"] = normalizedModel
				rewritten, err := json.Marshal(session)
				if err != nil {
					return nil, newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to re-encode session", err)
				}
				root["session"] = rewritten
			}
		}
	}
	// Normalize top-level model if present
	if _, ok := root["model"]; ok {
		root["model"] = normalizedModel
	}

	normalized, marshalErr := json.Marshal(root)
	if marshalErr != nil {
		return nil, newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to re-encode body", marshalErr)
	}
	return normalized, nil
}

// rewriteGASessionTranscriptionModel updates session.audio.input.transcription.model
// in place for a GA transcription-shaped session (RealtimeTranscriptionSessionCreateRequestGA,
// which has no top-level session.model — the field this function's caller
// already checked is absent). Needed so that alias resolution (which changes
// the routing model after key selection) actually reaches the field the
// provider layer reads, instead of silently leaving the pre-alias model in
// the body. Returns changed=false when the session isn't transcription-shaped
// (e.g. a full realtime session with no model set yet, which the caller
// should leave untouched).
func rewriteGASessionTranscriptionModel(session map[string]json.RawMessage, normalizedModel json.RawMessage) ([]byte, bool, *schemas.BifrostError) {
	audioJSON, ok := session["audio"]
	if !ok || len(audioJSON) == 0 || string(audioJSON) == "null" {
		return nil, false, nil
	}
	var audio map[string]json.RawMessage
	if err := json.Unmarshal(audioJSON, &audio); err != nil {
		return nil, false, newRealtimeClientSecretHandlerError(fasthttp.StatusBadRequest, "invalid_request_error", "session.audio must be an object", err)
	}
	inputJSON, ok := audio["input"]
	if !ok || len(inputJSON) == 0 || string(inputJSON) == "null" {
		return nil, false, nil
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return nil, false, newRealtimeClientSecretHandlerError(fasthttp.StatusBadRequest, "invalid_request_error", "session.audio.input must be an object", err)
	}
	transJSON, ok := input["transcription"]
	if !ok || len(transJSON) == 0 || string(transJSON) == "null" {
		return nil, false, nil
	}
	var transcription map[string]json.RawMessage
	if err := json.Unmarshal(transJSON, &transcription); err != nil {
		return nil, false, newRealtimeClientSecretHandlerError(fasthttp.StatusBadRequest, "invalid_request_error", "session.audio.input.transcription must be an object", err)
	}
	transcription["model"] = normalizedModel
	transcriptionJSON, err := json.Marshal(transcription)
	if err != nil {
		return nil, false, newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to re-encode session.audio.input.transcription", err)
	}
	input["transcription"] = transcriptionJSON
	inputJSON, err = json.Marshal(input)
	if err != nil {
		return nil, false, newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to re-encode session.audio.input", err)
	}
	audio["input"] = inputJSON
	audioJSON, err = json.Marshal(audio)
	if err != nil {
		return nil, false, newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to re-encode session.audio", err)
	}
	session["audio"] = audioJSON
	rewritten, err := json.Marshal(session)
	if err != nil {
		return nil, false, newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to re-encode session", err)
	}
	return rewritten, true, nil
}

const realtimeEphemeralKeyMappingPrefix = "realtime:ephemeral-key:"

type realtimeEphemeralKeyMapping struct {
	KeyID         string `json:"key_id,omitempty"`
	VirtualKey    string `json:"virtual_key,omitempty"`
	ProviderToken string `json:"provider_token,omitempty"`
}

func realtimeMappingVirtualKey(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	if ctx.Grant() != nil {
		if virtualKey := governance.PresentedVirtualKey(ctx); virtualKey != "" {
			return virtualKey
		}
	}
	return bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
}

func replaceAndCacheRealtimeEphemeralToken(kv schemas.KVStore, resp *schemas.BifrostPassthroughResponse, keyID string, virtualKey string) *schemas.BifrostError {
	if kv == nil || resp == nil || len(resp.Body) == 0 || strings.TrimSpace(keyID) == "" {
		return newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to secure realtime client secret", nil)
	}

	providerToken, ttl, nested, ok := parseRealtimeEphemeralKeyMapping(resp.Body)
	if !ok || strings.TrimSpace(providerToken) == "" || ttl <= 0 {
		return newRealtimeClientSecretHandlerError(fasthttp.StatusBadGateway, "server_error", "provider returned an invalid realtime client secret", nil)
	}

	bifrostToken := "ek_bf_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	body, err := replaceRealtimeEphemeralToken(resp.Body, bifrostToken, nested)
	if err != nil {
		return newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to secure realtime client secret", err)
	}

	mapping := realtimeEphemeralKeyMapping{
		KeyID:         strings.TrimSpace(keyID),
		VirtualKey:    strings.TrimSpace(virtualKey),
		ProviderToken: strings.TrimSpace(providerToken),
	}
	if err := kv.SetWithTTL(buildRealtimeEphemeralKeyMappingKey(bifrostToken), mapping, ttl); err != nil {
		logger.Error("failed to cache realtime ephemeral key mapping for key_id=%s: %v", keyID, err)
		return newRealtimeClientSecretHandlerError(fasthttp.StatusInternalServerError, "server_error", "failed to secure realtime client secret", err)
	}

	resp.Body = body
	return nil
}

func replaceRealtimeEphemeralToken(body []byte, token string, nested bool) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}

	if nested {
		var clientSecret map[string]json.RawMessage
		if err := json.Unmarshal(root["client_secret"], &clientSecret); err != nil {
			return nil, err
		}
		value, err := json.Marshal(token)
		if err != nil {
			return nil, err
		}
		clientSecret["value"] = value
		rewritten, err := json.Marshal(clientSecret)
		if err != nil {
			return nil, err
		}
		root["client_secret"] = rewritten
	} else {
		value, err := json.Marshal(token)
		if err != nil {
			return nil, err
		}
		root["value"] = value
	}

	return json.Marshal(root)
}

func parseRealtimeEphemeralKeyMapping(body []byte) (string, time.Duration, bool, bool) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return "", 0, false, false
	}

	var clientSecret struct {
		Value     string `json:"value"`
		ExpiresAt int64  `json:"expires_at"`
	}

	// OpenAI client_secrets responses expose the ephemeral token at the top level.
	// Keep accepting the nested shape too so the mapping logic stays compatible
	// with any provider/session endpoint variants that wrap the secret object.
	nested := false
	if err := json.Unmarshal(body, &clientSecret); err != nil || strings.TrimSpace(clientSecret.Value) == "" || clientSecret.ExpiresAt <= 0 {
		clientSecretRaw, ok := root["client_secret"]
		if !ok || len(clientSecretRaw) == 0 || string(clientSecretRaw) == "null" {
			return "", 0, false, false
		}
		// Reset before reparsing: Unmarshal preserves fields absent from the new
		// JSON, so leftovers from the failed top-level attempt could otherwise
		// combine with nested fields into a token/expiry pair no single shape
		// actually carried — and misreport which shape holds the secret.
		clientSecret.Value = ""
		clientSecret.ExpiresAt = 0
		if err := json.Unmarshal(clientSecretRaw, &clientSecret); err != nil {
			return "", 0, false, false
		}
		nested = true
	}
	if strings.TrimSpace(clientSecret.Value) == "" || clientSecret.ExpiresAt <= 0 {
		return "", 0, false, false
	}

	ttl := time.Until(time.Unix(clientSecret.ExpiresAt, 0))
	if ttl <= 0 {
		return "", 0, false, false
	}

	return clientSecret.Value, ttl, nested, true
}

func buildRealtimeEphemeralKeyMappingKey(token string) string {
	return realtimeEphemeralKeyMappingPrefix + strings.TrimSpace(token)
}

func realtimeSessionNotSupportedError(providerKey schemas.ModelProvider, provider schemas.Provider) *schemas.BifrostError {
	if rtProvider, ok := provider.(schemas.RealtimeProvider); ok && rtProvider.SupportsRealtimeAPI() {
		return newRealtimeClientSecretHandlerError(
			fasthttp.StatusBadRequest,
			"invalid_request_error",
			fmt.Sprintf("provider %s supports realtime websocket connections but not realtime client secret creation", providerKey),
			nil,
		)
	}

	return newRealtimeClientSecretHandlerError(
		fasthttp.StatusBadRequest,
		"invalid_request_error",
		fmt.Sprintf("provider %s does not support realtime client secret creation", providerKey),
		nil,
	)
}

func newRealtimeClientSecretHandlerError(status int, errorType, message string, err error) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(status),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr(errorType),
			Message: message,
			Error:   err,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.RealtimeRequest,
		},
	}
}

func writeRealtimeClientSecretResponse(ctx *fasthttp.RequestCtx, resp *schemas.BifrostPassthroughResponse) {
	if resp == nil {
		SendBifrostError(ctx, newRealtimeClientSecretHandlerError(
			fasthttp.StatusInternalServerError,
			"server_error",
			"provider returned an empty realtime client secret response",
			nil,
		))
		return
	}

	for key, value := range resp.Headers {
		ctx.Response.Header.Set(key, value)
	}
	if len(ctx.Response.Header.ContentType()) == 0 {
		ctx.SetContentType("application/json")
	}
	ctx.SetStatusCode(resp.StatusCode)
	ctx.SetBody(resp.Body)
}

func isJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
