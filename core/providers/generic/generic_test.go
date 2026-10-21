package generic

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedRequest struct {
	Method, Path, RawQuery, Body string
	Header                       http.Header
}

type upstream struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   []capturedRequest
}

func (u *upstream) requests() []capturedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]capturedRequest(nil), u.seen...)
}

// newUpstream stands in for the configured non-LLM API and records what reached it.
func newUpstream(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *upstream {
	t.Helper()
	u := &upstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.seen = append(u.seen, capturedRequest{Method: r.Method, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery, Body: string(body), Header: r.Header.Clone()})
		u.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func jsonUpstream(t *testing.T, status int, body string) *upstream {
	t.Helper()
	return newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Id", "up-1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// newProvider builds a generic provider named "echo" over the upstream with the given relay config.
func newProvider(t *testing.T, u *upstream, cfg *schemas.GenericProviderConfig) *GenericProvider {
	t.Helper()
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: u.server.URL, DefaultRequestTimeoutInSeconds: 5},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "echo",
			BaseProviderType:  schemas.Generic,
			GenericConfig:     cfg,
		},
	}, &testLogger{})
	require.NoError(t, err)
	return provider
}

func bearerKey(value string) schemas.Key {
	k := schemas.Key{ID: "k1", Name: "k1", Weight: 1}
	if value != "" {
		k.Value = *schemas.NewSecretVar(value)
	}
	return k
}

func testCtx(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	return ctx
}

var noHooks schemas.PostHookRunner = func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

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

// drainStream collects the forwarded body and the final chunk's usage.
func drainStream(t *testing.T, ch <-chan *schemas.BifrostStreamChunk) (string, *schemas.BifrostPassthroughUsage) {
	t.Helper()
	var body strings.Builder
	var usage *schemas.BifrostPassthroughUsage
	for {
		c, ok := nextChunk(t, ch, 3*time.Second)
		if !ok {
			return body.String(), usage
		}
		require.Nil(t, c.BifrostError)
		if c.BifrostPassthroughResponse != nil {
			body.Write(c.BifrostPassthroughResponse.Body)
			if c.BifrostPassthroughResponse.PassthroughUsage != nil {
				usage = c.BifrostPassthroughResponse.PassthroughUsage
			}
		}
	}
}

func relayRequest(method, path, rawQuery, body string, headers map[string]string) *schemas.BifrostPassthroughRequest {
	safe := map[string]string{"content-type": "application/json"}
	for k, v := range headers {
		safe[k] = v
	}
	return &schemas.BifrostPassthroughRequest{Method: method, Path: path, RawQuery: rawQuery, Body: []byte(body), SafeHeaders: safe, Provider: "echo"}
}

// TestGeneric is the make test-core entry point for this provider: every scenario is a subtest of
// GenericTests so TESTCASE=<name> selects it.
func TestGeneric(t *testing.T) {
	t.Run("GenericTests", func(t *testing.T) {
		t.Run("RelayPOSTBearer", testRelayPOSTBearer)
		t.Run("RelayGETQueryAuth", testRelayGETQueryAuth)
		t.Run("HeaderAuthWithPrefix", testHeaderAuthWithPrefix)
		t.Run("BasicAuth", testBasicAuth)
		t.Run("NoneAuthKeyless", testNoneAuthKeyless)
		t.Run("CallerAuthHeaderAndQueryParamStripped", testCallerCredentialsStripped)
		t.Run("HostPinningRejectsAbsoluteURLPath", testHostPinning)
		t.Run("Non2xxReturnsNilUsage", testNon2xxReturnsNilUsage)
		t.Run("NonBillableRouteReturnsNilUsage", testNonBillableRoute)
		t.Run("ResponseTooLargeIs502", testResponseTooLarge)
		t.Run("StreamSSEObservesUsage", testStreamSSEObservesUsage)
		t.Run("StreamNonSSEUsesDefaultUsage", testStreamNonSSEUsesDefaultUsage)
		t.Run("UnsupportedOperations", testUnsupportedOperations)
		t.Run("AllowedRequestsGate", testAllowedRequestsGate)
		t.Run("SigV4SignsRequest", testSigV4SignsRequest)
		t.Run("SigV4MissingRegionIs400", testSigV4MissingRegion)
		t.Run("SigV4DropsCallerSignatureHeaders", testSigV4DropsCallerSignatureHeaders)
		t.Run("RedirectsAreReturnedNotFollowed", testRedirectsAreReturnedNotFollowed)
		t.Run("TransportErrorRedactsQueryCredential", testTransportErrorRedactsQueryCredential)
		t.Run("UnitPricingEndToEnd", testUnitPricingEndToEnd)
	})
}

