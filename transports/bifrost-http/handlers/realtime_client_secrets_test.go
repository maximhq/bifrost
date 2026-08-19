package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	openaiProvider "github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

func TestResolveRealtimeClientSecretTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		route        schemas.RealtimeSessionRoute
		body         []byte
		wantProvider schemas.ModelProvider
		wantModel    string
		wantErr      bool
	}{
		{
			name:         "base route with session model",
			route:        schemas.RealtimeSessionRoute{Path: "/v1/realtime/client_secrets", EndpointType: schemas.RealtimeSessionEndpointClientSecrets},
			body:         []byte(`{"session":{"model":"openai/gpt-4o-realtime-preview"}}`),
			wantProvider: schemas.OpenAI,
			wantModel:    "gpt-4o-realtime-preview",
		},
		{
			name:         "base route with top level model",
			route:        schemas.RealtimeSessionRoute{Path: "/v1/realtime/sessions", EndpointType: schemas.RealtimeSessionEndpointSessions},
			body:         []byte(`{"model":"openai/gpt-4o-realtime-preview"}`),
			wantProvider: schemas.OpenAI,
			wantModel:    "gpt-4o-realtime-preview",
		},
		{
			name:         "openai alias uses bare model",
			route:        schemas.RealtimeSessionRoute{Path: "/openai/v1/realtime/client_secrets", EndpointType: schemas.RealtimeSessionEndpointClientSecrets, DefaultProvider: schemas.OpenAI},
			body:         []byte(`{"session":{"model":"gpt-4o-realtime-preview"}}`),
			wantProvider: schemas.OpenAI,
			wantModel:    "gpt-4o-realtime-preview",
		},
		{
			name:    "base route rejects bare model",
			route:   schemas.RealtimeSessionRoute{Path: "/v1/realtime/client_secrets", EndpointType: schemas.RealtimeSessionEndpointClientSecrets},
			body:    []byte(`{"session":{"model":"gpt-4o-realtime-preview"}}`),
			wantErr: true,
		},
		{
			name:    "missing model",
			route:   schemas.RealtimeSessionRoute{Path: "/openai/v1/realtime/client_secrets", EndpointType: schemas.RealtimeSessionEndpointClientSecrets, DefaultProvider: schemas.OpenAI},
			body:    []byte(`{"session":{}}`),
			wantErr: true,
		},
		{
			name:         "GA transcription session resolves model from nested audio path",
			route:        schemas.RealtimeSessionRoute{Path: "/v1/realtime/client_secrets", EndpointType: schemas.RealtimeSessionEndpointClientSecrets},
			body:         []byte(`{"session":{"type":"transcription","audio":{"input":{"transcription":{"model":"openai/gpt-4o-transcribe"}}}}}`),
			wantProvider: schemas.OpenAI,
			wantModel:    "gpt-4o-transcribe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var ctx fasthttp.RequestCtx
			gotProvider, gotModel, _, err := resolveRealtimeClientSecretTarget(&ctx, &lib.Config{}, tt.route, tt.body)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRealtimeClientSecretTarget() error = %v", err)
			}
			if gotProvider != tt.wantProvider {
				t.Fatalf("provider = %q, want %q", gotProvider, tt.wantProvider)
			}
			if gotModel != tt.wantModel {
				t.Fatalf("model = %q, want %q", gotModel, tt.wantModel)
			}
		})
	}
}

