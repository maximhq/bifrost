package nadir

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// bucketServer stands in for Nadir's /v1/bucket, recording what the plugin sent.
type bucketServer struct {
	*httptest.Server
	bucket     string
	status     int
	delay      time.Duration
	lastKey    string
	lastBody   map[string]any
	callCount  int
	rawPayload string
}

func newBucketServer(t *testing.T, bucket string) *bucketServer {
	t.Helper()
	s := &bucketServer{bucket: bucket, status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.callCount++
		s.lastKey = r.Header.Get("X-API-Key")
		body, _ := io.ReadAll(r.Body)
		s.rawPayload = string(body)
		_ = json.Unmarshal(body, &s.lastBody)
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		if s.status == http.StatusOK {
			_, _ = w.Write([]byte(`{"bucket":"` + s.bucket + `","confidence":0.9}`))
		} else {
			_, _ = w.Write([]byte(`{"detail":"nope"}`))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func testConfig(baseURL string) *Config {
	return &Config{
		BaseURL: baseURL,
		Tiers: map[string]string{
			"simple":  "openai/gpt-4o-mini",
			"medium":  "openai/gpt-4o",
			"complex": "anthropic/claude-sonnet-4-5",
		},
		FallbackModel: "openai/gpt-4o",
	}
}

func newPlugin(t *testing.T, cfg *Config) *Plugin {
	t.Helper()
	p, err := Init(cfg, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

func chatRequest(model string, messages ...schemas.ChatMessage) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Model: model, Input: messages},
	}
}

func userMessage(text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &text}}
}

func testContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func routedTo(t *testing.T, req *schemas.BifrostRequest) string {
	t.Helper()
	provider, model, _ := req.GetRequestFields()
	return string(provider) + "/" + model
}

func TestBucketRoutesToItsTier(t *testing.T) {
	for bucket, want := range map[string]string{
		"simple":  "openai/gpt-4o-mini",
		"medium":  "openai/gpt-4o",
		"complex": "anthropic/claude-sonnet-4-5",
	} {
		t.Run(bucket, func(t *testing.T) {
			server := newBucketServer(t, bucket)
			plugin := newPlugin(t, testConfig(server.URL))
			req := chatRequest(DefaultTriggerModel, userMessage("refactor the retry loop"))

			if err := plugin.PreRequestHook(testContext(t), req); err != nil {
				t.Fatalf("PreRequestHook: %v", err)
			}
			if got := routedTo(t, req); got != want {
				t.Fatalf("routed to %s, want %s", got, want)
			}
			if server.lastBody["source"] != "bifrost" {
				t.Fatalf("source = %v, want bifrost", server.lastBody["source"])
			}
		})
	}
}

func TestAModelThatIsNotTheTriggerIsLeftAlone(t *testing.T) {
	server := newBucketServer(t, "complex")
	plugin := newPlugin(t, testConfig(server.URL))
	req := chatRequest("gpt-4o-mini", userMessage("hi"))

	if err := plugin.PreRequestHook(testContext(t), req); err != nil {
		t.Fatalf("PreRequestHook: %v", err)
	}
	if got := routedTo(t, req); got != "/gpt-4o-mini" {
		t.Fatalf("request was rewritten to %s; a named model must survive untouched", got)
	}
	if server.callCount != 0 {
		t.Fatalf("classified %d times; a request that did not ask to be routed must not be sent to Nadir", server.callCount)
	}
}

func TestAProviderPrefixedTriggerStillRoutes(t *testing.T) {
	server := newBucketServer(t, "simple")
	plugin := newPlugin(t, testConfig(server.URL))
	req := chatRequest("openai/"+DefaultTriggerModel, userMessage("hi"))

	if err := plugin.PreRequestHook(testContext(t), req); err != nil {
		t.Fatalf("PreRequestHook: %v", err)
	}
	if got := routedTo(t, req); got != "openai/gpt-4o-mini" {
		t.Fatalf("routed to %s, want openai/gpt-4o-mini", got)
	}
}