// Per-unit pricing through the relay: a Textract-shaped response priced per page, a
// Translate-shaped request priced per character, and a TTS-shaped SSML request with an audio
// response priced per body byte, on both the buffered and the streaming paths.
func testUnitPricingEndToEnd(t *testing.T) {
	t.Run("response pages", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{"Blocks":[],"DocumentMetadata":{"Pages":3}}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: schemas.Ptr(0.0015)}})
		resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{"Document":{"Bytes":"aGk="}}`, nil))
		require.Nil(t, bifrostErr)
		require.NotNil(t, resp.PassthroughUsage)
		require.NotNil(t, resp.PassthroughUsage.Cost)
		assert.InDelta(t, 0.0045, resp.PassthroughUsage.Cost.TotalCost, 1e-9)
	})
	t.Run("request characters", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{"TranslatedText":"hallo"}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(0.001)}})
		resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{"Text":"hello","SourceLanguageCode":"en","TargetLanguageCode":"de"}`, map[string]string{"content-type": "application/x-amz-json-1.1"}))
		require.Nil(t, bifrostErr)
		require.NotNil(t, resp.PassthroughUsage.Cost)
		assert.InDelta(t, 0.005, resp.PassthroughUsage.Cost.TotalCost, 1e-9)
	})
	t.Run("request body bytes with a streamed audio response", func(t *testing.T) {
		up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "audio/mpeg")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ID3\x00binary"))
		})
		provider := newProvider(t, up, &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsBodyBytes, CostPerUnit: schemas.Ptr(0.01)}})
		ssml := "<speak>Hi</speak>"
		ch, bifrostErr := provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/cognitiveservices/v1", "", ssml, map[string]string{"content-type": "application/ssml+xml"}))
		require.Nil(t, bifrostErr)
		_, usage := drainStream(t, ch)
		require.NotNil(t, usage, "the unframed stream reports the request-side quantity on its final chunk")
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, float64(len(ssml))*0.01, usage.Cost.TotalCost, 1e-9)

		resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/cognitiveservices/v1", "", ssml, map[string]string{"content-type": "application/ssml+xml"}))
		require.Nil(t, bifrostErr)
		require.NotNil(t, resp.PassthroughUsage.Cost)
		assert.InDelta(t, float64(len(ssml))*0.01, resp.PassthroughUsage.Cost.TotalCost, 1e-9, "the buffered path agrees")
	})
}

// A redirect is relayed to the caller as the upstream answered it. Following it would carry the
// injected credential to the host the upstream named and step around the base URL pinning.
func testRedirectsAreReturnedNotFollowed(t *testing.T) {
	elsewhere := jsonUpstream(t, http.StatusOK, `{"leaked":true}`)
	origin := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.server.URL+"/landing", http.StatusTemporaryRedirect)
	})

	for _, tc := range []struct {
		name string
		cfg  *schemas.GenericProviderConfig
	}{
		{"header auth", &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "Ocp-Apim-Subscription-Key"}}},
		{"query auth", &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "api_key"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newProvider(t, origin, tc.cfg)
			resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("SECRET"), relayRequest(http.MethodPost, "/x", "", `{}`, nil))
			require.Nil(t, bifrostErr)
			assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode, "the 3xx reaches the caller")
			assert.Equal(t, elsewhere.server.URL+"/landing", resp.Headers["Location"])
			assert.Empty(t, elsewhere.requests(), "the redirect target never sees a request, let alone the key")

			ch, bifrostErr := provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("SECRET"), relayRequest(http.MethodPost, "/x/stream", "", `{}`, nil))
			require.Nil(t, bifrostErr)
			first, ok := nextChunk(t, ch, 3*time.Second)
			require.True(t, ok)
			require.NotNil(t, first.BifrostPassthroughResponse)
			assert.Equal(t, http.StatusTemporaryRedirect, first.BifrostPassthroughResponse.StatusCode, "the streaming relay behaves the same")
			drainStream(t, ch)
			assert.Empty(t, elsewhere.requests())
		})
	}
}

