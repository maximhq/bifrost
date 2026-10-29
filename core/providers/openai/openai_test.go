package openai_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/openai"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type oauthTestLogger struct{}

func (oauthTestLogger) Debug(string, ...any)                   {}
func (oauthTestLogger) Info(string, ...any)                    {}
func (oauthTestLogger) Warn(string, ...any)                    {}
func (oauthTestLogger) Error(string, ...any)                   {}
func (oauthTestLogger) Fatal(string, ...any)                   {}
func (oauthTestLogger) SetLevel(schemas.LogLevel)              {}
func (oauthTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (oauthTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// oauthStub is one server playing both the token endpoint and the OpenAI-compatible upstream,
// which is the shape of a custom provider whose IdP and API share a host.
type oauthStub struct {
	*httptest.Server
	mu          sync.Mutex
	tokenHits   int
	tokenStatus int
	form        url.Values
	basicUser   string
	auths       []string
}

func newOAuthStub(t *testing.T) *oauthStub {
	t.Helper()
	st := &oauthStub{tokenStatus: http.StatusOK}
	st.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			user, _, _ := r.BasicAuth()
			st.mu.Lock()
			st.tokenHits++
			st.form, st.basicUser = form, user
			status := st.tokenStatus
			st.mu.Unlock()
			w.WriteHeader(status)
			if status != http.StatusOK {
				_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"unknown client"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"minted-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		st.mu.Lock()
		st.auths = append(st.auths, r.Header.Get("Authorization"))
		st.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/v1/files/") {
			_, _ = w.Write([]byte(`{"id":"file-1","object":"file","bytes":1,"created_at":1700000000,"filename":"f.jsonl","purpose":"batch","status":"processed"}`))
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m","object":"model","created":1700000000,"owned_by":"acme"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(st.Close)
	return st
}

func newOAuthProvider(t *testing.T, baseURL string) *openai.OpenAIProvider {
	t.Helper()
	return openai.NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, CustomProviderKey: "acme"},
	}, oauthTestLogger{})
}

func oauthChat(t *testing.T, provider *openai.OpenAIProvider, key schemas.Key) *schemas.BifrostError {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: "acme",
		Model:    "m",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	})
	return bErr
}