func TestResolveRealtimeClientSecretTarget_NormalizesModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		route     schemas.RealtimeSessionRoute
		body      string
		wantModel string // bare model expected in normalized body
	}{
		{
			name:      "session.model provider prefix stripped",
			route:     schemas.RealtimeSessionRoute{Path: "/v1/realtime/client_secrets", EndpointType: schemas.RealtimeSessionEndpointClientSecrets},
			body:      `{"session":{"model":"openai/gpt-4o-realtime-preview","voice":"alloy"}}`,
			wantModel: "gpt-4o-realtime-preview",
		},
		{
			name:      "top-level model provider prefix stripped",
			route:     schemas.RealtimeSessionRoute{Path: "/v1/realtime/sessions", EndpointType: schemas.RealtimeSessionEndpointSessions},
			body:      `{"model":"openai/gpt-4o-realtime-preview"}`,
			wantModel: "gpt-4o-realtime-preview",
		},
		{
			name:      "bare model unchanged on alias route",
			route:     schemas.RealtimeSessionRoute{Path: "/openai/v1/realtime/client_secrets", EndpointType: schemas.RealtimeSessionEndpointClientSecrets, DefaultProvider: schemas.OpenAI},
			body:      `{"session":{"model":"gpt-4o-realtime-preview"}}`,
			wantModel: "gpt-4o-realtime-preview",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var ctx fasthttp.RequestCtx
			_, _, normalizedBody, err := resolveRealtimeClientSecretTarget(&ctx, &lib.Config{}, tt.route, []byte(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var root map[string]json.RawMessage
			if unmarshalErr := json.Unmarshal(normalizedBody, &root); unmarshalErr != nil {
				t.Fatalf("failed to unmarshal normalized body: %v", unmarshalErr)
			}

			// Check session.model if present
			if sessionJSON, ok := root["session"]; ok {
				var session map[string]json.RawMessage
				if unmarshalErr := json.Unmarshal(sessionJSON, &session); unmarshalErr != nil {
					t.Fatalf("failed to unmarshal session: %v", unmarshalErr)
				}
				if modelJSON, ok := session["model"]; ok {
					var model string
					if unmarshalErr := json.Unmarshal(modelJSON, &model); unmarshalErr != nil {
						t.Fatalf("failed to unmarshal session.model: %v", unmarshalErr)
					}
					if model != tt.wantModel {
						t.Fatalf("session.model = %q, want %q", model, tt.wantModel)
					}
				}
			}

			// Check top-level model if present
			if modelJSON, ok := root["model"]; ok {
				var model string
				if unmarshalErr := json.Unmarshal(modelJSON, &model); unmarshalErr != nil {
					t.Fatalf("failed to unmarshal model: %v", unmarshalErr)
				}
				if model != tt.wantModel {
					t.Fatalf("model = %q, want %q", model, tt.wantModel)
				}
			}
		})
	}
}