// A transport failure names the host and path it could not reach, never the query string: for
// query auth that string is the key, and the error goes to the caller and into the log row.
func testTransportErrorRedactsQueryCredential(t *testing.T) {
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1", DefaultRequestTimeoutInSeconds: 2},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "dead",
			BaseProviderType:  schemas.Generic,
			GenericConfig:     &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "api_key"}},
		},
	}, &testLogger{})
	require.NoError(t, err)

	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("QSECRET"), relayRequest(http.MethodPost, "/v1/search", "q=1", `{}`, nil))
	require.NotNil(t, bifrostErr)
	msg := bifrostErr.Error.Message
	if bifrostErr.Error.Error != nil {
		msg += " " + bifrostErr.Error.Error.Error()
	}
	assert.NotContains(t, msg, "QSECRET")
	assert.NotContains(t, msg, "api_key")
	assert.Contains(t, msg, "127.0.0.1:1/v1/search", "the destination is still named")

	_, bifrostErr = provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("QSECRET"), relayRequest(http.MethodPost, "/v1/stream", "q=1", `{}`, nil))
	require.NotNil(t, bifrostErr)
	msg = bifrostErr.Error.Message
	if bifrostErr.Error.Error != nil {
		msg += " " + bifrostErr.Error.Error.Error()
	}
	assert.NotContains(t, msg, "QSECRET")
}

func sigv4Key(region string) schemas.Key {
	k := schemas.Key{ID: "aws", Name: "aws", Weight: 1, BedrockKeyConfig: &schemas.BedrockKeyConfig{
		AccessKey: *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE"),
		SecretKey: *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
	}}
	if region != "" {
		k.BedrockKeyConfig.Region = schemas.NewSecretVar(region)
	}
	return k
}

// A generic provider with aws_sigv4 auth signs the relayed request with the key's AWS credentials
// for the configured service: this is what puts Textract and Translate behind Bifrost.
func testSigV4SignsRequest(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{"Blocks":[]}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "textract", Region: "us-east-1"}}})

	body := `{"Document":{"Bytes":"aGk="}}`
	resp, bifrostErr := provider.Passthrough(testCtx(t), sigv4Key(""), relayRequest(http.MethodPost, "/", "", body, map[string]string{"x-amz-target": "Textract.DetectDocumentText", "content-type": "application/x-amz-json-1.1"}))
	require.Nil(t, bifrostErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	got := up.requests()
	require.Len(t, got, 1)
	auth := got[0].Header.Get("Authorization")
	assert.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/"), auth)
	assert.Contains(t, auth, "/us-east-1/textract/aws4_request", "scope follows the configured region and service")
	assert.NotEmpty(t, got[0].Header.Get("X-Amz-Date"))
	assert.Equal(t, "Textract.DetectDocumentText", got[0].Header.Get("X-Amz-Target"), "the operation header the caller sent is kept and signed")
	assert.Equal(t, "application/x-amz-json-1.1", got[0].Header.Get("Content-Type"))
	assert.Equal(t, body, got[0].Body)

	t.Run("region falls back to the key", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "translate"}}})
		_, bifrostErr := provider.Passthrough(testCtx(t), sigv4Key("eu-west-1"), relayRequest(http.MethodPost, "/", "", `{}`, nil))
		require.Nil(t, bifrostErr)
		got := up.requests()
		require.Len(t, got, 1)
		assert.Contains(t, got[0].Header.Get("Authorization"), "/eu-west-1/translate/aws4_request")
	})
}

func testSigV4MissingRegion(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "textract"}}})
	_, bifrostErr := provider.Passthrough(testCtx(t), sigv4Key(""), relayRequest(http.MethodPost, "/", "", `{}`, nil))
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	assert.Contains(t, bifrostErr.Error.Message, "region")
	assert.Empty(t, up.requests())
}

