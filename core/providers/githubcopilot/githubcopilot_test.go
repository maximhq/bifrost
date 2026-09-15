package githubcopilot

import (
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

	t.Run("403 for the wrong credential layer says which token goes where", func(t *testing.T) {
		resp := newResponse(fasthttp.StatusForbidden, `{"message":"Resource not accessible by integration"}`)
		defer fasthttp.ReleaseResponse(resp)

		bErr := parseCopilotError(resp)

		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
		assert.Contains(t, bErr.Error.Message, "wrong credential")
	})

	t.Run("other 403s point at access and policy", func(t *testing.T) {
		resp := newResponse(fasthttp.StatusForbidden, `{"error":{"message":"model not allowed","type":"forbidden"}}`)
		defer fasthttp.ReleaseResponse(resp)

		bErr := parseCopilotError(resp)

		assert.Contains(t, bErr.Error.Message, "organization policy")
		assert.Contains(t, bErr.Error.Message, "model not allowed")
	})

	t.Run("429 leaves fallbacks alone", func(t *testing.T) {
		resp := newResponse(fasthttp.StatusTooManyRequests, `{"error":{"message":"slow down"}}`)
		defer fasthttp.ReleaseResponse(resp)

		bErr := parseCopilotError(resp)

		assert.Nil(t, bErr.AllowFallbacks, "a rate limit is transient, so fallbacks should still run")
		assert.Equal(t, "slow down", bErr.Error.Message)
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

	t.Run("embedding", func(t *testing.T) {
		_, bErr := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{})
		require.NotNil(t, bErr)
	})

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