// TestOAuthKeyConfigMintsBearer pins the custom-provider OAuth path end to end through the
// provider: a key with no value and an oauth_key_config mints a bearer from the token
// endpoint, sends it as Authorization, and reuses it across requests.
func TestOAuthKeyConfigMintsBearer(t *testing.T) {
	t.Parallel()
	stub := newOAuthStub(t)
	provider := newOAuthProvider(t, stub.URL)
	key := schemas.Key{
		Models: []string{"*"},
		OAuthKeyConfig: &schemas.OAuthKeyConfig{
			GrantType:    schemas.OAuthGrantClientCredentials,
			TokenURL:     *schemas.NewSecretVar(stub.URL + "/oauth/token"),
			ClientID:     schemas.NewSecretVar("acme-id"),
			ClientSecret: schemas.NewSecretVar("acme-secret"),
			Scopes:       []string{"inference"},
		},
	}
	for i := range 3 {
		if bErr := oauthChat(t, provider, key); bErr != nil {
			t.Fatalf("ChatCompletion attempt %d: %v", i+1, bErr.Error.Message)
		}
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.tokenHits != 1 {
		t.Fatalf("token mints = %d, want 1", stub.tokenHits)
	}
	if stub.form.Get("grant_type") != "client_credentials" || stub.form.Get("scope") != "inference" || stub.basicUser != "acme-id" {
		t.Fatalf("token request: grant=%q scope=%q user=%q", stub.form.Get("grant_type"), stub.form.Get("scope"), stub.basicUser)
	}
	if len(stub.auths) != 3 {
		t.Fatalf("upstream calls = %d, want 3", len(stub.auths))
	}
	for _, a := range stub.auths {
		if a != "Bearer minted-token" {
			t.Fatalf("Authorization = %q, want the minted bearer", a)
		}
	}
}

// TestOAuthKeyConfigStaticValueWins pins that a key value is sent verbatim and the token
// endpoint is never contacted, so adding oauth_key_config next to a value changes nothing.
func TestOAuthKeyConfigStaticValueWins(t *testing.T) {
	t.Parallel()
	stub := newOAuthStub(t)
	provider := newOAuthProvider(t, stub.URL)
	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("static-key"),
		OAuthKeyConfig: &schemas.OAuthKeyConfig{
			GrantType: schemas.OAuthGrantClientCredentials, TokenURL: *schemas.NewSecretVar(stub.URL + "/oauth/token"),
			ClientID: schemas.NewSecretVar("id"), ClientSecret: schemas.NewSecretVar("s"),
		},
	}
	if bErr := oauthChat(t, provider, key); bErr != nil {
		t.Fatalf("ChatCompletion: %v", bErr.Error.Message)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.tokenHits != 0 || len(stub.auths) != 1 || stub.auths[0] != "Bearer static-key" {
		t.Fatalf("mints=%d auths=%v, want no mint and the static bearer", stub.tokenHits, stub.auths)
	}
}

// TestOAuthKeyConfigRejectedCredential pins that a token-endpoint rejection comes back as a
// blocking error without an upstream call, and is answered from the negative cache after.
func TestOAuthKeyConfigRejectedCredential(t *testing.T) {
	t.Parallel()
	stub := newOAuthStub(t)
	stub.tokenStatus = http.StatusUnauthorized
	provider := newOAuthProvider(t, stub.URL)
	key := schemas.Key{
		Models: []string{"*"},
		OAuthKeyConfig: &schemas.OAuthKeyConfig{
			GrantType: schemas.OAuthGrantClientCredentials, TokenURL: *schemas.NewSecretVar(stub.URL + "/oauth/token"),
			ClientID: schemas.NewSecretVar("id"), ClientSecret: schemas.NewSecretVar("wrong"),
		},
	}
	for range 3 {
		bErr := oauthChat(t, provider, key)
		if bErr == nil {
			t.Fatal("expected the token rejection to fail the request")
		}
		if bErr.StatusCode == nil || *bErr.StatusCode != http.StatusUnauthorized {
			t.Fatalf("StatusCode = %v, want 401 from the token endpoint", bErr.StatusCode)
		}
		if bErr.AllowFallbacks == nil || *bErr.AllowFallbacks {
			t.Fatalf("AllowFallbacks = %v, want false", bErr.AllowFallbacks)
		}
		if !strings.Contains(bErr.Error.Message, "invalid_client unknown client") {
			t.Fatalf("message = %q", bErr.Error.Message)
		}
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.tokenHits != 1 {
		t.Fatalf("token hits = %d, want 1 (negative cache)", stub.tokenHits)
	}
	if len(stub.auths) != 0 {
		t.Fatalf("upstream was called %d times with no credential", len(stub.auths))
	}
}

// TestOAuthKeyConfigListModelsKeyStatuses pins that a key whose token cannot be minted still
// reports its own status from ListModels. The server persists these statuses as key health, so a
// key dropped before the per-key fan-out would keep showing its last healthy status.
func TestOAuthKeyConfigListModelsKeyStatuses(t *testing.T) {
	t.Parallel()
	good := newOAuthStub(t)
	bad := newOAuthStub(t)
	bad.tokenStatus = http.StatusUnauthorized
	provider := newOAuthProvider(t, good.URL)
	oauthKey := func(id, tokenURL string) schemas.Key {
		return schemas.Key{ID: id, Models: []string{"*"}, OAuthKeyConfig: &schemas.OAuthKeyConfig{
			GrantType: schemas.OAuthGrantClientCredentials, TokenURL: *schemas.NewSecretVar(tokenURL + "/oauth/token"),
			ClientID: schemas.NewSecretVar("id-" + id), ClientSecret: schemas.NewSecretVar("secret-" + id),
		}}
	}
	list := func(keys ...schemas.Key) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{Provider: "acme"})
	}
	statusOf := func(statuses []schemas.KeyStatus) map[string]schemas.KeyStatusType {
		m := map[string]schemas.KeyStatusType{}
		for _, s := range statuses {
			m[s.KeyID] = s.Status
		}
		return m
	}

	t.Run("a failed mint is reported next to the key that listed", func(t *testing.T) {
		resp, bErr := list(oauthKey("good", good.URL), oauthKey("bad", bad.URL))
		if bErr != nil {
			t.Fatalf("one usable key must be enough: %v", bErr.Error.Message)
		}
		got := statusOf(resp.KeyStatuses)
		if got["good"] != schemas.KeyStatusSuccess || got["bad"] != schemas.KeyStatusListModelsFailed {
			t.Fatalf("key statuses = %v, want good=success and bad=list_models_failed", got)
		}
	})

	t.Run("when every mint fails the error still carries each key's status", func(t *testing.T) {
		_, bErr := list(oauthKey("bad-1", bad.URL), oauthKey("bad-2", bad.URL))
		if bErr == nil {
			t.Fatal("expected an error when no key can be minted")
		}
		got := statusOf(bErr.ExtraFields.KeyStatuses)
		if got["bad-1"] != schemas.KeyStatusListModelsFailed || got["bad-2"] != schemas.KeyStatusListModelsFailed {
			t.Fatalf("error key statuses = %v, want both keys list_models_failed", got)
		}
	})
}