// Signature-bearing headers a caller sends are the signer's to set; they are dropped before
// signing so a stale caller signature can never be forwarded or folded into ours.
func testSigV4DropsCallerSignatureHeaders(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "textract", Region: "us-east-1"}}})
	_, bifrostErr := provider.Passthrough(testCtx(t), sigv4Key(""), relayRequest(http.MethodPost, "/", "", `{}`, map[string]string{
		"x-amz-date": "20200101T000000Z", "x-amz-security-token": "caller-token", "x-amz-content-sha256": "deadbeef", "authorization": "AWS4-HMAC-SHA256 Credential=CALLER/...",
		"x-amz-target": "Textract.AnalyzeDocument",
	}))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.NotEqual(t, "20200101T000000Z", got[0].Header.Get("X-Amz-Date"))
	assert.Empty(t, got[0].Header.Get("X-Amz-Security-Token"), "no session token on the key means none upstream")
	assert.NotEqual(t, "deadbeef", got[0].Header.Get("X-Amz-Content-Sha256"))
	assert.Contains(t, got[0].Header.Get("Authorization"), "Credential=AKIAIOSFODNN7EXAMPLE/")
	assert.Equal(t, "Textract.AnalyzeDocument", got[0].Header.Get("X-Amz-Target"))
}

func testRelayPOSTBearer(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{"results":[1,2],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	provider := newProvider(t, up, nil)

	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("sk-upstream"), relayRequest(http.MethodPost, "/search", "", `{"query":"bifrost"}`, map[string]string{"x-trace": "abc"}))
	require.Nil(t, bifrostErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"results":[1,2],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`, string(resp.Body))
	assert.Equal(t, "up-1", resp.Headers["X-Upstream-Id"], "upstream headers reach the caller")
	assert.Equal(t, "/search", resp.ExtraFields.PassthroughPath)
	require.NotNil(t, resp.PassthroughUsage, "a billable 2xx always reports usage")
	require.NotNil(t, resp.PassthroughUsage.LLMUsage, "optimistic parsing reads the OpenAI usage shape")
	assert.Equal(t, 7, resp.PassthroughUsage.LLMUsage.TotalTokens)
	assert.Nil(t, resp.PassthroughUsage.Cost, "no cost mapping and no fee means no cost")

	got := up.requests()
	require.Len(t, got, 1)
	assert.Equal(t, http.MethodPost, got[0].Method)
	assert.Equal(t, "/search", got[0].Path)
	assert.Equal(t, `{"query":"bifrost"}`, got[0].Body)
	assert.Equal(t, "Bearer sk-upstream", got[0].Header.Get("Authorization"))
	assert.Equal(t, "abc", got[0].Header.Get("X-Trace"), "non-credential caller headers are forwarded")
	assert.Equal(t, "application/json", got[0].Header.Get("Content-Type"))
}

func testRelayGETQueryAuth(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{"ok":true}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "api_key"}})

	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("q-secret"), relayRequest(http.MethodGet, "/v1/weather", "city=Paris", "", nil))
	require.Nil(t, bifrostErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	got := up.requests()
	require.Len(t, got, 1)
	assert.Equal(t, http.MethodGet, got[0].Method)
	assert.Equal(t, "city=Paris&api_key=q-secret", got[0].RawQuery, "the key rides on the query string after the caller's parameters")
	assert.Empty(t, got[0].Header.Get("Authorization"))
}

func testHeaderAuthWithPrefix(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "Ocp-Apim-Subscription-Key", ValuePrefix: "key-"}})

	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("abc"), relayRequest(http.MethodPost, "/cognitiveservices/v1", "", `<speak/>`, nil))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.Equal(t, "key-abc", got[0].Header.Get("Ocp-Apim-Subscription-Key"))
	assert.Empty(t, got[0].Header.Get("Authorization"))
}

func testBasicAuth(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthBasic}})

	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("user:pass"), relayRequest(http.MethodGet, "/me", "", "", nil))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")), got[0].Header.Get("Authorization"))
}

func testNoneAuthKeyless(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthNone}})

	_, bifrostErr := provider.Passthrough(testCtx(t), schemas.Key{}, relayRequest(http.MethodGet, "/public", "", "", nil))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Header.Get("Authorization"))
}

// The configured key is the only credential upstream: a caller's own auth header, the auth
// header name, and the auth query parameter never reach the API.
func testCallerCredentialsStripped(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "x-api-key"}})
		_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("real"), relayRequest(http.MethodPost, "/post", "", `{}`, map[string]string{"X-Api-Key": "caller-key", "authorization": "Bearer caller"}))
		require.Nil(t, bifrostErr)
		got := up.requests()
		require.Len(t, got, 1)
		assert.Equal(t, "real", got[0].Header.Get("X-Api-Key"))
		assert.Empty(t, got[0].Header.Get("Authorization"))
	})
	t.Run("query", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "key"}})
		_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("real"), relayRequest(http.MethodGet, "/get", "key=caller&q=1", "", nil))
		require.Nil(t, bifrostErr)
		got := up.requests()
		require.Len(t, got, 1)
		assert.Equal(t, "q=1&key=real", got[0].RawQuery)
	})
}

func testHostPinning(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, nil)
	for _, path := range []string{"//evil.example/x", "@evil.example/x", "https://evil.example/x"} {
		_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodGet, path, "", "", nil))
		require.NotNil(t, bifrostErr, "path %q must be refused", path)
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	}
	assert.Empty(t, up.requests(), "nothing reached the upstream")
}

func testNon2xxReturnsNilUsage(t *testing.T) {
	up := jsonUpstream(t, http.StatusTooManyRequests, `{"error":"slow down","usage":{"total_tokens":5}}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01)}})

	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/search", "", `{}`, nil))
	require.Nil(t, bifrostErr, "upstream errors are relayed, not converted")
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Nil(t, resp.PassthroughUsage, "a failed call is not charged")
}

