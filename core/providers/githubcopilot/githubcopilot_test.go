package githubcopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Compile-time proof that the provider satisfies the full Provider interface. Adding a
// method to the interface breaks this line rather than a distant call site.
var _ schemas.Provider = (*githubCopilotProvider)(nil)

func TestProviderConstructor(t *testing.T) {
	t.Run("leaves base URL empty so the credential decides the host", func(t *testing.T) {
		config := &schemas.ProviderConfig{}
		provider, err := NewGithubCopilotProvider(config, nil)
		require.NoError(t, err)

		assert.Equal(t, schemas.GithubCopilot, provider.GetProviderKey())
		assert.Empty(t, provider.networkConfig.BaseURL,
			"a default base URL here would pin every account to the public host")
		assert.NotNil(t, provider.streamingClient)
		assert.NotSame(t, provider.client, provider.streamingClient,
			"streams need their own client or they get killed at the unary read timeout")
	})

	t.Run("honours a configured base URL and strips the trailing slash", func(t *testing.T) {
		config := &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{BaseURL: "https://copilot.ghe.acme.com/"},
		}
		provider, err := NewGithubCopilotProvider(config, nil)
		require.NoError(t, err)

		assert.Equal(t, "https://copilot.ghe.acme.com", provider.networkConfig.BaseURL)
	})
}

func TestExchangeClient(t *testing.T) {
	provider, err := NewGithubCopilotProvider(&schemas.ProviderConfig{}, nil)
	require.NoError(t, err)

	t.Run("caps the response body at read time", func(t *testing.T) {
		// api.github.com is not supposed to return megabytes, and the cap is what stops a
		// misbehaving or intercepted upstream from being allocated in full.
		assert.Equal(t, maxExchangeBodyBytes, provider.exchangeClient.MaxResponseBodySize)
		assert.False(t, provider.exchangeClient.StreamResponseBody,
			"a credential exchange is a small unary call; streaming it would defeat the cap")
	})

	t.Run("inherits the dial policy rather than dialling raw", func(t *testing.T) {
		require.NotNil(t, provider.exchangeClient.Dial,
			"a nil Dial would mean the clone lost the configured policy and dials directly")
	})

	t.Run("rejects a private target on a direct connection", func(t *testing.T) {
		// This is the SSRF property the exchange client relies on, and it holds only for
		// direct connections: ConfigureDialer delegates to a proxy dialer when one is set,
		// and that path performs no address checks.
		_, err := provider.exchangeClient.Dial("10.0.0.1:443")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "private IP")
	})

	t.Run("rejects a link-local target on a direct connection", func(t *testing.T) {
		// 169.254.169.254 is the cloud metadata endpoint, the classic SSRF target.
		_, err := provider.exchangeClient.Dial("169.254.169.254:80")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "link-local")
	})
}