// TestOAuthKeyConfigMultiKeyOperationsMintLazily pins that the per-key loops behind multi-key
// operations (file, batch and container retrieval) mint a key's token only when the loop reaches
// that key. A working first key must answer without waiting on another key's token endpoint,
// which can take the whole exchange timeout when that endpoint is slow or down.
func TestOAuthKeyConfigMultiKeyOperationsMintLazily(t *testing.T) {
	t.Parallel()
	upstream := newOAuthStub(t)
	var slowHits atomic.Int64
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowHits.Add(1)
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(func() { close(release); slow.Close() })

	provider := newOAuthProvider(t, upstream.URL)
	static := schemas.Key{ID: "static", Models: []string{"*"}, Value: *schemas.NewSecretVar("sk-static")}
	cold := schemas.Key{ID: "cold", Models: []string{"*"}, OAuthKeyConfig: &schemas.OAuthKeyConfig{
		GrantType: schemas.OAuthGrantClientCredentials, TokenURL: *schemas.NewSecretVar(slow.URL + "/oauth/token"),
		ClientID: schemas.NewSecretVar("id"), ClientSecret: schemas.NewSecretVar("secret"),
	}}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	resp, bErr := provider.FileRetrieve(ctx, []schemas.Key{static, cold}, &schemas.BifrostFileRetrieveRequest{Provider: "acme", FileID: "file-1"})
	if bErr != nil {
		t.Fatalf("the static key should have served the request: %v", bErr.Error.Message)
	}
	if resp == nil || resp.ID != "file-1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if hits := slowHits.Load(); hits != 0 {
		t.Fatalf("the unused key's token endpoint was called %d times before the first key answered", hits)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("FileRetrieve took %s; it waited on an unused key", elapsed)
	}
}

// TestOAuthKeyConfigMultiKeyOperationsSkipAKeyThatCannotMint pins that one key whose token
// endpoint is down does not fail a multi-key operation: its mint failure is that key's error and
// the loop moves on to the next key. Only when no key works does an error surface.
func TestOAuthKeyConfigMultiKeyOperationsSkipAKeyThatCannotMint(t *testing.T) {
	t.Parallel()
	stub := newOAuthStub(t)
	provider := newOAuthProvider(t, stub.URL)
	oauthKey := func(id, tokenURL string) schemas.Key {
		return schemas.Key{ID: id, Models: []string{"*"}, OAuthKeyConfig: &schemas.OAuthKeyConfig{
			GrantType: schemas.OAuthGrantClientCredentials, TokenURL: *schemas.NewSecretVar(tokenURL),
			ClientID: schemas.NewSecretVar("id-" + id), ClientSecret: schemas.NewSecretVar("secret-" + id),
		}}
	}
	retrieve := func(keys ...schemas.Key) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return provider.FileRetrieve(ctx, keys, &schemas.BifrostFileRetrieveRequest{Provider: "acme", FileID: "file-1"})
	}

	resp, bErr := retrieve(oauthKey("down", "http://127.0.0.1:1/token"), oauthKey("up", stub.URL+"/oauth/token"))
	if bErr != nil {
		t.Fatalf("one key that cannot mint must not fail the operation: %v", bErr.Error.Message)
	}
	if resp == nil || resp.ID != "file-1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	stub.mu.Lock()
	auths := append([]string(nil), stub.auths...)
	stub.mu.Unlock()
	if len(auths) != 1 || auths[0] != "Bearer minted-token" {
		t.Fatalf("upstream authorizations = %v, want one request with the minted token", auths)
	}

	if _, bErr := retrieve(oauthKey("down-1", "http://127.0.0.1:1/token"), oauthKey("down-2", "http://127.0.0.1:1/token")); bErr == nil {
		t.Fatal("when no key can be minted the error must surface")
	}
}