// TestGATranscriptionSessionEndToEndThroughFullNormalizationPath drives the
// full request path: handler-level resolveRealtimeClientSecretTarget, then
// feeds its output into the OpenAI provider's NormalizeRealtimeClientSecretRequest
// exactly as CreateRealtimeClientSecret does. Regression test for the bug
// where GA transcription-shaped sessions (session.type=="transcription",
// model nested at session.audio.input.transcription.model) 400'd with
// "session.model or model is required" because extraction only ever checked
// session.model — confirmed live against real OpenAI before this fix.
func TestGATranscriptionSessionEndToEndThroughFullNormalizationPath(t *testing.T) {
	t.Parallel()

	var ctx fasthttp.RequestCtx
	route := schemas.RealtimeSessionRoute{Path: "/v1/realtime/client_secrets", EndpointType: schemas.RealtimeSessionEndpointClientSecrets}
	body := []byte(`{"session":{"type":"transcription","audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"transcription":{"model":"openai/whisper-1","language":"en"}}}}}`)

	providerKey, model, handlerBody, err := resolveRealtimeClientSecretTarget(&ctx, &lib.Config{}, route, body)
	if err != nil {
		t.Fatalf("resolveRealtimeClientSecretTarget() error = %v", err)
	}
	if providerKey != schemas.OpenAI || model != "whisper-1" {
		t.Fatalf("provider/model = %q/%q, want %q/%q", providerKey, model, schemas.OpenAI, "whisper-1")
	}

	finalBody, finalModel, bifrostErr := openaiProvider.NormalizeRealtimeClientSecretRequest(handlerBody, schemas.OpenAI, schemas.RealtimeSessionEndpointClientSecrets)
	if bifrostErr != nil {
		t.Fatalf("NormalizeRealtimeClientSecretRequest() error = %v", bifrostErr)
	}
	if finalModel != "whisper-1" {
		t.Fatalf("model = %q, want %q", finalModel, "whisper-1")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(finalBody, &payload); err != nil {
		t.Fatalf("failed to unmarshal final body: %v", err)
	}
	var session map[string]json.RawMessage
	if err := json.Unmarshal(payload["session"], &session); err != nil {
		t.Fatalf("failed to unmarshal session: %v", err)
	}
	if _, hasModel := session["model"]; hasModel {
		t.Fatal("transcription-type session must not gain a top-level session.model field")
	}
	var audio map[string]json.RawMessage
	if err := json.Unmarshal(session["audio"], &audio); err != nil {
		t.Fatalf("failed to unmarshal session.audio: %v", err)
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(audio["input"], &input); err != nil {
		t.Fatalf("failed to unmarshal session.audio.input: %v", err)
	}
	var transcription map[string]any
	if err := json.Unmarshal(input["transcription"], &transcription); err != nil {
		t.Fatalf("failed to unmarshal session.audio.input.transcription: %v", err)
	}
	if transcription["model"] != "whisper-1" {
		t.Fatalf("session.audio.input.transcription.model = %v, want %q", transcription["model"], "whisper-1")
	}
	if transcription["language"] != "en" {
		t.Fatalf("session.audio.input.transcription.language = %v, want %q (must survive the two-pass normalization)", transcription["language"], "en")
	}
	if _, hasFormat := input["format"]; !hasFormat {
		t.Fatal("session.audio.input.format must survive normalization")
	}
}

// TestFullRealtimeSessionWithTranscriptionSiblingEndToEnd is a regression
// test for a codex-review-caught bug: a full realtime session using the
// legacy top-level "model" field, with live input-audio transcription
// enabled as a completely standard sibling feature, was being misclassified
// as transcription-only by resolveRealtimeClientSecretTarget — the real
// conversational model was lost entirely. Drives the exact same
// handler->provider two-pass path as CreateRealtimeClientSecret does.
func TestFullRealtimeSessionWithTranscriptionSiblingEndToEnd(t *testing.T) {
	t.Parallel()

	var ctx fasthttp.RequestCtx
	route := schemas.RealtimeSessionRoute{Path: "/v1/realtime/client_secrets", EndpointType: schemas.RealtimeSessionEndpointClientSecrets}
	body := []byte(`{"model":"openai/gpt-4o-realtime-preview","session":{"audio":{"input":{"transcription":{"model":"openai/whisper-1"}}}}}`)

	providerKey, model, handlerBody, err := resolveRealtimeClientSecretTarget(&ctx, &lib.Config{}, route, body)
	if err != nil {
		t.Fatalf("resolveRealtimeClientSecretTarget() error = %v", err)
	}
	if providerKey != schemas.OpenAI || model != "gpt-4o-realtime-preview" {
		t.Fatalf("provider/model = %q/%q, want %q/%q", providerKey, model, schemas.OpenAI, "gpt-4o-realtime-preview")
	}

	finalBody, finalModel, bifrostErr := openaiProvider.NormalizeRealtimeClientSecretRequest(handlerBody, schemas.OpenAI, schemas.RealtimeSessionEndpointClientSecrets)
	if bifrostErr != nil {
		t.Fatalf("NormalizeRealtimeClientSecretRequest() error = %v", bifrostErr)
	}
	if finalModel != "gpt-4o-realtime-preview" {
		t.Fatalf("final model = %q, want %q", finalModel, "gpt-4o-realtime-preview")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(finalBody, &payload); err != nil {
		t.Fatalf("failed to unmarshal final body: %v", err)
	}
	var session map[string]json.RawMessage
	if err := json.Unmarshal(payload["session"], &session); err != nil {
		t.Fatalf("failed to unmarshal session: %v", err)
	}
	var sessionModel string
	if err := json.Unmarshal(session["model"], &sessionModel); err != nil || sessionModel != "gpt-4o-realtime-preview" {
		t.Fatalf("session.model = %v, want %q", session["model"], "gpt-4o-realtime-preview")
	}
	var sessionType string
	if err := json.Unmarshal(session["type"], &sessionType); err != nil || sessionType != "realtime" {
		t.Fatalf("session.type = %v, want %q (must not be reclassified as transcription-only)", session["type"], "realtime")
	}
}

// TestRewriteGASessionTranscriptionModelAppliesAliasResolution covers the
// alias-resolution rewrite path (handleRequest calls this after key selection
// when key.Aliases.Resolve changes the model) for a GA transcription session,
// which has no top-level session.model for the generic rewrite to target.
func TestRewriteGASessionTranscriptionModelAppliesAliasResolution(t *testing.T) {
	t.Parallel()

	session := map[string]json.RawMessage{
		"type":  json.RawMessage(`"transcription"`),
		"audio": json.RawMessage(`{"input":{"transcription":{"model":"whisper-1","language":"en"}}}`),
	}
	normalizedModel, err := json.Marshal("gpt-4o-transcribe")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rewritten, changed, bifrostErr := rewriteGASessionTranscriptionModel(session, normalizedModel)
	if bifrostErr != nil {
		t.Fatalf("rewriteGASessionTranscriptionModel() error = %v", bifrostErr)
	}
	if !changed {
		t.Fatal("expected changed=true for a GA transcription session")
	}

	var rewrittenSession map[string]json.RawMessage
	if err := json.Unmarshal(rewritten, &rewrittenSession); err != nil {
		t.Fatalf("failed to unmarshal rewritten session: %v", err)
	}
	var audio map[string]json.RawMessage
	if err := json.Unmarshal(rewrittenSession["audio"], &audio); err != nil {
		t.Fatalf("failed to unmarshal audio: %v", err)
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(audio["input"], &input); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}
	var transcription map[string]any
	if err := json.Unmarshal(input["transcription"], &transcription); err != nil {
		t.Fatalf("failed to unmarshal transcription: %v", err)
	}
	if transcription["model"] != "gpt-4o-transcribe" {
		t.Fatalf("model = %v, want %q (alias resolution must reach the nested field)", transcription["model"], "gpt-4o-transcribe")
	}
	if transcription["language"] != "en" {
		t.Fatalf("language = %v, want %q", transcription["language"], "en")
	}
}

func TestRewriteGASessionTranscriptionModelNoOpForFullRealtimeSession(t *testing.T) {
	t.Parallel()

	// A full realtime session with no audio.input.transcription at all should
	// be left untouched (changed=false) — the caller's own session.model path
	// handles that case.
	session := map[string]json.RawMessage{
		"instructions": json.RawMessage(`"be helpful"`),
	}
	normalizedModel, _ := json.Marshal("gpt-realtime")

	_, changed, bifrostErr := rewriteGASessionTranscriptionModel(session, normalizedModel)
	if bifrostErr != nil {
		t.Fatalf("rewriteGASessionTranscriptionModel() error = %v", bifrostErr)
	}
	if changed {
		t.Fatal("expected changed=false when session has no audio.input.transcription")
	}
}

func TestParseRealtimeEphemeralKeyMapping(t *testing.T) {
	t.Parallel()

	token, ttl, ok := parseRealtimeEphemeralKeyMapping([]byte(`{
		"value": "ek_test_123",
		"expires_at": 4102444800
	}`))
	if !ok {
		t.Fatal("expected ephemeral mapping to be parsed")
	}
	if token != "ek_test_123" {
		t.Fatalf("token = %q, want %q", token, "ek_test_123")
	}
	if ttl <= 0 {
		t.Fatalf("ttl = %v, want > 0", ttl)
	}
}

func TestParseRealtimeEphemeralKeyMapping_NestedFallback(t *testing.T) {
	t.Parallel()

	token, ttl, ok := parseRealtimeEphemeralKeyMapping([]byte(`{
		"client_secret": {
			"value": "ek_test_nested",
			"expires_at": 4102444800
		}
	}`))
	if !ok {
		t.Fatal("expected nested ephemeral mapping to be parsed")
	}
	if token != "ek_test_nested" {
		t.Fatalf("token = %q, want %q", token, "ek_test_nested")
	}
	if ttl <= 0 {
		t.Fatalf("ttl = %v, want > 0", ttl)
	}
}

func TestCacheRealtimeEphemeralKeyMappingStoresKeyID(t *testing.T) {
	t.Parallel()

	store, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatalf("kvstore.New() error = %v", err)
	}
	defer store.Close()

	body := []byte(`{
		"value": "ek_test_456",
		"expires_at": ` + "4102444800" + `
	}`)
	cacheRealtimeEphemeralKeyMapping(store, body, "key_123", "sk-bf-test")

	raw, err := store.Get(buildRealtimeEphemeralKeyMappingKey("ek_test_456"))
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	value, ok := raw.([]byte)
	if !ok {
		t.Fatalf("cached value type = %T, want []byte", raw)
	}
	var mapping realtimeEphemeralKeyMapping
	if err := json.Unmarshal(value, &mapping); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if mapping.KeyID != "key_123" {
		t.Fatalf("mapping.KeyID = %q, want %q", mapping.KeyID, "key_123")
	}
	if mapping.VirtualKey != "sk-bf-test" {
		t.Fatalf("mapping.VirtualKey = %q, want %q", mapping.VirtualKey, "sk-bf-test")
	}
}

func TestCacheRealtimeEphemeralKeyMappingSkipsExpiredSecrets(t *testing.T) {
	t.Parallel()

	store, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatalf("kvstore.New() error = %v", err)
	}
	defer store.Close()

	expired := time.Now().Add(-time.Minute).Unix()
	body := fmt.Appendf(nil, `{
		"value": "ek_expired",
		"expires_at": %d
	}`, expired)
	cacheRealtimeEphemeralKeyMapping(store, body, "key_123", "")

	if _, err := store.Get(buildRealtimeEphemeralKeyMappingKey("ek_expired")); err == nil {
		t.Fatal("expected no cached mapping for expired token")
	}
}

func TestIsJSONContentType(t *testing.T) {
	t.Parallel()

	if !isJSONContentType("application/json; charset=utf-8") {
		t.Fatal("expected application/json content type to pass")
	}
	if !isJSONContentType("application/vnd.openai+json") {
		t.Fatal("expected +json content type to pass")
	}
	if isJSONContentType("text/plain") {
		t.Fatal("expected text/plain content type to fail")
	}
}

type mockRealtimeMintingGovernancePlugin struct {
	err            *schemas.BifrostError
	seenUserID     string
	seenVirtualKey string
	seenProvider   schemas.ModelProvider
	seenModel      string
	evaluateCalls  int
}

func (m *mockRealtimeMintingGovernancePlugin) GetName() string {
	return governance.PluginName
}

func (m *mockRealtimeMintingGovernancePlugin) EvaluateGovernanceRequest(ctx *schemas.BifrostContext, evaluationRequest *governance.EvaluationRequest, _ schemas.RequestType) (*governance.EvaluationResult, *schemas.BifrostError) {
	m.evaluateCalls++
	m.seenUserID = ""
	m.seenVirtualKey = ""
	m.seenProvider = ""
	m.seenModel = ""
	if evaluationRequest != nil {
		m.seenUserID = evaluationRequest.UserID
		m.seenVirtualKey = evaluationRequest.VirtualKey
		m.seenProvider = evaluationRequest.Provider
		m.seenModel = evaluationRequest.Model
	}
	if ctx != nil && m.seenVirtualKey == "" {
		m.seenVirtualKey = bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	}
	if m.err != nil {
		return nil, m.err
	}
	return &governance.EvaluationResult{Decision: governance.DecisionAllow}, nil
}

func (m *mockRealtimeMintingGovernancePlugin) HTTPTransportPreHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

func (m *mockRealtimeMintingGovernancePlugin) HTTPTransportPostHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, _ *schemas.HTTPResponse) error {
	return nil
}

func (m *mockRealtimeMintingGovernancePlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (m *mockRealtimeMintingGovernancePlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

func (m *mockRealtimeMintingGovernancePlugin) PostLLMHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return result, bifrostErr, nil
}

func (m *mockRealtimeMintingGovernancePlugin) PreMCPHook(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	return req, nil, nil
}

func (m *mockRealtimeMintingGovernancePlugin) PostMCPHook(_ *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

func (m *mockRealtimeMintingGovernancePlugin) Cleanup() error {
	return nil
}

func (m *mockRealtimeMintingGovernancePlugin) GetGovernanceStore() governance.GovernanceStore {
	return nil
}

func TestRealtimeClientSecretsEvaluateMintingGovernance_RequiresAccess(t *testing.T) {
	t.Parallel()

	config := &lib.Config{}
	plugin := &mockRealtimeMintingGovernancePlugin{
		err: &schemas.BifrostError{
			Type:       schemas.Ptr("virtual_key_required"),
			StatusCode: schemas.Ptr(401),
			Error: &schemas.ErrorField{
				Message: "virtual key is required. Provide a virtual key via the x-bf-vk header.",
			},
		},
	}
	plugins := []schemas.BasePlugin{plugin}
	config.BasePlugins.Store(&plugins)

	handler := NewRealtimeClientSecretsHandler(nil, config)
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer bifrostCtx.Done()

	err := handler.evaluateMintingGovernance(bifrostCtx, schemas.OpenAI, "gpt-realtime")
	if err == nil {
		t.Fatal("expected governance error")
	}
	if err.StatusCode == nil {
		t.Fatal("expected status code")
	}
	if got, want := *err.StatusCode, fasthttp.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestRealtimeClientSecretsEvaluateMintingGovernance_PassesContext(t *testing.T) {
	t.Parallel()

	config := &lib.Config{}
	plugin := &mockRealtimeMintingGovernancePlugin{}
	plugins := []schemas.BasePlugin{
		plugin,
	}
	config.BasePlugins.Store(&plugins)

	handler := NewRealtimeClientSecretsHandler(nil, config)
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer bifrostCtx.Done()
	bifrostCtx.SetValue(schemas.BifrostContextKeyUserID, "user_123")
	bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-123")

	if err := handler.evaluateMintingGovernance(bifrostCtx, schemas.OpenAI, "gpt-realtime"); err != nil {
		t.Fatalf("unexpected governance error: %v", err)
	}
	if plugin.evaluateCalls != 1 {
		t.Fatalf("evaluate calls = %d, want 1", plugin.evaluateCalls)
	}
	if plugin.seenUserID != "user_123" {
		t.Fatalf("governance user id = %q, want %q", plugin.seenUserID, "user_123")
	}
	if plugin.seenVirtualKey != "sk-bf-123" {
		t.Fatalf("virtual key = %q, want %q", plugin.seenVirtualKey, "sk-bf-123")
	}
	if plugin.seenProvider != schemas.OpenAI {
		t.Fatalf("provider = %q, want %q", plugin.seenProvider, schemas.OpenAI)
	}
	if plugin.seenModel != "gpt-realtime" {
		t.Fatalf("model = %q, want %q", plugin.seenModel, "gpt-realtime")
	}
}

func TestRealtimeClientSecretsEvaluateMintingGovernance_ContinuesWithoutGovernance(t *testing.T) {
	t.Parallel()

	handler := NewRealtimeClientSecretsHandler(nil, &lib.Config{})
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer bifrostCtx.Done()

	if err := handler.evaluateMintingGovernance(bifrostCtx, schemas.OpenAI, "gpt-realtime"); err != nil {
		t.Fatalf("unexpected governance error without plugin: %v", err)
	}
}