// Everything a broken classifier can do lands on fallback_model, never on the trigger name,
// which no provider serves.
func TestClassifierFailuresRouteToFallback(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		server := newBucketServer(t, "simple")
		server.status = http.StatusTooManyRequests
		plugin := newPlugin(t, testConfig(server.URL))
		req := chatRequest(DefaultTriggerModel, userMessage("hi"))

		_ = plugin.PreRequestHook(testContext(t), req)
		if got := routedTo(t, req); got != "openai/gpt-4o" {
			t.Fatalf("routed to %s, want the fallback openai/gpt-4o", got)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := newBucketServer(t, "complex")
		server.delay = 200 * time.Millisecond
		cfg := testConfig(server.URL)
		cfg.TimeoutMs = 20
		plugin := newPlugin(t, cfg)
		req := chatRequest(DefaultTriggerModel, userMessage("hi"))

		_ = plugin.PreRequestHook(testContext(t), req)
		if got := routedTo(t, req); got != "openai/gpt-4o" {
			t.Fatalf("routed to %s, want the fallback openai/gpt-4o", got)
		}
	})

	t.Run("unreachable host", func(t *testing.T) {
		server := newBucketServer(t, "simple")
		plugin := newPlugin(t, testConfig(server.URL))
		server.Close()
		req := chatRequest(DefaultTriggerModel, userMessage("hi"))

		_ = plugin.PreRequestHook(testContext(t), req)
		if got := routedTo(t, req); got != "openai/gpt-4o" {
			t.Fatalf("routed to %s, want the fallback openai/gpt-4o", got)
		}
	})

	t.Run("bucket with no tier configured", func(t *testing.T) {
		server := newBucketServer(t, "reasoning")
		plugin := newPlugin(t, testConfig(server.URL))
		req := chatRequest(DefaultTriggerModel, userMessage("hi"))

		_ = plugin.PreRequestHook(testContext(t), req)
		if got := routedTo(t, req); got != "openai/gpt-4o" {
			t.Fatalf("routed to %s, want the fallback openai/gpt-4o", got)
		}
	})
}

func TestARequestWithNoTextIsNotSentToNadir(t *testing.T) {
	server := newBucketServer(t, "simple")
	plugin := newPlugin(t, testConfig(server.URL))
	req := chatRequest(DefaultTriggerModel)

	_ = plugin.PreRequestHook(testContext(t), req)
	if server.callCount != 0 {
		t.Fatalf("classified %d times; an empty request is a 400 from the endpoint, spend no round trip on it", server.callCount)
	}
	if got := routedTo(t, req); got != "openai/gpt-4o" {
		t.Fatalf("routed to %s, want the fallback openai/gpt-4o", got)
	}
}

func TestPassthroughRequestsAreLeftAlone(t *testing.T) {
	server := newBucketServer(t, "complex")
	plugin := newPlugin(t, testConfig(server.URL))
	req := chatRequest(DefaultTriggerModel, userMessage("hi"))
	req.RequestType = schemas.PassthroughRequest

	_ = plugin.PreRequestHook(testContext(t), req)
	if server.callCount != 0 {
		t.Fatalf("classified a passthrough request %d times", server.callCount)
	}
}

func TestAPIKeyIsSentOnlyWhenConfigured(t *testing.T) {
	server := newBucketServer(t, "simple")
	cfg := testConfig(server.URL)
	cfg.APIKey = "ndr_test"
	plugin := newPlugin(t, cfg)
	_ = plugin.PreRequestHook(testContext(t), chatRequest(DefaultTriggerModel, userMessage("hi")))
	if server.lastKey != "ndr_test" {
		t.Fatalf("X-API-Key = %q, want ndr_test", server.lastKey)
	}

	t.Setenv("NADIR_API_KEY", "")
	anonymous := newPlugin(t, testConfig(server.URL))
	_ = anonymous.PreRequestHook(testContext(t), chatRequest(DefaultTriggerModel, userMessage("hi")))
	if server.lastKey != "" {
		t.Fatalf("X-API-Key = %q, want it absent when no key is configured", server.lastKey)
	}
}

func TestEnvironmentSuppliesKeyAndBase(t *testing.T) {
	server := newBucketServer(t, "simple")
	t.Setenv("NADIR_API_KEY", "ndr_from_env")
	t.Setenv("NADIR_API_BASE", server.URL)
	cfg := testConfig("")
	plugin := newPlugin(t, cfg)

	_ = plugin.PreRequestHook(testContext(t), chatRequest(DefaultTriggerModel, userMessage("hi")))
	if server.lastKey != "ndr_from_env" {
		t.Fatalf("X-API-Key = %q, want ndr_from_env", server.lastKey)
	}
	if server.callCount != 1 {
		t.Fatalf("classified %d times, want 1 against the env base URL", server.callCount)
	}
}

func TestTextIsExtractedFromEveryShapeItArrivesIn(t *testing.T) {
	block := "explain this diff"
	cases := map[string]struct {
		req  *schemas.BifrostRequest
		want string
	}{
		"content string": {
			req:  chatRequest(DefaultTriggerModel, userMessage("explain this diff")),
			want: "explain this diff",
		},
		"content blocks": {
			req: chatRequest(DefaultTriggerModel, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: &block},
				}},
			}),
			want: "explain this diff",
		},
		"responses api content string": {
			req: &schemas.BifrostRequest{
				RequestType: schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{
					Model: DefaultTriggerModel,
					Input: []schemas.ResponsesMessage{{
						Role:    responsesRole(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{ContentStr: &block},
					}},
				},
			},
			want: "explain this diff",
		},
		"responses api content blocks": {
			req: &schemas.BifrostRequest{
				RequestType: schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{
					Model: DefaultTriggerModel,
					Input: []schemas.ResponsesMessage{{
						Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: &block},
						}},
					}},
				},
			},
			want: "explain this diff",
		},
		"text completion prompt": {
			req: &schemas.BifrostRequest{
				RequestType: schemas.TextCompletionRequest,
				TextCompletionRequest: &schemas.BifrostTextCompletionRequest{
					Model: DefaultTriggerModel,
					Input: &schemas.TextCompletionInput{PromptStr: &block},
				},
			},
			want: "explain this diff",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := newBucketServer(t, "medium")
			plugin := newPlugin(t, testConfig(server.URL))

			_ = plugin.PreRequestHook(testContext(t), tc.req)
			if server.callCount != 1 {
				t.Fatalf("classified %d times, want 1", server.callCount)
			}
			messages, _ := server.lastBody["messages"].([]any)
			if len(messages) != 1 {
				t.Fatalf("sent %d messages, want 1: %s", len(messages), server.rawPayload)
			}
			first, _ := messages[0].(map[string]any)
			if first["content"] != tc.want {
				t.Fatalf("content = %v, want %q", first["content"], tc.want)
			}
		})
	}
}