func TestResolveCredentials(t *testing.T) {
	t.Run("refuses a bare token with no base URL", func(t *testing.T) {
		// A Copilot API token does not carry its own host. Paid plans are served from
		// api.individual / api.business / api.enterprise, and only the token exchange
		// reveals which. Guessing the public host turns a Business token into a 401 that
		// reads like a bad credential, so refuse instead.
		key := schemas.Key{Value: *schemas.NewSecretVar("tid=abc;exp=123")}

		creds, bErr := resolveCredentials(nil, key, nil, "", nil)

		require.Nil(t, creds)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "base_url")
	})

	t.Run("a configured base URL wins", func(t *testing.T) {
		key := schemas.Key{Value: *schemas.NewSecretVar("tid=abc")}

		creds, bErr := resolveCredentials(nil, key, nil, "https://copilot.ghe.acme.com/", nil)

		require.Nil(t, bErr)
		assert.Equal(t, "https://copilot.ghe.acme.com", creds.BaseURL)
	})

	t.Run("refuses a plain http base URL unless it is loopback", func(t *testing.T) {
		for _, token := range []string{"tid=abc", "github_pat_x", "ghu_x"} {
			for _, baseURL := range []string{"http://copilot.example.com", "ftp://copilot.example.com", "copilot.example.com"} {
				key := schemas.Key{Value: *schemas.NewSecretVar(token)}

				creds, bErr := resolveCredentials(nil, key, nil, baseURL, nil)

				require.Nil(t, creds, token+" "+baseURL)
				require.NotNil(t, bErr, token+" "+baseURL)
				assert.Contains(t, bErr.Error.Message, "network_config.base_url", token+" "+baseURL)
			}
		}
		for baseURL, want := range map[string]string{
			"http://localhost:8080":  "http://localhost:8080",
			"http://127.0.0.1:9000/": "http://127.0.0.1:9000",
			"http://[::1]:9000":      "http://[::1]:9000",
		} {
			key := schemas.Key{Value: *schemas.NewSecretVar("tid=abc")}

			creds, bErr := resolveCredentials(nil, key, nil, baseURL, nil)

			require.Nil(t, bErr, baseURL)
			assert.Equal(t, want, creds.BaseURL)
		}
	})

	t.Run("names the unsupported GitHub token type instead of asking for base_url", func(t *testing.T) {
		for _, token := range []string{"ghp_classic", "ghs_installation"} {
			for _, baseURL := range []string{"", "https://api.business.githubcopilot.com"} {
				key := schemas.Key{Value: *schemas.NewSecretVar(token)}

				creds, bErr := resolveCredentials(nil, key, nil, baseURL, nil)

				require.Nil(t, creds, token)
				require.NotNil(t, bErr, token)
				assert.NotContains(t, bErr.Error.Message, "base_url", token)
				assert.Contains(t, bErr.Error.Message, "not supported", token)
			}
		}
	})

	t.Run("trims surrounding whitespace on the token", func(t *testing.T) {
		key := schemas.Key{Value: *schemas.NewSecretVar("  tid=abc\n")}

		creds, bErr := resolveCredentials(nil, key, nil, "https://api.business.githubcopilot.com", nil)

		require.Nil(t, bErr)
		assert.Equal(t, "tid=abc", creds.Token,
			"a stray newline from a pasted secret would otherwise corrupt the Authorization header")
	})

	t.Run("a whitespace-only base URL counts as unset", func(t *testing.T) {
		key := schemas.Key{Value: *schemas.NewSecretVar("tid=abc")}

		creds, bErr := resolveCredentials(nil, key, nil, "   ", nil)

		require.Nil(t, creds)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "base_url")
	})

	t.Run("a whitespace-only token is treated as absent", func(t *testing.T) {
		key := schemas.Key{Value: *schemas.NewSecretVar("   ")}

		creds, bErr := resolveCredentials(nil, key, nil, "https://api.githubcopilot.com", nil)

		require.Nil(t, creds)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "no credentials on this key")
	})

	t.Run("routes each kind of token", func(t *testing.T) {
		// A dial that always fails keeps this offline: reaching the exchange at all is the
		// proof that the token took the user-token path rather than the base_url refusal.
		offline := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return nil, errors.New("offline") }}
		tests := []struct {
			name string
			key  schemas.Key
			want string
		}{
			{"a ghu_ token goes to the exchange without base_url", schemas.Key{Value: *schemas.NewSecretVar("ghu_route_check")}, "could not reach GitHub"},
			{"a fine-grained PAT looks up its plan host instead of exchanging", schemas.Key{Value: *schemas.NewSecretVar("github_pat_route_check")}, "look up the Copilot plan"},
			{"a key with nothing names both ways to authenticate", schemas.Key{}, "sign in with GitHub"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, bErr := resolveCredentials(nil, tt.key, offline, "", nil)

				require.NotNil(t, bErr)
				assert.Contains(t, bErr.Error.Message, tt.want)
			})
		}
	})
}