func testNonBillableRoute(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{"status":"done"}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{
		Usage:  &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01)},
		Routes: []schemas.GenericRouteConfig{{Method: "GET", Path: "/jobs/*", Billable: schemas.Ptr(false)}},
	})

	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodGet, "/jobs/123", "", "", nil))
	require.Nil(t, bifrostErr)
	assert.Nil(t, resp.PassthroughUsage, "a status poll is relayed but not counted")

	resp, bifrostErr = provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/jobs", "", `{}`, nil))
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.PassthroughUsage)
	require.NotNil(t, resp.PassthroughUsage.Cost)
	assert.InDelta(t, 0.01, resp.PassthroughUsage.Cost.TotalCost, 1e-9, "the provider fee applies to routes without an override")

	resp, bifrostErr = provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodGet, "/status", "", "", nil))
	require.Nil(t, bifrostErr)
	assert.Nil(t, resp.PassthroughUsage, "an unrouted GET is not counted")
}

func testResponseTooLarge(t *testing.T) {
	orig := genericPassthroughMaxResponseBytes
	genericPassthroughMaxResponseBytes = 16
	t.Cleanup(func() { genericPassthroughMaxResponseBytes = orig })

	up := jsonUpstream(t, http.StatusOK, strings.Repeat("x", 64))
	provider := newProvider(t, up, nil)
	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodGet, "/big", "", "", nil))
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusBadGateway, *bifrostErr.StatusCode)
}

func testStreamSSEObservesUsage(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"delta\":\"hel\"}\n\n"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("data: {\"delta\":\"lo\",\"usage\":{\"input_tokens\":2,\"output_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.002)}})

	ch, bifrostErr := provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/chat/stream", "", `{"stream":true}`, nil))
	require.Nil(t, bifrostErr)
	body, usage := drainStream(t, ch)
	assert.Contains(t, body, `"delta":"hel"`)
	assert.Contains(t, body, "data: [DONE]", "bytes are forwarded unchanged")
	require.NotNil(t, usage, "the final chunk carries the observed usage")
	require.NotNil(t, usage.LLMUsage)
	assert.Equal(t, 7, usage.LLMUsage.TotalTokens, "input+output when the event has no total")
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.002, usage.Cost.TotalCost, 1e-9, "the per-request fee is folded into the observed usage")
}

// A binary or otherwise unframed stream has nothing to observe; it still counts as one request.
func testStreamNonSSEUsesDefaultUsage(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ID3\x00\x00binary"))
	})
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.5)}})

	ch, bifrostErr := provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/tts/stream", "", `{"text":"hi"}`, nil))
	require.Nil(t, bifrostErr)
	body, usage := drainStream(t, ch)
	assert.Equal(t, "ID3\x00\x00binary", body)
	require.NotNil(t, usage, "DefaultUsage lands on the final chunk when nothing was observed")
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.5, usage.Cost.TotalCost, 1e-9)
}