// TestOAuthKeyConfigBlankValueMints pins that a whitespace-only value counts as no value, as it
// does in key validation and selection: the key mints from its oauth_key_config instead of
// sending a blank bearer upstream.
func TestOAuthKeyConfigBlankValueMints(t *testing.T) {
	t.Parallel()
	stub := newOAuthStub(t)
	provider := newOAuthProvider(t, stub.URL)
	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("   "),
		OAuthKeyConfig: &schemas.OAuthKeyConfig{
			GrantType: schemas.OAuthGrantClientCredentials, TokenURL: *schemas.NewSecretVar(stub.URL + "/oauth/token"),
			ClientID: schemas.NewSecretVar("id"), ClientSecret: schemas.NewSecretVar("secret"),
		},
	}
	if bErr := oauthChat(t, provider, key); bErr != nil {
		t.Fatalf("chat failed: %v", bErr.Error.Message)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.auths) != 1 || stub.auths[0] != "Bearer minted-token" {
		t.Fatalf("upstream authorizations = %q, want the minted token, not the blank value", stub.auths)
	}
}

// TestOAuthKeyConfigResolvesAfterOperationCheck pins the order at method entry: a disallowed
// operation is reported as not allowed, never as a token-mint failure, so an operator who
// turned chat off sees the real reason even when the identity provider is also down.
func TestOAuthKeyConfigResolvesAfterOperationCheck(t *testing.T) {
	t.Parallel()
	provider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "https://llm.example", DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType:  schemas.OpenAI,
			CustomProviderKey: "acme",
			AllowedRequests:   &schemas.AllowedRequests{Embedding: true},
		},
	}, oauthTestLogger{})
	key := schemas.Key{
		Models: []string{"*"},
		OAuthKeyConfig: &schemas.OAuthKeyConfig{
			GrantType: schemas.OAuthGrantClientCredentials, TokenURL: *schemas.NewSecretVar("http://127.0.0.1:1/token"),
			ClientID: schemas.NewSecretVar("id"), ClientSecret: schemas.NewSecretVar("s"),
		},
	}
	bErr := oauthChat(t, provider, key)
	if bErr == nil {
		t.Fatal("chat is not allowed on this provider")
	}
	if strings.Contains(bErr.Error.Message, "oauth token request") {
		t.Fatalf("the mint ran before the operation check: %q", bErr.Error.Message)
	}
	if !strings.Contains(strings.ToLower(bErr.Error.Message), "not allowed") && !strings.Contains(strings.ToLower(bErr.Error.Message), "not supported") {
		t.Fatalf("message = %q, want the operation-not-allowed error", bErr.Error.Message)
	}
}