// TestConfigurationErrorsBlockFallbacks pins that a Copilot setup fault never drains onto
// another provider. BifrostError.AllowFallbacks is nil-means-allowed, so a configuration
// error built without setting it explicitly is treated as retryable: a key with no
// credentials, or a token with no host to send it to, would quietly route the prompt to
// whatever fallback is configured, on someone else's bill, while the operator sees success.
func TestConfigurationErrorsBlockFallbacks(t *testing.T) {
	assertBlocked := func(t *testing.T, bErr *schemas.BifrostError) {
		t.Helper()
		require.NotNil(t, bErr)
		require.NotNil(t, bErr.AllowFallbacks, "a configuration fault must set AllowFallbacks explicitly")
		assert.False(t, *bErr.AllowFallbacks)
	}

	t.Run("no credentials on the key", func(t *testing.T) {
		_, bErr := resolveCredentials(nil, schemas.Key{}, nil, "https://api.githubcopilot.com", nil)
		assertBlocked(t, bErr)
	})

	t.Run("token with no base URL", func(t *testing.T) {
		_, bErr := resolveCredentials(nil, schemas.Key{Value: *schemas.NewSecretVar("tid=abc")}, nil, "", nil)
		assertBlocked(t, bErr)
	})
}

func TestBuildAuthHeaders(t *testing.T) {
	creds := &copilotCredentials{Token: "tid=abc", BaseURL: defaultCopilotAPIBaseURL}

	t.Run("carries the bearer token and the editor identity", func(t *testing.T) {
		headers := buildAuthHeaders(creds, false)

		assert.Equal(t, "Bearer tid=abc", headers["Authorization"])
		assert.Equal(t, copilotIntegrationID, headers["Copilot-Integration-Id"])
		assert.Equal(t, editorVersion, headers["Editor-Version"])
		assert.Equal(t, editorPluginVersion, headers["Editor-Plugin-Version"])
		assert.Equal(t, copilotUserAgent, headers["User-Agent"])
		assert.NotEmpty(t, headers["X-Request-Id"])
	})

	// Copilot answers a PAT sent as vscode-chat with "Personal Access Tokens are not supported".
	t.Run("a PAT goes out under the Copilot SDK's integration ID on every path", func(t *testing.T) {
		pat := &copilotCredentials{Token: "github_pat_abc", BaseURL: defaultCopilotAPIBaseURL, SDK: true}

		assert.Equal(t, "Bearer github_pat_abc", buildAuthHeaders(pat, false)["Authorization"])
		assert.Equal(t, sdkIntegrationID, buildAuthHeaders(pat, false)["Copilot-Integration-Id"])
		forPAT := (&githubCopilotProvider{}).mergeEditorHeaders(pat)["Copilot-Integration-Id"]
		forToken := (&githubCopilotProvider{}).mergeEditorHeaders(creds)["Copilot-Integration-Id"]
		assert.Equal(t, sdkIntegrationID, forPAT.GetValue(), "list models")
		assert.Equal(t, copilotIntegrationID, forToken.GetValue())
	})

	t.Run("gives every request a distinct request id", func(t *testing.T) {
		first := buildAuthHeaders(creds, false)["X-Request-Id"]
		second := buildAuthHeaders(creds, false)["X-Request-Id"]

		assert.NotEqual(t, first, second)
	})

	t.Run("sets the vision header only when the turn carries an image", func(t *testing.T) {
		_, present := buildAuthHeaders(creds, false)["Copilot-Vision-Request"]
		assert.False(t, present, "sending the vision header on a text turn is rejected by Copilot")

		assert.Equal(t, "true", buildAuthHeaders(creds, true)["Copilot-Vision-Request"])
	})
}

func TestChatRequestHasImageContent(t *testing.T) {
	text := "hello"
	textBlock := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: &text}
	imageBlock := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage}

	tests := []struct {
		name     string
		request  *schemas.BifrostChatRequest
		expected bool
	}{
		{"nil request", nil, false},
		{
			"plain string content",
			&schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				{Content: &schemas.ChatMessageContent{ContentStr: &text}},
			}},
			false,
		},
		{
			"text blocks only",
			&schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				{Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{textBlock}}},
			}},
			false,
		},
		{
			"image in the first message",
			&schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				{Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{imageBlock}}},
			}},
			true,
		},
		{
			"image in a later message after a nil content message",
			&schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				{Content: nil},
				{Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{textBlock, imageBlock}}},
			}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, chatRequestHasImageContent(tt.request))
		})
	}
}

