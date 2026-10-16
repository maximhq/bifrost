package bedrock

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturedPassthroughRequest is what the fake AWS endpoint saw.
type capturedPassthroughRequest struct {
	Method, Path, RawQuery, Body string
	Header                       http.Header
}

type passthroughTarget struct {
	server   *httptest.Server
	provider *BedrockProvider
	mu       sync.Mutex
	seen     []capturedPassthroughRequest
}

func (p *passthroughTarget) requests() []capturedPassthroughRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]capturedPassthroughRequest(nil), p.seen...)
}

// newPassthroughTarget stands in for both bedrock-agent-runtime and bedrock-runtime: the key's
// endpoint overrides send each service to the same local TLS server.
func newPassthroughTarget(t *testing.T, status int, respBody string, respHeader map[string]string) *passthroughTarget {
	t.Helper()
	target := &passthroughTarget{}
	target.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		target.mu.Lock()
		target.seen = append(target.seen, capturedPassthroughRequest{Method: r.Method, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery, Body: string(body), Header: r.Header.Clone()})
		target.mu.Unlock()
		for k, v := range respHeader {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(target.server.Close)
	// The production constructor always sets both clients; the event-stream operation uses the streaming one.
	target.provider = &BedrockProvider{client: target.server.Client(), streamingClient: providerUtils.BuildStreamingHTTPClient(target.server.Client()), logger: &passthroughTestLogger{}}
	return target
}

func (p *passthroughTarget) key(apiKey string) schemas.Key {
	host := strings.TrimPrefix(p.server.URL, "https://")
	k := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
		AccessKey: *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE"),
		SecretKey: *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
		Region:    schemas.NewSecretVar("us-west-2"),
		Endpoints: &schemas.BedrockEndpoints{AgentRuntime: schemas.NewSecretVar(host), Runtime: schemas.NewSecretVar(host)},
	}}
	if apiKey != "" {
		k.Value = *schemas.NewSecretVar(apiKey)
	}
	return k
}

func passthroughCtx(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	return ctx
}