func testUnsupportedOperations(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, nil)
	assert.Equal(t, schemas.ModelProvider("echo"), provider.GetProviderKey())

	_, bifrostErr := provider.ChatCompletion(testCtx(t), bearerKey("k"), &schemas.BifrostChatRequest{Model: "x"})
	require.NotNil(t, bifrostErr)
	assert.Equal(t, "unsupported_operation", *bifrostErr.Error.Code)
	assert.Equal(t, schemas.ModelProvider("echo"), bifrostErr.ExtraFields.Provider)

	_, bifrostErr = provider.Embedding(testCtx(t), bearerKey("k"), &schemas.BifrostEmbeddingRequest{Model: "x"})
	require.NotNil(t, bifrostErr)
	assert.Equal(t, "unsupported_operation", *bifrostErr.Error.Code)
	assert.Empty(t, up.requests())
}

func testAllowedRequestsGate(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "gated",
			BaseProviderType:  schemas.Generic,
			AllowedRequests:   &schemas.AllowedRequests{Passthrough: true},
		},
	}, &testLogger{})
	require.NoError(t, err)

	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodGet, "/x", "", "", nil))
	require.Nil(t, bifrostErr)
	_, bifrostErr = provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodGet, "/x", "", "", nil))
	require.NotNil(t, bifrostErr, "passthrough_stream is off for this provider")
	assert.Equal(t, "unsupported_operation", *bifrostErr.Error.Code)
}

func TestNewGenericProviderRequiresBaseURL(t *testing.T) {
	_, err := NewGenericProvider(&schemas.ProviderConfig{CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Generic}}, &testLogger{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base_url")

	_, err = NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: "https://api.example.com"},
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: "magic"}}},
	}, &testLogger{})
	require.Error(t, err, "an invalid relay config is refused at construction")
}

func TestGenericStreams(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{
		Streaming: &schemas.GenericStreamingConfig{Paths: []string{"/v1/live/**"}, DetectFromAcceptHeader: true},
		Routes: []schemas.GenericRouteConfig{
			{Method: "POST", Path: "/v1/stream-but-not", Stream: schemas.Ptr(false)},
			{Method: "POST", Path: "/v1/events", Stream: schemas.Ptr(true)},
		},
	})
	assert.False(t, provider.Streams("POST", "/v1/stream-but-not", "", true), "a route's stream flag wins over every heuristic")
	assert.True(t, provider.Streams("POST", "/v1/events", "", false))
	assert.True(t, provider.Streams("GET", "/v1/live/room/1", "", false), "streaming.paths glob")
	assert.True(t, provider.Streams("POST", "/v1/chat", "text/event-stream", false), "Accept header when enabled")
	assert.True(t, provider.Streams("POST", "/v1/chat", "", true), "body stream flag")
	assert.True(t, provider.Streams("POST", "/v1/chat/stream", "", false), "path names a stream")
	assert.False(t, provider.Streams("POST", "/v1/chat", "application/json", false))
}

func BenchmarkGenericPassthroughUnary(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	b.Cleanup(server.Close)
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 5},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "bench", BaseProviderType: schemas.Generic},
	}, &testLogger{})
	if err != nil {
		b.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	b.Cleanup(ctx.Cancel)
	key := bearerKey("k")
	req := relayRequest(http.MethodPost, "/search", "", `{"q":"x"}`, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, bifrostErr := provider.Passthrough(ctx, key, req); bifrostErr != nil {
			b.Fatal(bifrostErr.Error.Message)
		}
	}
}

// testLogger discards everything.
type testLogger struct{}

func (l *testLogger) Debug(msg string, args ...any)                     {}
func (l *testLogger) Info(msg string, args ...any)                      {}
func (l *testLogger) Warn(msg string, args ...any)                      {}
func (l *testLogger) Error(msg string, args ...any)                     {}
func (l *testLogger) Fatal(msg string, args ...any)                     {}
func (l *testLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *testLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *testLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}