func TestParseCopilotError(t *testing.T) {
	newResponse := func(status int, body string) *fasthttp.Response {
		resp := fasthttp.AcquireResponse()
		resp.SetStatusCode(status)
		resp.Header.SetContentType("application/json")
		resp.SetBodyString(body)
		return resp
	}

	t.Run("401 blocks fallbacks and names the cause", func(t *testing.T) {
		resp := newResponse(fasthttp.StatusUnauthorized, `{"message":"Bad credentials"}`)
		defer fasthttp.ReleaseResponse(resp)

		bErr := parseCopilotError(resp)

		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks,
			"draining a revoked-token failure onto a paid fallback key is the wrong outcome")
		assert.Contains(t, bErr.Error.Message, "expired or was revoked")
		assert.Contains(t, bErr.Error.Message, "Bad credentials")
	})

	t.Run("a 403 points at access and policy", func(t *testing.T) {
		resp := newResponse(fasthttp.StatusForbidden, `{"error":{"message":"model not allowed","type":"forbidden"}}`)
		defer fasthttp.ReleaseResponse(resp)

		bErr := parseCopilotError(resp)

		assert.Contains(t, bErr.Error.Message, "organization policy")
		assert.Contains(t, bErr.Error.Message, "model not allowed")
	})

	t.Run("a PAT refused for the Copilot CLI policy names the policy", func(t *testing.T) {
		resp := newResponse(fasthttp.StatusForbidden, `unauthorized: not authorized to use this Copilot feature`)
		defer fasthttp.ReleaseResponse(resp)

		bErr := parseCopilotError(resp)

		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
		assert.Contains(t, bErr.Error.Message, "Copilot CLI policy")
	})

	t.Run("429 leaves fallbacks alone", func(t *testing.T) {
		resp := newResponse(fasthttp.StatusTooManyRequests, `{"error":{"message":"slow down"}}`)
		defer fasthttp.ReleaseResponse(resp)

		bErr := parseCopilotError(resp)

		assert.Nil(t, bErr.AllowFallbacks, "a rate limit is transient, so fallbacks should still run")
		assert.Equal(t, "slow down", bErr.Error.Message)
	})

	t.Run("429 keeps the status and the retry hint", func(t *testing.T) {
		resp := newResponse(fasthttp.StatusTooManyRequests, `{"error":{"message":"slow down"}}`)
		defer fasthttp.ReleaseResponse(resp)
		resp.Header.Set("Retry-After", "7")

		bErr := parseCopilotError(resp)

		require.NotNil(t, bErr.StatusCode)
		assert.Equal(t, fasthttp.StatusTooManyRequests, *bErr.StatusCode)
		assert.Equal(t, int64(7000), bErr.ExtraFields.RetryAfter)
	})

	t.Run("an empty body still produces an actionable message", func(t *testing.T) {
		resp := newResponse(fasthttp.StatusUnauthorized, "")
		defer fasthttp.ReleaseResponse(resp)

		bErr := parseCopilotError(resp)

		assert.Contains(t, bErr.Error.Message, "github copilot:")
		assert.Contains(t, bErr.Error.Message, "(no detail)")
	})
}

func TestUnsupportedOperations(t *testing.T) {
	provider, err := NewGithubCopilotProvider(&schemas.ProviderConfig{}, nil)
	require.NoError(t, err)

	ctx := &schemas.BifrostContext{}
	key := schemas.Key{Value: *schemas.NewSecretVar("tid=abc")}

	t.Run("text completion", func(t *testing.T) {
		_, bErr := provider.TextCompletion(ctx, key, &schemas.BifrostTextCompletionRequest{})
		require.NotNil(t, bErr)
	})

	t.Run("batch create", func(t *testing.T) {
		_, bErr := provider.BatchCreate(ctx, key, &schemas.BifrostBatchCreateRequest{})
		require.NotNil(t, bErr)
	})

	t.Run("list models with no keys", func(t *testing.T) {
		_, bErr := provider.ListModels(ctx, nil, &schemas.BifrostListModelsRequest{})
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "no keys configured")
	})
}

// fakeCopilotAPI stands in for the Copilot inference host and records what each request
// carried, so routing tests can assert on the endpoint and the wire shape together.
type fakeCopilotAPI struct {
	server *httptest.Server
	status int

	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	path    string
	headers http.Header
	body    map[string]any
}