// The three Bedrock operations that have no native Bifrost route are reachable through the
// bedrock passthrough: Bifrost signs the request with the key's credentials, sends it to the
// service host for the key's region, and returns the upstream answer untouched.
func TestBedrockPassthroughSignsAndForwardsSupportedOperations(t *testing.T) {
	cases := []struct {
		name, path, body string
	}{
		{"InvokeAgent", "/agents/AGENT123456/agentAliases/ALIAS12345/sessions/session-1/text", `{"inputText":"hello"}`},
		{"Retrieve", "/knowledgebases/KB12345678/retrieve", `{"retrievalQuery":{"text":"what is bifrost"}}`},
		{"ApplyGuardrail", "/guardrail/gr1a2b3c4d5e/version/DRAFT/apply", `{"source":"INPUT","content":[{"text":{"text":"hi"}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := newPassthroughTarget(t, http.StatusOK, `{"ok":true}`, map[string]string{"Content-Type": "application/json", "X-Amzn-Requestid": "req-1"})
			resp, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.key(""), &schemas.BifrostPassthroughRequest{
				Method:      http.MethodPost,
				Path:        tc.path,
				Body:        []byte(tc.body),
				SafeHeaders: map[string]string{"Content-Type": "application/json"},
			})
			require.Nil(t, bifrostErr, "supported operation must not be refused")
			require.NotNil(t, resp)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.JSONEq(t, `{"ok":true}`, string(resp.Body))
			assert.Equal(t, "application/json", resp.Headers["Content-Type"], "the upstream content type must reach the caller so SDKs can decode the body")
			assert.Equal(t, "req-1", resp.Headers["X-Amzn-Requestid"])

			got := target.requests()
			require.Len(t, got, 1)
			assert.Equal(t, http.MethodPost, got[0].Method)
			assert.Equal(t, tc.path, got[0].Path)
			assert.Equal(t, tc.body, got[0].Body)
			assert.Equal(t, "application/json", got[0].Header.Get("Content-Type"))
			auth := got[0].Header.Get("Authorization")
			assert.Contains(t, auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/", "request must be SigV4 signed")
			assert.Contains(t, auth, "/us-west-2/bedrock/aws4_request", "scope follows the key's region and the bedrock signing name")
			assert.NotEmpty(t, got[0].Header.Get("X-Amz-Date"))
		})
	}
}

// An upstream error is the answer to the caller's call, not a gateway failure: the status, headers
// and body come back as AWS sent them so SDKs can read the exception type.
func TestBedrockPassthroughForwardsUpstreamErrorsVerbatim(t *testing.T) {
	target := newPassthroughTarget(t, http.StatusNotFound, `{"message":"agent not found"}`, map[string]string{"X-Amzn-Errortype": "ResourceNotFoundException", "Content-Type": "application/json"})
	resp, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.key(""), &schemas.BifrostPassthroughRequest{
		Method: http.MethodPost,
		Path:   "/agents/AGENT123456/agentAliases/ALIAS12345/sessions/s/text",
		Body:   []byte(`{}`),
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.JSONEq(t, `{"message":"agent not found"}`, string(resp.Body))
	assert.Equal(t, "ResourceNotFoundException", resp.Headers["X-Amzn-Errortype"])
}

// An API-key-only key: it holds a Bedrock API key and endpoint overrides but no AWS credentials.
func (p *passthroughTarget) apiKeyOnlyKey(apiKey string) schemas.Key {
	host := strings.TrimPrefix(p.server.URL, "https://")
	return schemas.Key{
		Value: *schemas.NewSecretVar(apiKey),
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region:    schemas.NewSecretVar("us-west-2"),
			Endpoints: &schemas.BedrockEndpoints{AgentRuntime: schemas.NewSecretVar(host), Runtime: schemas.NewSecretVar(host)},
		},
	}
}

const (
	agentPassthroughPath     = "/agents/AGENT12345/agentAliases/ALIAS12345/sessions/s1/text"
	retrievePassthroughPath  = "/knowledgebases/KB12345678/retrieve"
	guardrailPassthroughPath = "/guardrail/gr1a2b3c4d5e/version/DRAFT/apply"
)

func passthroughRequest(path string) *schemas.BifrostPassthroughRequest {
	return &schemas.BifrostPassthroughRequest{Method: http.MethodPost, Path: path, Body: []byte(`{}`)}
}

// A Bedrock API key authenticates a bedrock-runtime operation with a bearer token instead of SigV4.
func TestBedrockPassthroughUsesBearerForAPIKeysOnRuntimeOperations(t *testing.T) {
	target := newPassthroughTarget(t, http.StatusOK, `{}`, nil)
	_, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.key("bedrock-api-key-123"), passthroughRequest(guardrailPassthroughPath))
	require.Nil(t, bifrostErr)
	got := target.requests()
	require.Len(t, got, 1)
	assert.Equal(t, "Bearer bedrock-api-key-123", got[0].Header.Get("Authorization"))
	assert.Empty(t, got[0].Header.Get("X-Amz-Date"), "an API-key request is not SigV4 signed")
}

// AWS documents that Bedrock API keys cannot be used with Agents for Amazon Bedrock Runtime
// operations. A key that holds both an API key and AWS credentials is therefore signed with the
// credentials for InvokeAgent and Retrieve, never sent as a bearer token.
func TestBedrockPassthroughSignsAgentRuntimeOperationsEvenWhenTheKeyHoldsAnAPIKey(t *testing.T) {
	for name, path := range map[string]string{"InvokeAgent": agentPassthroughPath, "Retrieve": retrievePassthroughPath} {
		t.Run(name, func(t *testing.T) {
			target := newPassthroughTarget(t, http.StatusOK, `{}`, nil)
			_, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.key("bedrock-api-key-123"), passthroughRequest(path))
			require.Nil(t, bifrostErr)
			got := target.requests()
			require.Len(t, got, 1)
			assert.Contains(t, got[0].Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/")
			assert.NotContains(t, got[0].Header.Get("Authorization"), "Bearer")
			assert.NotContains(t, got[0].Header.Get("Authorization"), "bedrock-api-key-123")
		})
	}
}

// A key that holds only a Bedrock API key cannot call an Agents Runtime operation at all, so the
// request is refused with an actionable 400 before anything is sent to AWS - while the same key
// still works for ApplyGuardrail, which is a bedrock-runtime operation.
func TestBedrockPassthroughRefusesAPIKeyOnlyKeysForAgentRuntimeOperations(t *testing.T) {
	for name, path := range map[string]string{"InvokeAgent": agentPassthroughPath, "Retrieve": retrievePassthroughPath} {
		t.Run(name, func(t *testing.T) {
			target := newPassthroughTarget(t, http.StatusOK, `{}`, nil)
			resp, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.apiKeyOnlyKey("bedrock-api-key-123"), passthroughRequest(path))
			assert.Nil(t, resp)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
			require.NotNil(t, bifrostErr.Error)
			assert.Contains(t, bifrostErr.Error.Message, "AWS credentials")
			assert.Empty(t, target.requests(), "a refused request must never reach AWS")
		})
	}
	t.Run("ApplyGuardrail still works with the same key", func(t *testing.T) {
		target := newPassthroughTarget(t, http.StatusOK, `{}`, nil)
		_, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.apiKeyOnlyKey("bedrock-api-key-123"), passthroughRequest(guardrailPassthroughPath))
		require.Nil(t, bifrostErr)
		got := target.requests()
		require.Len(t, got, 1)
		assert.Equal(t, "Bearer bedrock-api-key-123", got[0].Header.Get("Authorization"))
	})
}

// Whatever credentials the caller sent are never the upstream credential, and the caller cannot
// steer the host or add signed headers.
func TestBedrockPassthroughDropsCallerCredentialsAndHostHeaders(t *testing.T) {
	target := newPassthroughTarget(t, http.StatusOK, `{}`, nil)
	_, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.key(""), &schemas.BifrostPassthroughRequest{
		Method: http.MethodPost,
		Path:   "/guardrail/gr1a2b3c4d5e/version/1/apply",
		Body:   []byte(`{}`),
		SafeHeaders: map[string]string{
			"Content-Type":         "application/json",
			"Authorization":        "Bearer attacker-token",
			"X-Amz-Security-Token": "attacker-session",
			"X-Amz-Target":         "other.Operation",
			"Host":                 "evil.example.com",
		},
	})
	require.Nil(t, bifrostErr)
	got := target.requests()
	require.Len(t, got, 1)
	assert.NotContains(t, got[0].Header.Get("Authorization"), "attacker-token")
	assert.Contains(t, got[0].Header.Get("Authorization"), "AWS4-HMAC-SHA256")
	assert.Empty(t, got[0].Header.Get("X-Amz-Security-Token"))
	assert.Empty(t, got[0].Header.Get("X-Amz-Target"))
}

// Only the three allow-listed operations are reachable. Everything else - other methods, other
// services, traversal, extra or empty segments, encoded dots - is refused with a 400 before any
// request leaves the gateway.
func TestBedrockPassthroughRejectsEverythingOutsideTheAllowList(t *testing.T) {
	cases := []struct {
		name, method, path string
	}{
		{"wrong method on an allowed path", http.MethodGet, "/knowledgebases/KB12345678/retrieve"},
		{"model invocation is not allowed", http.MethodPost, "/model/anthropic.claude-v2/invoke"},
		{"control plane is not allowed", http.MethodPost, "/foundation-models"},
		{"traversal in an identifier", http.MethodPost, "/agents/../model/x/agentAliases/a/sessions/s/text"},
		{"encoded traversal", http.MethodPost, "/agents/%2e%2e/agentAliases/a/sessions/s/text"},
		{"extra trailing segment", http.MethodPost, "/knowledgebases/KB12345678/retrieve/extra"},
		{"empty identifier", http.MethodPost, "/agents//agentAliases/a/sessions/s/text"},
		{"identifier with a slash", http.MethodPost, "/guardrail/a/b/version/1/apply"},
		{"identifier with a space", http.MethodPost, "/knowledgebases/KB 1/retrieve"},
		{"retrieve-and-generate is not allowed", http.MethodPost, "/retrieveAndGenerate"},
		{"empty path", http.MethodPost, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := newPassthroughTarget(t, http.StatusOK, `{}`, nil)
			resp, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.key(""), &schemas.BifrostPassthroughRequest{Method: tc.method, Path: tc.path, Body: []byte(`{}`)})
			assert.Nil(t, resp)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
			assert.Empty(t, target.requests(), "a refused request must never reach AWS")
		})
	}
}

// Each identifier follows the character set AWS documents for that parameter, not one shared rule:
// InvokeAgent session ids may contain dots (and other of "._:-"), while agent, alias, knowledge-base
// and guardrail ids are alphanumeric only and a guardrail version is a number or DRAFT. A session id
// pattern that admits dots also admits "..", so dot-only segments are refused explicitly.
func TestBedrockPassthroughIdentifiersFollowAWSCharacterSets(t *testing.T) {
	const agentPath = "/agents/AGENT12345/agentAliases/ALIAS12345/sessions/%s/text"
	cases := []struct {
		name, path string
		accepted   bool
	}{
		{"session id with a dot", sprintfPath(agentPath, "session.1"), true},
		{"session id using every allowed punctuation", sprintfPath(agentPath, "a_b:c-d.e"), true},
		{"session id with a leading dot", sprintfPath(agentPath, ".hidden"), true},
		{"session id that is a single dot", sprintfPath(agentPath, "."), false},
		{"session id that is two dots", sprintfPath(agentPath, ".."), false},
		{"session id that is only dots", sprintfPath(agentPath, "..."), false},
		{"session id with a space", sprintfPath(agentPath, "a b"), false},
		{"agent id with an underscore", "/agents/AGENT_1234/agentAliases/ALIAS12345/sessions/s1/text", false},
		{"agent alias id with a colon", "/agents/AGENT12345/agentAliases/ALIAS:1234/sessions/s1/text", false},
		{"knowledge base id with a hyphen", "/knowledgebases/KB-12345678/retrieve", false},
		{"guardrail id with a dot", "/guardrail/gr.1a2b3c/version/1/apply", false},
		{"guardrail version DRAFT", "/guardrail/gr1a2b3c4d5e/version/DRAFT/apply", true},
		{"guardrail version 1", "/guardrail/gr1a2b3c4d5e/version/1/apply", true},
		{"guardrail version at the 8 digit limit", "/guardrail/gr1a2b3c4d5e/version/12345678/apply", true},
		{"guardrail version latest", "/guardrail/gr1a2b3c4d5e/version/latest/apply", false},
		{"guardrail version lowercase draft", "/guardrail/gr1a2b3c4d5e/version/draft/apply", false},
		{"guardrail version zero", "/guardrail/gr1a2b3c4d5e/version/0/apply", false},
		{"guardrail version with a leading zero", "/guardrail/gr1a2b3c4d5e/version/01/apply", false},
		{"guardrail version past 8 digits", "/guardrail/gr1a2b3c4d5e/version/123456789/apply", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := newPassthroughTarget(t, http.StatusOK, `{}`, nil)
			resp, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.key(""), &schemas.BifrostPassthroughRequest{Method: http.MethodPost, Path: tc.path, Body: []byte(`{}`)})
			if tc.accepted {
				require.Nil(t, bifrostErr, "AWS accepts this identifier, so the gateway must forward it")
				require.NotNil(t, resp)
				got := target.requests()
				require.Len(t, got, 1)
				assert.Equal(t, tc.path, got[0].Path)
				return
			}
			assert.Nil(t, resp)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
			assert.Empty(t, target.requests(), "a refused request must never reach AWS")
		})
	}
}

func sprintfPath(format, id string) string { return strings.Replace(format, "%s", id, 1) }

// newStreamingPassthroughTarget is a fake AWS endpoint whose handler the test controls, so it can
// pace chunks and stall. The provider's clients get a short whole-response timeout, which only a
// client built for streaming escapes.
func newStreamingPassthroughTarget(t *testing.T, overallTimeout time.Duration, handler http.HandlerFunc) *passthroughTarget {
	t.Helper()
	target := &passthroughTarget{}
	target.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target.mu.Lock()
		target.seen = append(target.seen, capturedPassthroughRequest{Method: r.Method, Path: r.URL.EscapedPath(), Header: r.Header.Clone()})
		target.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(target.server.Close)
	client := target.server.Client()
	client.Timeout = overallTimeout
	target.provider = &BedrockProvider{client: client, streamingClient: providerUtils.BuildStreamingHTTPClient(client), logger: &passthroughTestLogger{}}
	return target
}

func passthroughStreamCtx(t *testing.T, idle time.Duration) *schemas.BifrostContext {
	t.Helper()
	ctx := passthroughCtx(t)
	if idle > 0 {
		ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, idle)
	}
	return ctx
}

var passthroughNoHooks schemas.PostHookRunner = func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

// next reads one chunk or fails the test if none arrives in time.
func nextChunk(t *testing.T, ch <-chan *schemas.BifrostStreamChunk, within time.Duration) (*schemas.BifrostStreamChunk, bool) {
	t.Helper()
	select {
	case c, ok := <-ch:
		return c, ok
	case <-time.After(within):
		t.Fatalf("no chunk within %s", within)
		return nil, false
	}
}

// InvokeAgent answers with an event stream. The streaming passthrough hands each piece to the caller
// as AWS sends it instead of buffering the whole run: the first chunk must be delivered while the
// upstream is still working on the rest.
func TestBedrockPassthroughStreamForwardsInvokeAgentChunksAsTheyArrive(t *testing.T) {
	release := make(chan struct{})
	target := newStreamingPassthroughTarget(t, 0, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Header().Set("X-Amzn-Requestid", "req-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk-1|"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		_, _ = w.Write([]byte("chunk-2"))
	})

	ch, bifrostErr := target.provider.PassthroughStream(passthroughStreamCtx(t, 0), passthroughNoHooks, nil, target.key(""), passthroughRequest(agentPassthroughPath))
	require.Nil(t, bifrostErr)
	require.NotNil(t, ch)

	first, ok := nextChunk(t, ch, 3*time.Second)
	require.True(t, ok)
	require.Nil(t, first.BifrostError)
	require.NotNil(t, first.BifrostPassthroughResponse)
	assert.Equal(t, http.StatusOK, first.BifrostPassthroughResponse.StatusCode)
	assert.Equal(t, "application/vnd.amazon.eventstream", first.BifrostPassthroughResponse.Headers["Content-Type"])
	assert.Equal(t, "req-stream", first.BifrostPassthroughResponse.Headers["X-Amzn-Requestid"])
	assert.Equal(t, "chunk-1|", string(first.BifrostPassthroughResponse.Body), "the first piece arrives while the upstream is still producing")

	close(release)
	var rest []byte
	for {
		c, ok := nextChunk(t, ch, 3*time.Second)
		if !ok {
			break
		}
		require.Nil(t, c.BifrostError)
		if c.BifrostPassthroughResponse != nil {
			rest = append(rest, c.BifrostPassthroughResponse.Body...)
		}
	}
	assert.Equal(t, "chunk-2", string(rest))

	got := target.requests()
	require.Len(t, got, 1)
	assert.Equal(t, agentPassthroughPath, got[0].Path)
	assert.Contains(t, got[0].Header.Get("Authorization"), "AWS4-HMAC-SHA256", "the streaming path is signed like the unary one")
}

// A long agent run is not bound by the unary whole-response timeout; only a stalled connection is.
func TestBedrockPassthroughStreamHasNoOverallDeadline(t *testing.T) {
	target := newStreamingPassthroughTarget(t, 150*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 5; i++ {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	})
	ch, bifrostErr := target.provider.PassthroughStream(passthroughStreamCtx(t, 0), passthroughNoHooks, nil, target.key(""), passthroughRequest(agentPassthroughPath))
	require.Nil(t, bifrostErr)
	total := 0
	for {
		c, ok := nextChunk(t, ch, 3*time.Second)
		if !ok {
			break
		}
		require.Nil(t, c.BifrostError, "a 500ms stream must outlive a 150ms unary timeout")
		if c.BifrostPassthroughResponse != nil {
			total += len(c.BifrostPassthroughResponse.Body)
		}
	}
	assert.Equal(t, 5, total)
}

// A connection that goes quiet is cut by the idle timeout and reported as an error chunk.
func TestBedrockPassthroughStreamStalledUpstreamHitsTheIdleTimeout(t *testing.T) {
	done := make(chan struct{})
	target := newStreamingPassthroughTarget(t, 0, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		select {
		case <-done:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	t.Cleanup(func() { close(done) })
	ch, bifrostErr := target.provider.PassthroughStream(passthroughStreamCtx(t, 300*time.Millisecond), passthroughNoHooks, nil, target.key(""), passthroughRequest(agentPassthroughPath))
	require.Nil(t, bifrostErr)
	sawError := false
	for {
		c, ok := nextChunk(t, ch, 3*time.Second)
		if !ok {
			break
		}
		if c.BifrostError != nil {
			sawError = true
		}
	}
	assert.True(t, sawError, "a stalled stream must end with an error chunk, not hang")
}

// An upstream error reaches the caller as the first chunk with AWS's status, headers and body.
func TestBedrockPassthroughStreamForwardsUpstreamErrorStatus(t *testing.T) {
	target := newStreamingPassthroughTarget(t, 0, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amzn-Errortype", "ResourceNotFoundException")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"agent not found"}`))
	})
	ch, bifrostErr := target.provider.PassthroughStream(passthroughStreamCtx(t, 0), passthroughNoHooks, nil, target.key(""), passthroughRequest(agentPassthroughPath))
	require.Nil(t, bifrostErr)
	first, ok := nextChunk(t, ch, 3*time.Second)
	require.True(t, ok)
	require.NotNil(t, first.BifrostPassthroughResponse)
	assert.Equal(t, http.StatusNotFound, first.BifrostPassthroughResponse.StatusCode)
	assert.Equal(t, "ResourceNotFoundException", first.BifrostPassthroughResponse.Headers["X-Amzn-Errortype"])
	assert.JSONEq(t, `{"message":"agent not found"}`, string(first.BifrostPassthroughResponse.Body))
}