// Nadir's own docs advertise the base URL with /v1 on it, so operators paste it both ways.
func TestBucketURLNeverDoublesV1(t *testing.T) {
	for _, base := range []string{
		"https://api.getnadir.com",
		"https://api.getnadir.com/",
		"https://api.getnadir.com/v1",
		"https://api.getnadir.com/v1/",
	} {
		if got := bucketURL(base); got != "https://api.getnadir.com/v1/bucket" {
			t.Fatalf("bucketURL(%q) = %q", base, got)
		}
	}
	if got := bucketURL("https://gateway.internal/nadir"); got != "https://gateway.internal/nadir/v1/bucket" {
		t.Fatalf("a host with a path prefix was not preserved: %q", got)
	}
}

// Config mistakes surface at boot, where the operator can read them, rather than as a
// request-time failure indistinguishable from Nadir being down.
func TestInitRejectsUnusableConfig(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	cases := map[string]*Config{
		"nil tiers":           {FallbackModel: "openai/gpt-4o"},
		"no fallback":         {Tiers: map[string]string{"simple": "openai/gpt-4o-mini"}},
		"unprefixed tier":     {Tiers: map[string]string{"simple": "gpt-4o-mini"}, FallbackModel: "openai/gpt-4o"},
		"unknown provider":    {Tiers: map[string]string{"simple": "notaprovider/x"}, FallbackModel: "openai/gpt-4o"},
		"unprefixed fallback": {Tiers: map[string]string{"simple": "openai/gpt-4o-mini"}, FallbackModel: "gpt-4o"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Init(cfg, logger); err == nil {
				t.Fatal("Init accepted a config it cannot serve")
			}
		})
	}
	if _, err := Init(nil, logger); err == nil {
		t.Fatal("Init accepted a nil config")
	}
}

func TestBucketNameMatchingIsCaseAndSpaceInsensitive(t *testing.T) {
	server := newBucketServer(t, " Complex ")
	plugin := newPlugin(t, testConfig(server.URL))
	req := chatRequest(DefaultTriggerModel, userMessage("hi"))

	_ = plugin.PreRequestHook(testContext(t), req)
	if got := routedTo(t, req); got != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("routed to %s, want anthropic/claude-sonnet-4-5", got)
	}
}

func responsesRole(role schemas.ResponsesMessageRoleType) *schemas.ResponsesMessageRoleType {
	return &role
}

// A /v1/responses request carrying the trigger model reaches PreRequestHook the same way a
// chat request does, so it must be classified rather than dropped on fallback_model.
func TestResponsesRequestsAreClassified(t *testing.T) {
	server := newBucketServer(t, "complex")
	plugin := newPlugin(t, testConfig(server.URL))
	text := "port the scheduler to the new executor and prove it terminates"
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Model: DefaultTriggerModel,
			Input: []schemas.ResponsesMessage{{
				Role:    responsesRole(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: &text},
			}},
		},
	}

	if err := plugin.PreRequestHook(testContext(t), req); err != nil {
		t.Fatalf("PreRequestHook: %v", err)
	}
	if server.callCount != 1 {
		t.Fatalf("classified %d times, want 1", server.callCount)
	}
	messages, _ := server.lastBody["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("sent %d messages, want 1: %s", len(messages), server.rawPayload)
	}
	if first, _ := messages[0].(map[string]any); first["content"] != text {
		t.Fatalf("content = %v, want %q", first["content"], text)
	}
	if got := routedTo(t, req); got != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("routed to %s, want the complex tier, not the fallback", got)
	}
}

// A bucket name Nadir never returns can only ever route to fallback_model, so the tier it
// configures would be silently unreachable. Rejected at Init instead.
func TestInitRejectsATierNadirNeverReturns(t *testing.T) {
	_, err := Init(&Config{
		Tiers:         map[string]string{"simlpe": "openai/gpt-4o-mini"},
		FallbackModel: "openai/gpt-4o",
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err == nil {
		t.Fatal("Init accepted a tier key Nadir never returns")
	}
}