func newFakeCopilotAPI(t *testing.T) *fakeCopilotAPI {
	t.Helper()
	f := &fakeCopilotAPI{status: http.StatusOK}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{path: r.URL.Path, headers: r.Header.Clone(), body: body})
		f.mu.Unlock()

		if f.status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"token expired"}}`)
			return
		}
		stream, _ := body["stream"].(bool)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		switch {
		case r.URL.Path == "/chat/completions" && stream:
			_, _ = fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"+
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n"+
				"data: [DONE]\n\n")
		case r.URL.Path == "/chat/completions":
			_, _ = fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		case r.URL.Path == "/responses" && stream:
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"m\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
		case r.URL.Path == "/responses":
			_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		case r.URL.Path == "/v1/messages" && stream:
			_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case r.URL.Path == "/v1/messages":
			_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		case r.URL.Path == "/models":
			// Copilot's own shape: OpenAI's list envelope plus capabilities and supported_endpoints.
			_, _ = fmt.Fprint(w, `{"object":"list","data":[`+
				`{"id":"gpt-5.5","object":"model","vendor":"OpenAI","name":"GPT-5.5","version":"gpt-5.5","model_picker_enabled":true,"capabilities":{"type":"chat","family":"gpt-5.5","supports":{"streaming":true,"tool_calls":true}},"supported_endpoints":["/chat/completions","/responses"]},`+
				`{"id":"claude-sonnet-4.6","object":"model","vendor":"Anthropic","name":"Claude Sonnet 4.6","version":"claude-sonnet-4.6","model_picker_enabled":true,"capabilities":{"type":"chat","family":"claude-sonnet-4.6"},"supported_endpoints":["/chat/completions","/v1/messages"]},`+
				`{"id":"text-embedding-3-small","object":"model","vendor":"Azure OpenAI","name":"Embedding V3 small","version":"text-embedding-3-small","model_picker_enabled":false,"capabilities":{"type":"embeddings","family":"text-embedding-3-small"}}`+
				`]}`)
		case r.URL.Path == "/embeddings":
			_, _ = fmt.Fprint(w, `{"object":"list","model":"text-embedding-3-small","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeCopilotAPI) only(t *testing.T) recordedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.requests, 1, "expected exactly one upstream request")
	return f.requests[0]
}

// routeTestLogger silences every level the shared handlers may log at.
type routeTestLogger struct{ noopLogger }

func (routeTestLogger) Debug(string, ...any) {}
func (routeTestLogger) Info(string, ...any)  {}
func (routeTestLogger) Error(string, ...any) {}

func newRoutedProvider(t *testing.T, api *fakeCopilotAPI) (*githubCopilotProvider, *schemas.BifrostContext, schemas.Key) {
	t.Helper()
	provider, err := NewGithubCopilotProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: api.server.URL},
	}, routeTestLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	return provider, ctx, schemas.Key{Value: *schemas.NewSecretVar("tid=abc")}
}