// The streaming entry point enforces the same allow-list as the unary one.
func TestBedrockPassthroughStreamRefusesRoutesOutsideTheAllowList(t *testing.T) {
	target := newStreamingPassthroughTarget(t, 0, func(w http.ResponseWriter, r *http.Request) {})
	ch, bifrostErr := target.provider.PassthroughStream(passthroughStreamCtx(t, 0), passthroughNoHooks, nil, target.key(""), passthroughRequest("/model/amazon.titan-text-express-v1/invoke"))
	assert.Nil(t, ch)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	assert.Empty(t, target.requests())
}

// The unary entry point stays usable for an InvokeAgent call made through the Go SDK, but it is
// bounded: it escapes the whole-response timeout like the stream does, and refuses a body past the cap.
func TestBedrockPassthroughUnaryInvokeAgentIsBoundedNotTimedOut(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("done"))
	}
	t.Run("InvokeAgent outlives the unary timeout", func(t *testing.T) {
		target := newStreamingPassthroughTarget(t, 100*time.Millisecond, slow)
		resp, bifrostErr := target.provider.Passthrough(passthroughStreamCtx(t, 0), target.key(""), passthroughRequest(agentPassthroughPath))
		require.Nil(t, bifrostErr, "InvokeAgent is not bound by the unary whole-response timeout")
		assert.Equal(t, "done", string(resp.Body))
	})
	t.Run("Retrieve keeps the unary timeout", func(t *testing.T) {
		target := newStreamingPassthroughTarget(t, 100*time.Millisecond, slow)
		_, bifrostErr := target.provider.Passthrough(passthroughStreamCtx(t, 0), target.key(""), passthroughRequest(retrievePassthroughPath))
		require.NotNil(t, bifrostErr, "a plain request/response operation still times out")
	})
	t.Run("a body past the cap is refused, not buffered", func(t *testing.T) {
		old := bedrockPassthroughMaxResponseBytes
		bedrockPassthroughMaxResponseBytes = 10
		t.Cleanup(func() { bedrockPassthroughMaxResponseBytes = old })
		target := newStreamingPassthroughTarget(t, 0, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("y", 64))) })
		resp, bifrostErr := target.provider.Passthrough(passthroughStreamCtx(t, 0), target.key(""), passthroughRequest(agentPassthroughPath))
		assert.Nil(t, resp)
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.Error)
		assert.Contains(t, bifrostErr.Error.Message, "too large")
	})
}

// passthroughTestLogger discards everything: the stream error path logs through the provider logger.
type passthroughTestLogger struct{}

func (l *passthroughTestLogger) Debug(msg string, args ...any)                     {}
func (l *passthroughTestLogger) Info(msg string, args ...any)                      {}
func (l *passthroughTestLogger) Warn(msg string, args ...any)                      {}
func (l *passthroughTestLogger) Error(msg string, args ...any)                     {}
func (l *passthroughTestLogger) Fatal(msg string, args ...any)                     {}
func (l *passthroughTestLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *passthroughTestLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *passthroughTestLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}