// TestKeylessCustomProviderUnchanged pins that a key with neither value nor oauth_key_config
// still reaches the upstream with no Authorization header, as keyless custom providers rely on.
func TestKeylessCustomProviderUnchanged(t *testing.T) {
	t.Parallel()
	stub := newOAuthStub(t)
	provider := newOAuthProvider(t, stub.URL)
	if bErr := oauthChat(t, provider, schemas.Key{Models: []string{"*"}}); bErr != nil {
		t.Fatalf("ChatCompletion: %v", bErr.Error.Message)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.tokenHits != 0 || len(stub.auths) != 1 || stub.auths[0] != "" {
		t.Fatalf("mints=%d auths=%v, want no mint and no Authorization", stub.tokenHits, stub.auths)
	}
}

func TestOpenAI(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
		t.Skip("Skipping OpenAI tests because OPENAI_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:           schemas.OpenAI,
		ChatModel:          "gpt-4o",
		PromptCachingModel: "gpt-4.1",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o"},
		},
		VisionModel:        "gpt-4o",
		EmbeddingModel:     "text-embedding-3-small",
		TranscriptionModel: "gpt-4o-transcribe",
		TranscriptionFallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "whisper-1"},
		},
		SpeechSynthesisModel:    "gpt-4o-mini-tts",
		ReasoningModel:          "gpt-5-mini", // o4-mini was retired; gpt-5-mini returns both reasoning items and message output
		ImageGenerationModel:    "gpt-image-1.5",
		ImageEditModel:          "gpt-image-1.5",
		ImageVariationModel:     "", // dall-e-2 is deprecated and no other OpenAI model supports image variations
		VideoGenerationModel:    "sora-2",
		ChatAudioModel:          "gpt-audio-mini",
		PassthroughModel:        "gpt-4o",
		ExternalCompactionModel: "gpt-4o",
		DecisionEmulationModel:  "gpt-4o-mini",
		Scenarios: llmtests.TestScenarios{
			DecisionEmulation:          true,
			TextCompletion:             false,
			TextCompletionStream:       false,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			WebSearchTool:              true,
			ImageURL:                   true,
			ImageBase64:                true,
			MultipleImages:             true,
			FileBase64:                 true,
			FileURL:                    true,
			CompleteEnd2End:            true,
			SpeechSynthesis:            true,
			SpeechSynthesisStream:      true,
			Transcription:              true,
			TranscriptionStream:        true,
			Embedding:                  true,
			Reasoning:                  true,
			ListModels:                 true,
			ImageGeneration:            true,
			ImageGenerationStream:      true,
			ImageEdit:                  true,
			ImageEditStream:            true,
			ImageVariation:             false, // dall-e-2 is deprecated and no other OpenAI model supports image variations
			VideoGeneration:            false, // disabled for now because of long running operations
			VideoRetrieve:              false,
			VideoRemix:                 false,
			VideoDownload:              false,
			VideoList:                  false,
			VideoDelete:                false,
			BatchCreate:                true,
			BatchList:                  true,
			BatchRetrieve:              true,
			BatchCancel:                true,
			BatchResults:               true,
			FileUpload:                 true,
			FileList:                   true,
			FileRetrieve:               true,
			FileDelete:                 true,
			FileContent:                true,
			FileBatchInput:             true,
			CountTokens:                true,
			ResponsesLifecycle:         true,
			ExternalCompaction:         true,
			ChatAudio:                  true,
			StructuredOutputs:          true, // Structured outputs with nullable enum support
			ContainerCreate:            true,
			ContainerList:              true,
			ContainerRetrieve:          true,
			ContainerDelete:            true,
			ContainerFileCreate:        true,
			ContainerFileList:          true,
			ContainerFileRetrieve:      true,
			ContainerFileContent:       true,
			ContainerFileDelete:        true,
			PromptCaching:              true,
			PassthroughAPI:             true,
			WebSocketResponses:         true,
			Realtime:                   false,
		},
		RealtimeModel: "gpt-4o-realtime-preview",
	}

	t.Run("OpenAITests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// TestOpenAIDisableStoreForcesStoreFalse verifies that openai_config.disable_store forces store=false on chat and
// responses requests, including a passthrough extra_params store, without rewriting the caller's request that fallbacks share.
func TestOpenAIDisableStoreForcesStoreFalse(t *testing.T) {
	for _, api := range []string{"chat", "responses"} {
		for _, streaming := range []bool{false, true} {
			for _, mode := range []string{"typed", "passthrough"} {
				t.Run(fmt.Sprintf("%s/stream=%v/%s", api, streaming, mode), func(t *testing.T) {
					var body []byte
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, _ = io.ReadAll(r.Body)
						switch {
						case api == "chat" && streaming:
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
						case api == "chat":
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
						case streaming:
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"m\",\"output\":[]}}\n\n"))
						default:
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[]}`))
						}
					}))
					defer server.Close()

					provider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
						NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true},
						OpenAIConfig:  &schemas.OpenAIConfig{DisableStore: true},
					}, oauthTestLogger{})
					ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
					var extraParams map[string]interface{}
					if mode == "passthrough" {
						ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
						extraParams = map[string]interface{}{"store": true}
					}
					key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
					postHookRunner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
						return result, err
					}

					var callerStore *bool
					var callerExtra map[string]interface{}
					if api == "chat" {
						req := &schemas.BifrostChatRequest{
							Provider: schemas.OpenAI,
							Model:    "gpt-4o-mini",
							Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
							Params:   &schemas.ChatParameters{Store: schemas.Ptr(true), ExtraParams: extraParams},
						}
						if streaming {
							stream, bErr := provider.ChatCompletionStream(ctx, postHookRunner, nil, key, req)
							if bErr != nil {
								t.Fatalf("ChatCompletionStream returned error: %v", llmtests.GetErrorMessage(bErr))
							}
							for range stream {
							}
						} else if _, bErr := provider.ChatCompletion(ctx, key, req); bErr != nil {
							t.Fatalf("ChatCompletion returned error: %v", llmtests.GetErrorMessage(bErr))
						}
						callerStore, callerExtra = req.Params.Store, req.Params.ExtraParams
					} else {
						req := &schemas.BifrostResponsesRequest{
							Provider: schemas.OpenAI,
							Model:    "gpt-4o-mini",
							Input:    []schemas.ResponsesMessage{llmtests.CreateBasicResponsesMessage("hello")},
							Params:   &schemas.ResponsesParameters{Store: schemas.Ptr(true), ExtraParams: extraParams},
						}
						if streaming {
							stream, bErr := provider.ResponsesStream(ctx, postHookRunner, nil, key, req)
							if bErr != nil {
								t.Fatalf("ResponsesStream returned error: %v", llmtests.GetErrorMessage(bErr))
							}
							for range stream {
							}
						} else if _, bErr := provider.Responses(ctx, key, req); bErr != nil {
							t.Fatalf("Responses returned error: %v", llmtests.GetErrorMessage(bErr))
						}
						callerStore, callerExtra = req.Params.Store, req.Params.ExtraParams
					}

					if !strings.Contains(string(body), `"store":false`) || strings.Count(string(body), `"store":`) != 1 {
						t.Fatalf("expected upstream body to contain exactly one \"store\":false, got %s", body)
					}
					if !*callerStore {
						t.Fatalf("caller's Params.Store was rewritten to false")
					}
					if v, ok := callerExtra["store"]; ok && v != true {
						t.Fatalf("caller's ExtraParams store was rewritten to %v", v)
					}
				})
			}
		}
	}
}

func TestStreamingRequestBodyLifetime(t *testing.T) {
	for _, kind := range []string{"chat", "responses", "text"} {
		for _, capture := range []bool{false, true} {
			for _, cancelStream := range []bool{false, true} {
				// flag is the handler's sendBackRawRequest argument; the context key must win when they disagree.
				for _, flag := range []bool{capture, !capture} {
					t.Run(fmt.Sprintf("%s/capture=%v/flag=%v/cancel=%v", kind, capture, flag, cancelStream), func(t *testing.T) {
						release := make(chan struct{})
						var releaseOnce sync.Once
						upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							_, _ = io.Copy(io.Discard, r.Body)
							w.Header().Set("Content-Type", "text/event-stream")
							w.WriteHeader(http.StatusOK)
							w.(http.Flusher).Flush()
							select {
							case <-release:
							case <-r.Context().Done():
								return
							}
							switch kind {
							case "responses":
								_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"model\":\"gpt-4o\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
							case "text":
								_, _ = io.WriteString(w, "data: {\"id\":\"resp\",\"model\":\"gpt-4o\",\"choices\":[{\"text\":\"ok\",\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
							default:
								_, _ = io.WriteString(w, "data: {\"id\":\"resp\",\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
							}
						}))
						t.Cleanup(upstream.Close)
						t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
						ctx, cancel := schemas.NewBifrostContextWithTimeout(t.Context(), 5*time.Second)
						defer cancel()
						ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
						ctx.SetValue(schemas.BifrostContextKeyCaptureRawRequest, capture)
						client := &fasthttp.Client{StreamResponseBody: true}
						defer client.CloseIdleConnections()
						errors := make(chan *schemas.BifrostError, 1)
						hook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
							if err != nil {
								select {
								case errors <- err:
								default:
								}
							}
							return result, err
						}
						// Return only a weak pointer so this test does not itself keep the upload alive.
						var expectedRawHash [sha256.Size]byte
						start := func() (chan *schemas.BifrostStreamChunk, weak.Pointer[byte]) {
							body := []byte(`{"model":"gpt-4o","stream":true,"prompt":"` + strings.Repeat("x", 1<<20) + `"}`)
							expectedRawHash = sha256.Sum256(body)
							pointer := weak.Make(&body[0])
							var stream chan *schemas.BifrostStreamChunk
							var err *schemas.BifrostError
							switch kind {
							case "responses":
								stream, err = openai.HandleOpenAIResponsesStreaming(ctx, client, upstream.URL, &schemas.BifrostResponsesRequest{Model: "gpt-4o", RawRequestBody: body}, nil, nil, 2, flag, false, schemas.OpenAI, hook, nil, nil, nil, nil, nil, oauthTestLogger{}, func(context.Context) {})
							case "text":
								stream, err = openai.HandleOpenAITextCompletionStreaming(ctx, client, upstream.URL, &schemas.BifrostTextCompletionRequest{Model: "gpt-4o", RawRequestBody: body}, nil, nil, 2, flag, false, schemas.OpenAI, nil, hook, nil, nil, oauthTestLogger{}, func(context.Context) {})
							default:
								stream, err = openai.HandleOpenAIChatCompletionStreaming(ctx, client, upstream.URL, &schemas.BifrostChatRequest{Model: "gpt-4o", RawRequestBody: body}, nil, nil, 2, flag, false, schemas.OpenAI, hook, nil, nil, nil, nil, nil, nil, oauthTestLogger{}, func(context.Context) {})
							}
							if err != nil {
								t.Fatalf("stream setup: %v", err)
							}
							return stream, pointer
						}
						stream, pointer := start()
						// Chat and text register usage asynchronously; wait before replacing
						// their handle so cancellation cannot observe a later empty value.
						// Responses streams do not register a usage handle here.
						if kind != "responses" {
							deadline := time.NewTimer(time.Second)
							defer deadline.Stop()
							ticker := time.NewTicker(time.Millisecond)
							defer ticker.Stop()
							for ctx.Value(schemas.BifrostContextKeyStreamAccumulatedUsage) == nil {
								select {
								case <-deadline.C:
									t.Fatal("stream did not register accumulated usage")
								case <-ticker.C:
								}
							}
						}
						for range 5 {
							runtime.GC()
							runtime.Gosched()
						}
						if retained := pointer.Value() != nil; retained != capture {
							t.Errorf("upload retained during open stream=%v, want %v", retained, capture)
						}
						if cancelStream {
							ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, &schemas.BifrostLLMUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10})
							cancel()
						} else {
							releaseOnce.Do(func() { close(release) })
						}
						sawRaw := false
						checkRaw := func(raw any) {
							if raw == nil {
								return
							}
							sawRaw = true
							data, ok := raw.(json.RawMessage)
							if !ok || sha256.Sum256(data) != expectedRawHash {
								t.Fatal("terminal raw request changed or was truncated")
							}
						}
						for chunk := range stream {
							if chunk.BifrostError != nil && !cancelStream {
								t.Fatalf("stream error: %v", chunk.BifrostError)
							}
							if chunk.BifrostChatResponse != nil {
								checkRaw(chunk.BifrostChatResponse.ExtraFields.RawRequest)
							}
							if chunk.BifrostTextCompletionResponse != nil {
								checkRaw(chunk.BifrostTextCompletionResponse.ExtraFields.RawRequest)
							}
							if chunk.BifrostResponsesStreamResponse != nil {
								checkRaw(chunk.BifrostResponsesStreamResponse.ExtraFields.RawRequest)
							}
						}
						if cancelStream {
							select {
							case err := <-errors:
								checkRaw(err.ExtraFields.RawRequest)
								if err.ExtraFields.BilledUsage == nil || err.ExtraFields.BilledUsage.TotalTokens != 10 {
									t.Fatal("cancel lost billed usage")
								}
							case <-time.After(time.Second):
								t.Fatal("cancellation posthook never ran")
							}
						}
						if sawRaw != capture {
							t.Errorf("terminal raw request=%v, want %v", sawRaw, capture)
						}
					})
				}
			}
		}
	}
}

func BenchmarkOpenAIStreamSetup(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"resp\",\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := &fasthttp.Client{StreamResponseBody: true}
	defer client.CloseIdleConnections()
	body := []byte(`{"model":"gpt-4o","prompt":"` + strings.Repeat("x", 4096) + `"}`)
	hook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	b.ReportAllocs()
	for b.Loop() {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		stream, err := openai.HandleOpenAIChatCompletionStreaming(ctx, client, upstream.URL, &schemas.BifrostChatRequest{Model: "gpt-4o", RawRequestBody: body}, nil, nil, 2, false, false, schemas.OpenAI, hook, nil, nil, nil, nil, nil, nil, oauthTestLogger{}, func(context.Context) {})
		if err != nil {
			b.Fatal(err)
		}
		for range stream {
		}
	}
}

type streamHeapAccount struct {
	config schemas.ProviderConfig
}

func (a *streamHeapAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (a *streamHeapAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "heap-test", Value: *schemas.NewSecretVar("local-test"), Models: []string{"gpt-4o"}, Weight: 1}}, nil
}

func (a *streamHeapAccount) GetConfigForProvider(schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	config := a.config
	return &config, nil
}

// BenchmarkOpenAIConcurrentStreamHeap measures live heap, not allocation throughput.
// Run each selected leaf in a fresh process with -benchtime=1x. All uploads have
// finished and every stream has delivered content before the heap is sampled.
// The upstream keeps streams open with heartbeats until measurement is complete.
// BIFROST_HEAP_HOLD overrides the default 3s hold; it must be below 60s.
func BenchmarkOpenAIConcurrentStreamHeap(b *testing.B) {
	hold := 3 * time.Second
	if value := os.Getenv("BIFROST_HEAP_HOLD"); value != "" {
		var err error
		hold, err = time.ParseDuration(value)
		if err != nil || hold <= 0 || hold >= 60*time.Second {
			b.Fatal("BIFROST_HEAP_HOLD must be a positive duration below 60s")
		}
	}
	for _, path := range []string{"sdk-raw", "sdk-typed"} {
		for _, size := range []struct{ streams, mib int }{{32, 1}, {128, 1}, {32, 4}} {
			for _, capture := range []bool{false, true} {
				name := fmt.Sprintf("%s/%dx%dMiB/capture=%v", path, size.streams, size.mib, capture)
				b.Run(name, func(b *testing.B) {
					if b.N != 1 {
						b.Skip("use -benchtime=1x for a retained-heap measurement")
					}
					ctx, cancel := context.WithTimeout(b.Context(), 90*time.Second)
					defer cancel()
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if _, err := io.Copy(io.Discard, r.Body); err != nil {
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"id\":\"heap\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ready\"}}]}\n\n")
						w.(http.Flusher).Flush()
						ticker := time.NewTicker(200 * time.Millisecond)
						defer ticker.Stop()
						for {
							select {
							case <-ctx.Done():
								return
							case <-r.Context().Done():
								return
							case <-ticker.C:
								if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
									return
								}
								w.(http.Flusher).Flush()
							}
						}
					}))
					defer upstream.Close()
					defer cancel() // Close active streams before waiting for the server.
					gateway, err := bifrost.Init(ctx, schemas.BifrostConfig{
						Account: &streamHeapAccount{config: schemas.ProviderConfig{
							NetworkConfig: schemas.NetworkConfig{
								BaseURL: upstream.URL, AllowPrivateNetwork: true,
								StreamIdleTimeoutInSeconds: 60, MaxConnsPerHost: size.streams + 1,
							},
							ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 4, BufferSize: size.streams},
							SendBackRawRequest:       capture,
						}},
						Logger: oauthTestLogger{}, InitialPoolSize: 4,
					})
					if err != nil {
						b.Fatal(err)
					}
					defer gateway.Shutdown()
					var streams []chan *schemas.BifrostStreamChunk
					defer func() {
						cancel()
						for _, stream := range streams {
							for range stream {
							}
						}
					}()
					snapshot := func(label string) uint64 {
						runtime.GC()
						runtime.GC() // Also clear sync.Pool victim buffers from uploads.
						var stats runtime.MemStats
						runtime.ReadMemStats(&stats)
						if dir := os.Getenv("BIFROST_HEAP_PROFILE_DIR"); dir != "" {
							file, err := os.Create(filepath.Join(dir, label+".pprof"))
							if err != nil {
								b.Fatal(err)
							}
							err = pprof.WriteHeapProfile(file)
							closeErr := file.Close()
							if err != nil || closeErr != nil {
								b.Fatalf("heap profile: write=%v close=%v", err, closeErr)
							}
						}
						return stats.HeapAlloc
					}
					idle := snapshot("idle")
					// Keep request construction in a separate frame. The benchmark owns
					// only the returned channel, just as a caller that drops its request.
					start := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
						requestCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
						request := &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"}
						if path == "sdk-typed" {
							content := strings.Repeat("x", size.mib<<20)
							request.Input = []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &content}}}
						} else {
							request.RawRequestBody = []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"` + strings.Repeat("x", size.mib<<20) + `"}]}`)
							request.Input = []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("raw input")}}}
							requestCtx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
						}
						return gateway.ChatCompletionStreamRequest(requestCtx, request)
					}
					for range size.streams {
						stream, err := start()
						if err != nil {
							b.Fatalf("stream setup: %v", err)
						}
						streams = append(streams, stream)
						select {
						case chunk, ok := <-stream:
							if !ok || chunk == nil || chunk.BifrostError != nil {
								b.Fatalf("stream did not deliver initial content: %+v", chunk)
							}
						case <-ctx.Done():
							b.Fatal(ctx.Err())
						}
					}
					select {
					case <-time.After(hold):
					case <-ctx.Done():
						b.Fatal(ctx.Err())
					}
					active := snapshot("active")
					b.ReportMetric(float64(idle), "idle-heap-B")
					b.ReportMetric(float64(active), "active-heap-B")
					b.ReportMetric(float64(int64(active)-int64(idle)), "retained-heap-B")
					b.ReportMetric(float64(int64(active)-int64(idle))/float64(size.streams), "retained-B/stream")
				})
			}
		}
	}
}