func chatRequestFor(model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.GithubCopilot,
		Model:    model,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

func responsesRequestFor(model string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.GithubCopilot,
		Model:    model,
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

func passThroughPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

func drain(stream chan *schemas.BifrostStreamChunk) (chunks int, streamErr *schemas.BifrostError) {
	for chunk := range stream {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil {
			streamErr = chunk.BifrostError
		}
		chunks++
	}
	return chunks, streamErr
}

// assertEditorIdentity pins that every endpoint, including the Anthropic-shaped one, carries
// the bearer token and editor headers Copilot validates.
func assertEditorIdentity(t *testing.T, req recordedRequest) {
	t.Helper()
	assert.Equal(t, "Bearer tid=abc", req.headers.Get("Authorization"))
	assert.Equal(t, copilotIntegrationID, req.headers.Get("Copilot-Integration-Id"))
	assert.Equal(t, editorVersion, req.headers.Get("Editor-Version"))
	assert.Equal(t, copilotUserAgent, req.headers.Get("User-Agent"))
}

func TestChatCompletionRouting(t *testing.T) {
	t.Run("an OpenAI model uses chat completions", func(t *testing.T) {
		api := newFakeCopilotAPI(t)
		provider, ctx, key := newRoutedProvider(t, api)

		resp, bErr := provider.ChatCompletion(ctx, key, chatRequestFor("gpt-5.5"))

		require.Nil(t, bErr)
		require.NotNil(t, resp)
		req := api.only(t)
		assert.Equal(t, "/chat/completions", req.path)
		assertEditorIdentity(t, req)
	})

	t.Run("a Claude model uses the Anthropic messages endpoint", func(t *testing.T) {
		api := newFakeCopilotAPI(t)
		provider, ctx, key := newRoutedProvider(t, api)

		resp, bErr := provider.ChatCompletion(ctx, key, chatRequestFor("claude-sonnet-4.6"))

		require.Nil(t, bErr)
		require.NotEmpty(t, resp.Choices)
		assert.Equal(t, "hello", *resp.Choices[0].Message.Content.ContentStr)
		req := api.only(t)
		assert.Equal(t, "/v1/messages", req.path)
		assertEditorIdentity(t, req)
		assert.Contains(t, req.body, "max_tokens", "the Messages API rejects a body without max_tokens")
		assert.NotContains(t, req.body, "choices")
	})

	t.Run("a Claude stream uses the Anthropic messages endpoint", func(t *testing.T) {
		api := newFakeCopilotAPI(t)
		provider, ctx, key := newRoutedProvider(t, api)

		stream, bErr := provider.ChatCompletionStream(ctx, passThroughPostHook, nil, key, chatRequestFor("claude-sonnet-4.6"))
		require.Nil(t, bErr)
		chunks, streamErr := drain(stream)

		require.Nil(t, streamErr)
		assert.Positive(t, chunks)
		req := api.only(t)
		assert.Equal(t, "/v1/messages", req.path)
		assert.Equal(t, true, req.body["stream"])
	})
}

func TestResponsesRouting(t *testing.T) {
	tests := []struct {
		name  string
		model string
		path  string
	}{
		// gpt-5.x-codex models are served on /responses only, so converting through chat
		// fails for them outright.
		{"an OpenAI model goes native", "gpt-5.3-codex", "/responses"},
		{"a Claude model uses the Anthropic messages endpoint", "claude-opus-4.6", "/v1/messages"},
		{"a chat-only model converts through chat completions", "gemini-3-pro-preview", "/chat/completions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeCopilotAPI(t)
			provider, ctx, key := newRoutedProvider(t, api)

			resp, bErr := provider.Responses(ctx, key, responsesRequestFor(tt.model))

			require.Nil(t, bErr)
			require.NotNil(t, resp)
			require.NotEmpty(t, resp.Output)
			req := api.only(t)
			assert.Equal(t, tt.path, req.path)
			assertEditorIdentity(t, req)
		})

		t.Run(tt.name+" when streaming", func(t *testing.T) {
			api := newFakeCopilotAPI(t)
			provider, ctx, key := newRoutedProvider(t, api)

			stream, bErr := provider.ResponsesStream(ctx, passThroughPostHook, nil, key, responsesRequestFor(tt.model))
			require.Nil(t, bErr)
			chunks, streamErr := drain(stream)

			require.Nil(t, streamErr)
			assert.Positive(t, chunks)
			req := api.only(t)
			assert.Equal(t, tt.path, req.path)
			assert.Equal(t, true, req.body["stream"])
		})
	}

	t.Run("an image turn carries the vision header on the native endpoint", func(t *testing.T) {
		api := newFakeCopilotAPI(t)
		provider, ctx, key := newRoutedProvider(t, api)
		request := responsesRequestFor("gpt-5.5")
		request.Input[0].Content = &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
			Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
				ImageURL: schemas.Ptr("https://example.com/cat.png"),
			},
		}}}

		_, bErr := provider.Responses(ctx, key, request)

		require.Nil(t, bErr)
		assert.Equal(t, "true", api.only(t).headers.Get("Copilot-Vision-Request"))
	})
}

