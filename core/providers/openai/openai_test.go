package openai_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/openai"

	"github.com/maximhq/bifrost/core/schemas"
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
		TextModel:          "gpt-3.5-turbo-instruct",
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
		ReasoningModel:          "o4-mini", // o4-mini properly returns both reasoning items and message output
		ImageGenerationModel:    "gpt-image-1",
		ImageEditModel:          "gpt-image-1",
		ImageVariationModel:     "", // dall-e-2 is deprecated and no other OpenAI model supports image variations
		VideoGenerationModel:    "sora-2",
		ChatAudioModel:          "gpt-audio-mini",
		PassthroughModel:        "gpt-4o",
		ExternalCompactionModel: "gpt-4o",
		DecisionEmulationModel:  "gpt-4o-mini",
		Scenarios: llmtests.TestScenarios{
			DecisionEmulation:          true,
			TextCompletion:             true,
			TextCompletionStream:       true,
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