func TestMessagesEndpointAuthErrorsBlockFallbacks(t *testing.T) {
	// The shared Anthropic handlers parse errors themselves, so the Copilot-specific 401
	// handling has to be applied on the way out or a revoked token drains onto a fallback.
	api := newFakeCopilotAPI(t)
	api.status = http.StatusUnauthorized
	provider, ctx, key := newRoutedProvider(t, api)

	_, bErr := provider.ChatCompletion(ctx, key, chatRequestFor("claude-sonnet-4.6"))

	require.NotNil(t, bErr)
	require.NotNil(t, bErr.AllowFallbacks)
	assert.False(t, *bErr.AllowFallbacks)
	assert.Contains(t, bErr.Error.Message, "expired or was revoked")
	assert.Contains(t, bErr.Error.Message, "token expired")
}

func TestListModels(t *testing.T) {
	api := newFakeCopilotAPI(t)
	provider, ctx, key := newRoutedProvider(t, api)
	key.Models = []string{"*"}

	resp, bErr := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.GithubCopilot})

	require.Nil(t, bErr)
	ids := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
	}
	assert.ElementsMatch(t, []string{
		"github-copilot/gpt-5.5",
		"github-copilot/claude-sonnet-4.6",
		"github-copilot/text-embedding-3-small",
	}, ids)
	req := api.only(t)
	assert.Equal(t, "/models", req.path)
	assertEditorIdentity(t, req)
}

// TestListModelsReportsEveryKeyStatus pins that each key is listed with its own credentials
// and gets its own status; listing only the first key left every key's status unknown.
func TestListModelsReportsEveryKeyStatus(t *testing.T) {
	api := newFakeCopilotAPI(t)
	provider, ctx, good := newRoutedProvider(t, api)
	good.ID, good.Models = "good", []string{"*"}
	bad := schemas.Key{ID: "bad", Models: []string{"*"}}

	resp, bErr := provider.ListModels(ctx, []schemas.Key{bad, good}, &schemas.BifrostListModelsRequest{Provider: schemas.GithubCopilot})

	require.Nil(t, bErr)
	require.NotEmpty(t, resp.Data)
	statuses := map[string]schemas.KeyStatusType{}
	for _, ks := range resp.KeyStatuses {
		statuses[ks.KeyID] = ks.Status
	}
	assert.Equal(t, map[string]schemas.KeyStatusType{
		"good": schemas.KeyStatusSuccess,
		"bad":  schemas.KeyStatusListModelsFailed,
	}, statuses)
}

func TestEmbedding(t *testing.T) {
	api := newFakeCopilotAPI(t)
	provider, ctx, key := newRoutedProvider(t, api)

	resp, bErr := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{
		Provider: schemas.GithubCopilot,
		Model:    "text-embedding-3-small",
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("hello")}}},
		},
	})

	require.Nil(t, bErr)
	require.Len(t, resp.Data, 1)
	req := api.only(t)
	assert.Equal(t, "/embeddings", req.path)
	assertEditorIdentity(t, req)
	assert.Equal(t, "text-embedding-3-small", req.body["model"])
	// Copilot rejects a bare string input with 400, so a single input still goes out as an array.
	assert.Equal(t, []any{"hello"}, req.body["input"])
}

func TestResponsesRequestHasImageContent(t *testing.T) {
	text := responsesRequestFor("gpt-5.5")
	assert.False(t, responsesRequestHasImageContent(nil))
	assert.False(t, responsesRequestHasImageContent(text))

	image := responsesRequestFor("gpt-5.5")
	image.Input = append(image.Input, schemas.ResponsesMessage{Content: &schemas.ResponsesMessageContent{
		ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesInputMessageContentBlockTypeImage}},
	}})
	assert.True(t, responsesRequestHasImageContent(image))
}

// BenchmarkRouteSelection measures the per-request cost the endpoint routing adds in front
// of every Chat and Responses call: one family check and one capability lookup.
func BenchmarkRouteSelection(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for _, model := range []string{"gpt-5.5", "claude-sonnet-4.6", "gemini-3-pro-preview"} {
		b.Run(model, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if !schemas.IsAnthropicModelFamily(ctx, model) {
					canonical := schemas.ResolveCanonicalModel(ctx, model)
					_ = schemas.ResolveModelCaps(schemas.GithubCopilot, canonical).SupportsResponsesEndpoint(schemas.IsOpenAIModelFamily(ctx, canonical))
				}
			}
		})
	}
}
