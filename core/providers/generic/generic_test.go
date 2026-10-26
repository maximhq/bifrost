package generic

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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
		t.Run("StreamNonSSEBilledByBodyBytes", testStreamNonSSEBilledByBodyBytes)
		t.Run("StreamSSEBilledByBodyChars", testStreamSSEBilledByBodyChars)
		t.Run("UnsupportedOperations", testUnsupportedOperations)
		t.Run("AllowedRequestsGate", testAllowedRequestsGate)
		t.Run("SigV4SignsRequest", testSigV4SignsRequest)
		t.Run("SigV4MissingRegionIs400", testSigV4MissingRegion)
		t.Run("SigV4DropsCallerSignatureHeaders", testSigV4DropsCallerSignatureHeaders)
		t.Run("RedirectsAreReturnedNotFollowed", testRedirectsAreReturnedNotFollowed)
		t.Run("TransportErrorRedactsQueryCredential", testTransportErrorRedactsQueryCredential)
		t.Run("UnitPricingEndToEnd", testUnitPricingEndToEnd)
		t.Run("IgnoreProviderCostKeepsOperatorFees", testIgnoreProviderCostKeepsOperatorFees)
	})
}

// ignore_provider_cost drops what the upstream reports and nothing else: the operator's flat fee
// and unit pricing are not provider-reported and keep applying, on both relay paths.
func testIgnoreProviderCostKeepsOperatorFees(t *testing.T) {
	cfg := &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{
		CostPath: "usage.cost", CostPerRequest: schemas.Ptr(0.01), UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.002),
	}}
	build := func(up *upstream) *GenericProvider {
		provider, err := NewGenericProvider(&schemas.ProviderConfig{
			NetworkConfig:        schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5},
			IgnoreProviderCost:   true,
			CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "echo", BaseProviderType: schemas.Generic, GenericConfig: cfg},
		}, &testLogger{})
		require.NoError(t, err)
		return provider
	}

	up := jsonUpstream(t, http.StatusOK, `{"pages":3,"usage":{"cost":99}}`)
	resp, bifrostErr := build(up).Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/scan", "", `{}`, nil))
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.PassthroughUsage)
	require.NotNil(t, resp.PassthroughUsage.Cost)
	assert.InDelta(t, 0.01+3*0.002, resp.PassthroughUsage.Cost.TotalCost, 1e-9, "the upstream's 99 is ignored, the fee and units are not")

	sse := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"pages\":2,\"usage\":{\"cost\":99}}\n\n"))
	})
	ch, bifrostErr := build(sse).PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/scan/stream", "", `{}`, nil))
	require.Nil(t, bifrostErr)
	_, usage := drainStream(t, ch)
	require.NotNil(t, usage)
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.01+2*0.002, usage.Cost.TotalCost, 1e-9)

	// Without the flag the upstream cost is read as before.
	trusting, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "echo", BaseProviderType: schemas.Generic, GenericConfig: cfg},
	}, &testLogger{})
	require.NoError(t, err)
	resp, bifrostErr = trusting.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/scan", "", `{}`, nil))
	require.Nil(t, bifrostErr)
	assert.InDelta(t, 99+0.01+3*0.002, resp.PassthroughUsage.Cost.TotalCost, 1e-9)
}

// Per-unit pricing through the relay: a Textract-shaped response priced per page, a
// Translate-shaped request priced per character, and a TTS-shaped SSML request with an audio
// response priced per body byte, on both the buffered and the streaming paths.
func testUnitPricingEndToEnd(t *testing.T) {
	t.Run("response pages", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{"Blocks":[],"DocumentMetadata":{"Pages":3}}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: schemas.Ptr(0.0015)}})
		resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{"Document":{"Bytes":"aGk="}}`, nil))
		require.Nil(t, bifrostErr)
		require.NotNil(t, resp.PassthroughUsage)
		require.NotNil(t, resp.PassthroughUsage.Cost)
		assert.InDelta(t, 0.0045, resp.PassthroughUsage.Cost.TotalCost, 1e-9)
	})
	t.Run("request characters", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{"TranslatedText":"hallo"}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(0.001)}})
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
		provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsBodyBytes, CostPerUnit: schemas.Ptr(0.01)}})
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
	// The upstream echoes the request query into Location (a trailing-slash redirect does this on
	// most frameworks) and the key on its own in a second header.
	origin := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if k := r.URL.Query().Get("api_key"); k != "" {
			w.Header().Set("X-Echo-Key", "key="+k)
		}
		http.Redirect(w, r, elsewhere.server.URL+"/landing?"+r.URL.RawQuery, http.StatusTemporaryRedirect)
	})

	for _, tc := range []struct {
		name string
		cfg  *schemas.GenericProviderConfig
	}{
		{"header auth", &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "Ocp-Apim-Subscription-Key"}}},
		{"query auth", &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "api_key"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newProvider(t, origin, tc.cfg)
			resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("SECRET"), relayRequest(http.MethodPost, "/x", "q=x", `{}`, nil))
			require.Nil(t, bifrostErr)
			assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode, "the 3xx reaches the caller")
			assert.Equal(t, elsewhere.server.URL+"/landing?q=x", resp.Headers["Location"], "the caller's query survives, the injected key does not")
			assert.Empty(t, elsewhere.requests(), "the redirect target never sees a request, let alone the key")
			for name, hv := range resp.Headers {
				assert.NotContains(t, hv, "SECRET", "header %s", name)
			}
			if tc.cfg.Auth.Type == schemas.GenericAuthQuery {
				assert.Equal(t, "[redacted]", resp.Headers["X-Echo-Key"], "a non-URL carrier of the key is replaced wholesale")
			}

			ch, bifrostErr := provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("SECRET"), relayRequest(http.MethodPost, "/x/stream", "q=x", `{}`, nil))
			require.Nil(t, bifrostErr)
			first, ok := nextChunk(t, ch, 3*time.Second)
			require.True(t, ok)
			require.NotNil(t, first.BifrostPassthroughResponse)
			assert.Equal(t, http.StatusTemporaryRedirect, first.BifrostPassthroughResponse.StatusCode, "the streaming relay behaves the same")
			assert.Equal(t, elsewhere.server.URL+"/landing?q=x", first.BifrostPassthroughResponse.Headers["Location"])
			for name, hv := range first.BifrostPassthroughResponse.Headers {
				assert.NotContains(t, hv, "SECRET", "stream header %s", name)
			}
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
			GenericConfig:     &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "api_key"}},
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
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "textract"}}})

	body := `{"Document":{"Bytes":"aGk="}}`
	resp, bifrostErr := provider.Passthrough(testCtx(t), sigv4Key("us-east-1"), relayRequest(http.MethodPost, "/", "", body, map[string]string{"x-amz-target": "Textract.DetectDocumentText", "content-type": "application/x-amz-json-1.1"}))
	require.Nil(t, bifrostErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	got := up.requests()
	require.Len(t, got, 1)
	auth := got[0].Header.Get("Authorization")
	assert.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/"), auth)
	assert.Contains(t, auth, "/us-east-1/textract/aws4_request", "scope follows the key's region and the configured service")
	assert.NotEmpty(t, got[0].Header.Get("X-Amz-Date"))
	assert.Equal(t, "Textract.DetectDocumentText", got[0].Header.Get("X-Amz-Target"), "the operation header the caller sent is kept and signed")
	assert.Equal(t, "application/x-amz-json-1.1", got[0].Header.Get("Content-Type"))
	assert.Equal(t, body, got[0].Body)

	t.Run("region falls back to the key", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "translate"}}})
		_, bifrostErr := provider.Passthrough(testCtx(t), sigv4Key("eu-west-1"), relayRequest(http.MethodPost, "/", "", `{}`, nil))
		require.Nil(t, bifrostErr)
		got := up.requests()
		require.Len(t, got, 1)
		assert.Contains(t, got[0].Header.Get("Authorization"), "/eu-west-1/translate/aws4_request")
	})

	t.Run("a region in the provider config is not read", func(t *testing.T) {
		var cfg schemas.GenericProviderConfig
		require.NoError(t, json.Unmarshal([]byte(`{"skip_route_check":true,"auth":{"type":"aws_sigv4","aws":{"service":"translate","region":"eu-west-1"}}}`), &cfg))
		up := jsonUpstream(t, http.StatusOK, `{}`)
		provider := newProvider(t, up, &cfg)
		_, bifrostErr := provider.Passthrough(testCtx(t), sigv4Key("us-east-1"), relayRequest(http.MethodPost, "/", "", `{}`, nil))
		require.Nil(t, bifrostErr)
		got := up.requests()
		require.Len(t, got, 1)
		assert.Contains(t, got[0].Header.Get("Authorization"), "/us-east-1/translate/aws4_request", "the key's region signs the request")
	})
}

func testSigV4MissingRegion(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "textract"}}})
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
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "textract"}}})
	_, bifrostErr := provider.Passthrough(testCtx(t), sigv4Key("us-east-1"), relayRequest(http.MethodPost, "/", "", `{}`, map[string]string{
		"x-amz-date": "20200101T000000Z", "x-amz-security-token": "caller-token", "x-amz-content-sha256": "deadbeef", "authorization": "AWS4-HMAC-SHA256 Credential=CALLER/...",
		"x-amz-target": "Textract.AnalyzeDocument", "x-amz-copy-source": "/other-bucket/secret.csv", "x-amz-acl": "public-read",
	}))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Header.Get("X-Amz-Copy-Source"), "a caller's x-amz-* header is not signed with the operator's credentials")
	assert.Empty(t, got[0].Header.Get("X-Amz-Acl"))
	assert.NotEqual(t, "20200101T000000Z", got[0].Header.Get("X-Amz-Date"))
	assert.Empty(t, got[0].Header.Get("X-Amz-Security-Token"), "no session token on the key means none upstream")
	assert.NotEqual(t, "deadbeef", got[0].Header.Get("X-Amz-Content-Sha256"))
	assert.Contains(t, got[0].Header.Get("Authorization"), "Credential=AKIAIOSFODNN7EXAMPLE/")
	assert.Equal(t, "Textract.AnalyzeDocument", got[0].Header.Get("X-Amz-Target"))

	// An x-amz-* header the operator configured is the operator's, and is sent.
	operator, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5, ExtraHeaders: map[string]schemas.SecretVar{"X-Amz-Server-Side-Encryption": *schemas.NewSecretVar("aws:kms")}},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "s3", BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{
			SkipRouteCheck: true,
			Auth:           schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "s3"}},
		}},
	}, &testLogger{})
	require.NoError(t, err)
	_, bifrostErr = operator.Passthrough(testCtx(t), sigv4Key("us-east-1"), &schemas.BifrostPassthroughRequest{Method: http.MethodPut, Path: "/obj", Body: []byte(`x`), SafeHeaders: map[string]string{}})
	require.Nil(t, bifrostErr)
	got = up.requests()
	require.Len(t, got, 2)
	assert.Equal(t, "aws:kms", got[1].Header.Get("X-Amz-Server-Side-Encryption"))
	assert.Empty(t, got[1].Header.Get("Content-Type"), "a relayed request is signed as sent: no JSON default is invented")
	assert.Empty(t, got[1].Header.Get("Accept"))
	assert.Equal(t, "x", got[1].Body, "the body is rewound after hashing and reaches the upstream intact")
}

func testRelayPOSTBearer(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{"results":[1,2],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{InputTokensPath: "usage.prompt_tokens", OutputTokensPath: "usage.completion_tokens", TotalTokensPath: "usage.total_tokens"}})

	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("sk-upstream"), relayRequest(http.MethodPost, "/search", "", `{"query":"bifrost"}`, map[string]string{"x-trace": "abc"}))
	require.Nil(t, bifrostErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"results":[1,2],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`, string(resp.Body))
	assert.Equal(t, "up-1", resp.Headers["X-Upstream-Id"], "upstream headers reach the caller")
	assert.Equal(t, "/search", resp.ExtraFields.PassthroughPath)
	require.NotNil(t, resp.PassthroughUsage, "a billable 2xx always reports usage")
	require.NotNil(t, resp.PassthroughUsage.LLMUsage, "the mapped token paths read the OpenAI usage shape")
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
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "api_key"}})

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
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "Ocp-Apim-Subscription-Key", ValuePrefix: "key-"}})

	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("abc"), relayRequest(http.MethodPost, "/cognitiveservices/v1", "", `<speak/>`, nil))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.Equal(t, "key-abc", got[0].Header.Get("Ocp-Apim-Subscription-Key"))
	assert.Empty(t, got[0].Header.Get("Authorization"))
}

func testBasicAuth(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthBasic}})

	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("user:pass"), relayRequest(http.MethodGet, "/me", "", "", nil))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")), got[0].Header.Get("Authorization"))
}

func testNoneAuthKeyless(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthNone}})

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
		provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "x-api-key"}})
		_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("real"), relayRequest(http.MethodPost, "/post", "", `{}`, map[string]string{"X-Api-Key": "caller-key", "authorization": "Bearer caller"}))
		require.Nil(t, bifrostErr)
		got := up.requests()
		require.Len(t, got, 1)
		assert.Equal(t, "real", got[0].Header.Get("X-Api-Key"))
		assert.Empty(t, got[0].Header.Get("Authorization"))
	})
	t.Run("query", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "key"}})
		_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("real"), relayRequest(http.MethodGet, "/get", "key=caller&q=1", "", nil))
		require.Nil(t, bifrostErr)
		got := up.requests()
		require.Len(t, got, 1)
		assert.Equal(t, "q=1&key=real", got[0].RawQuery)

		_, bifrostErr = provider.Passthrough(testCtx(t), bearerKey("real"), relayRequest(http.MethodGet, "/get", "z=1&key=caller&q=%zz", "", nil))
		require.Nil(t, bifrostErr, "a value that does not decode is forwarded as sent; the credential is still stripped")
		got = up.requests()
		require.Len(t, got, 2)
		assert.Equal(t, "z=1&q=%zz&key=real", got[1].RawQuery, "other segments keep their order and bytes")

		_, bifrostErr = provider.Passthrough(testCtx(t), bearerKey("real"), relayRequest(http.MethodGet, "/get", "ke%zzy=caller&q=1", "", nil))
		require.NotNil(t, bifrostErr, "a parameter name that cannot be decoded is refused")
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
		assert.Len(t, up.requests(), 2, "nothing reached the upstream with the caller's credential")
	})
}

func testHostPinning(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true})
	for _, path := range []string{"//evil.example/x", "@evil.example/x", "https://evil.example/x"} {
		_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodGet, path, "", "", nil))
		require.NotNil(t, bifrostErr, "path %q must be refused", path)
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	}
	assert.Empty(t, up.requests(), "nothing reached the upstream")
}

func testNon2xxReturnsNilUsage(t *testing.T) {
	up := jsonUpstream(t, http.StatusTooManyRequests, `{"error":"slow down","usage":{"total_tokens":5}}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01)}})

	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/search", "", `{}`, nil))
	require.Nil(t, bifrostErr, "upstream errors are relayed, not converted")
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Nil(t, resp.PassthroughUsage, "a failed call is not charged")
}

func testNonBillableRoute(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{"status":"done"}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{
		SkipRouteCheck: true,
		Usage:          &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01)},
		Routes:         []schemas.GenericRouteConfig{{Method: "GET", Path: "/jobs/*", Billable: schemas.Ptr(false)}},
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
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true})
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
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{InputTokensPath: "usage.input_tokens", OutputTokensPath: "usage.output_tokens", CostPerRequest: schemas.Ptr(0.002)}})

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
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.5)}})

	ch, bifrostErr := provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/tts/stream", "", `{"text":"hi"}`, nil))
	require.Nil(t, bifrostErr)
	body, usage := drainStream(t, ch)
	assert.Equal(t, "ID3\x00\x00binary", body)
	require.NotNil(t, usage, "DefaultUsage lands on the final chunk when nothing was observed")
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.5, usage.Cost.TotalCost, 1e-9)
}

// A reply priced by its size bills a stream the same as a buffered reply: the bytes are counted as
// they are forwarded and charged on the final chunk, on top of the fee.
func testStreamNonSSEBilledByBodyBytes(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ID3\x00\x00bin"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("ary-tail"))
	})
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{
		CostPerRequest: schemas.Ptr(0.5), UnitsSource: schemas.GenericUnitsFromResponse, UnitsMeasure: schemas.GenericUnitsBodyBytes, CostPerUnit: schemas.Ptr(0.001),
	}})

	ch, bifrostErr := provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/tts/stream", "", `{"text":"hi"}`, nil))
	require.Nil(t, bifrostErr)
	body, usage := drainStream(t, ch)
	assert.Equal(t, "ID3\x00\x00binary-tail", body)
	require.NotNil(t, usage)
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.5+0.001*float64(len(body)), usage.Cost.TotalCost, 1e-9, "fee plus every forwarded byte")
}

// Characters are counted as runes across chunk boundaries, and tokens an SSE event reports still
// land in the same usage.
func testStreamSSEBilledByBodyChars(t *testing.T) {
	first := "data: {\"delta\":\"h\xc3\xa9llo \xe2\x82"
	second := "\xac\"}\n\ndata: {\"usage\":{\"input_tokens\":2,\"output_tokens\":5}}\n\ndata: [DONE]\n\n"
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(first))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(second))
	})
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{
		InputTokensPath: "usage.input_tokens", OutputTokensPath: "usage.output_tokens",
		UnitsSource: schemas.GenericUnitsFromResponse, UnitsMeasure: schemas.GenericUnitsBodyChars, CostPerUnit: schemas.Ptr(0.01),
	}})

	ch, bifrostErr := provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/chat/stream", "", `{"stream":true}`, nil))
	require.Nil(t, bifrostErr)
	body, usage := drainStream(t, ch)
	assert.Equal(t, first+second, body)
	require.NotNil(t, usage)
	require.NotNil(t, usage.LLMUsage)
	assert.Equal(t, 7, usage.LLMUsage.TotalTokens, "tokens from the events are kept")
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.01*float64(utf8.RuneCount([]byte(body))), usage.Cost.TotalCost, 1e-9, "runes of the whole stream, split rune counted once")
}

func testUnsupportedOperations(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true})
	assert.Equal(t, schemas.ModelProvider("echo"), provider.GetProviderKey())

	_, bifrostErr := provider.ChatCompletion(testCtx(t), bearerKey("k"), &schemas.BifrostChatRequest{Model: "x"})
	require.NotNil(t, bifrostErr)
	assert.Equal(t, "unsupported_operation", *bifrostErr.Error.Code)
	assert.Contains(t, bifrostErr.Error.Message, "echo provider", "the error names the provider")

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
			GenericConfig:     &schemas.GenericProviderConfig{SkipRouteCheck: true},
		},
	}, &testLogger{})
	require.NoError(t, err)

	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodGet, "/x", "", "", nil))
	require.Nil(t, bifrostErr)
	_, bifrostErr = provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodGet, "/x", "", "", nil))
	require.NotNil(t, bifrostErr, "passthrough_stream is off for this provider")
	assert.Equal(t, "unsupported_operation", *bifrostErr.Error.Code)
}

// A billable route that reads its quantity in the caller's body refuses an ambiguous or non-JSON
// body with 400 before anything is sent, on both relay paths; a JSON body labelled text/plain is
// still measured.
func TestGenericRequestBilledBodyMustBeUnambiguous(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Routes: []schemas.GenericRouteConfig{{
		Method: "POST", Path: "/translate",
		Usage: &schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(0.001)},
	}}})
	dup := relayRequest(http.MethodPost, "/translate", "", `{"Text":"a","Text":"`+strings.Repeat("b", 50)+`"}`, map[string]string{"content-type": "application/json"})
	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), dup)
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	_, bifrostErr = provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), dup)
	require.NotNil(t, bifrostErr)
	assert.Empty(t, up.requests(), "a refused body never reaches the upstream")

	plain := relayRequest(http.MethodPost, "/translate", "", `{"Text":"hello"}`, map[string]string{"content-type": "text/plain"})
	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), plain)
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.PassthroughUsage.Cost)
	assert.InDelta(t, 0.005, resp.PassthroughUsage.Cost.TotalCost, 1e-9, "five characters billed whatever the declared content type")
}

func TestNewGenericProviderRequiresBaseURL(t *testing.T) {
	_, err := NewGenericProvider(&schemas.ProviderConfig{CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Generic}}, &testLogger{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base_url")

	_, err = NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: "https://api.example.com"},
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: "magic"}}},
	}, &testLogger{})
	require.Error(t, err, "an invalid relay config is refused at construction")
}

// generic is a base type only: a provider config that does not name it as the custom provider's
// base type is refused by the constructor, like every provider validates its own config.
func TestNewGenericProviderRequiresGenericBaseType(t *testing.T) {
	network := schemas.NetworkConfig{BaseURL: "https://api.example.com"}
	_, err := NewGenericProvider(&schemas.ProviderConfig{NetworkConfig: network}, &testLogger{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base type")

	_, err = NewGenericProvider(&schemas.ProviderConfig{NetworkConfig: network, CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI}}, &testLogger{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base type")
}

// A route with a job block attaches the job id and the settlement snapshot to the start call's
// usage, so the logging plugin can record the job; the start call is still counted and charged
// as the route says. A start response that names no id starts no job.
// An operator's extra header is the operator's: a caller sending the same name does not replace
// it, while any other caller header is forwarded.
func TestGenericOperatorHeadersWinOverCaller(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5, ExtraHeaders: map[string]schemas.SecretVar{"X-Tenant": *schemas.NewSecretVar("ops")}},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "echo", BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{SkipRouteCheck: true}},
	}, &testLogger{})
	require.NoError(t, err)
	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/search", "", `{}`, map[string]string{"x-tenant": "evil", "x-trace": "abc"}))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.Equal(t, "ops", got[0].Header.Get("X-Tenant"), "the operator's value stands")
	assert.Equal(t, "abc", got[0].Header.Get("X-Trace"), "other caller headers are forwarded")
}

// x-bf-eh-* headers reach the context, not the caller header map the routes are matched on, so the
// generic relay does not apply them: a prefixed X-Amz-Target would run an operation the plan never
// approved, and a prefixed copy of an operator header would replace the operator's value.
func TestGenericIgnoresContextExtraHeaders(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5, ExtraHeaders: map[string]schemas.SecretVar{"X-Tenant": *schemas.NewSecretVar("ops")}},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "echo", BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{
			Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}}},
		}},
	}, &testLogger{})
	require.NoError(t, err)
	ctx := testCtx(t)
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{"x-amz-target": {"Textract.AnalyzeDocument"}, "x-tenant": {"evil"}})
	_, bifrostErr := provider.Passthrough(ctx, bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{}`, map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis"}))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.Equal(t, "Textract.GetDocumentAnalysis", got[0].Header.Get("X-Amz-Target"), "the operation the route approved is the one sent")
	assert.Equal(t, "ops", got[0].Header.Get("X-Tenant"), "the operator's value stands")
}

// A Go SDK caller can put one header in SafeHeaders under two spellings. Route matching reads one and
// forwarding could send the other, so an allowlisted operation would be approved and billed while
// another ran; the request is refused before anything is sent.
func TestGenericRefusesHeaderUnderTwoSpellings(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "echo", BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{
			Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}}},
		}},
	}, &testLogger{})
	require.NoError(t, err)
	headers := map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis", "X-Amz-Target": "Textract.AnalyzeDocument"}
	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{}`, headers))
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	_, bifrostErr = provider.PassthroughStream(testCtx(t), nil, nil, bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{}`, headers))
	require.NotNil(t, bifrostErr, "the streaming relay refuses it too")
	assert.Empty(t, up.requests(), "nothing reaches the upstream")
}

// Two spellings of one operator header would let routes match one value while the other is sent.
func TestNewGenericProviderRefusesExtraHeaderSpellings(t *testing.T) {
	_, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "https://example.com", ExtraHeaders: map[string]schemas.SecretVar{
			"X-Amz-Target": *schemas.NewSecretVar("Textract.GetDocumentAnalysis"),
			"x-amz-target": *schemas.NewSecretVar("Textract.AnalyzeDocument"),
		}},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "echo", BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{SkipRouteCheck: true}},
	}, &testLogger{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two spellings")
}

// The route check holds on the Go SDK path too: the provider refuses an unlisted operation with 404
// before anything is sent, the same way the HTTP transport does.
func TestGenericRouteCheckRefusedBeforeAuth(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{
		Auth:   schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "textract"}},
		Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/listed"}},
	})
	_, bifrostErr := provider.Passthrough(testCtx(t), sigv4Key(""), relayRequest(http.MethodPost, "/other", "", `{}`, nil))
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusNotFound, *bifrostErr.StatusCode, "an unlisted route is a 404 even when the key has no region to sign with")
}

// The routes list is an allowlist by default: an unlisted operation is a 404 and never sent.
func TestGenericRouteCheckOnProvider(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/search"}}})
	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/other", "", `{}`, nil))
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusNotFound, *bifrostErr.StatusCode)
	_, bifrostErr = provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/other", "", `{}`, nil))
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusNotFound, *bifrostErr.StatusCode)
	assert.Empty(t, up.requests(), "nothing reached the upstream")
	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/search", "", `{}`, nil))
	require.Nil(t, bifrostErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a listed operation is relayed")

	t.Run("skip_route_check relays an unlisted operation", func(t *testing.T) {
		up := jsonUpstream(t, http.StatusOK, `{}`)
		provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/search"}}})
		resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/other", "", `{}`, nil))
		require.Nil(t, bifrostErr)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Len(t, up.requests(), 1)
	})
}

// A job accounting poll (an internal request, run with the plugin pipeline skipped) reaches the
// upstream even when the poll operation is not a listed route: the allowlist
// is for callers, and the poll is the operator's own request.
func TestGenericInternalPollBypassesRouteCheck(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{"JobStatus":"SUCCEEDED"}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/start"}}})
	poll := relayRequest(http.MethodGet, "/jobs/42", "", "", nil)
	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), poll)
	require.NotNil(t, bifrostErr, "a caller's unlisted operation is still refused")
	assert.Equal(t, http.StatusNotFound, *bifrostErr.StatusCode)
	ctx := testCtx(t)
	ctx.SetValue(schemas.BifrostContextKeyInternalJobPoll, true)
	resp, bifrostErr := provider.Passthrough(ctx, bearerKey("k"), poll)
	require.Nil(t, bifrostErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the sweeper's poll is relayed")
	assert.Len(t, up.requests(), 1)

	// Nor do the caller billing checks: a bodyless poll on a provider billed per request character
	// would otherwise be refused with 400 and the job parked unpriced.
	billed := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(0.001)}})
	_, bifrostErr = billed.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/poll", "", "", nil))
	require.NotNil(t, bifrostErr, "a caller's unmeasurable body is refused")
	resp, bifrostErr = billed.Passthrough(ctx, bearerKey("k"), relayRequest(http.MethodPost, "/poll", "", "", nil))
	require.Nil(t, bifrostErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestGenericJobStartHandle(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{"JobId":"job-42"}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{
		SkipRouteCheck: true,
		Usage:          &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01), UnitsPath: "DocumentMetadata.Pages", CostPerUnit: schemas.Ptr(0.0015)},
		Routes: []schemas.GenericRouteConfig{{
			Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Start*"},
			Usage: &schemas.GenericUsageConfig{CostPerUnit: schemas.Ptr(0.0)},
			Job: &schemas.GenericJobConfig{
				IDPath:          "JobId",
				Poll:            schemas.GenericJobPoll{Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"}, Body: `{"JobId":"{{job.id}}"}`},
				StatusPath:      "JobStatus",
				Succeeded:       []string{"SUCCEEDED"},
				Usage:           &schemas.GenericUsageConfig{CostPerUnit: schemas.Ptr(0.0015)},
				IntervalSeconds: 15,
			},
		}},
	})
	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{"DocumentLocation":{}}`, map[string]string{"x-amz-target": "Textract.StartDocumentAnalysis", "content-type": "application/x-amz-json-1.1"}))
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.PassthroughUsage, "the start call is counted")
	require.NotNil(t, resp.PassthroughUsage.Cost)
	assert.InDelta(t, 0.01, resp.PassthroughUsage.Cost.TotalCost, 1e-9, "the start call pays the flat fee and no units")
	handle := resp.PassthroughUsage.ProviderJob
	require.NotNil(t, handle, "the job handle rides on the usage")
	assert.Equal(t, "job-42", handle.ID)
	assert.Equal(t, 15, handle.IntervalSeconds)
	snap, err := ParseJobSnapshot(handle.Params)
	require.NoError(t, err)
	assert.Equal(t, "JobStatus", snap.Job.StatusPath)
	assert.Equal(t, []string{"SUCCEEDED"}, snap.Job.Succeeded)
	require.NotNil(t, snap.Usage)
	assert.Equal(t, "DocumentMetadata.Pages", snap.Usage.UnitsPath, "the provider's units path is merged into the snapshot")
	assert.InDelta(t, 0.0015, *snap.Usage.CostPerUnit, 0, "the job's own rate wins over the route's zero")
	assert.Nil(t, snap.Job.Usage, "the job usage is folded in, not carried twice")
	poll := snap.PollRequest("job-42")
	assert.Equal(t, "Textract.GetDocumentAnalysis", poll.Headers["x-amz-target"])
	assert.JSONEq(t, `{"JobId":"job-42"}`, string(poll.Body))
	assert.Equal(t, "/", poll.Path)
	assert.Empty(t, poll.RawQuery)
	// A poll path that carries its parameters in the query (a GET status endpoint) is split so the
	// relay sends them as a query and not as a re-encoded ? inside the path.
	byQuery := JobSnapshot{Job: schemas.GenericJobConfig{Poll: schemas.GenericJobPoll{Method: "GET", Path: "/jobs/status?id={{job.id}}&verbose=true"}}}
	poll = byQuery.PollRequest("job-42")
	assert.Equal(t, "GET", poll.Method)
	assert.Equal(t, "/jobs/status", poll.Path)
	assert.Equal(t, "id=job-42&verbose=true", poll.RawQuery)
	assert.Nil(t, poll.Body)
	// The id is escaped for where it lands, so it cannot change the job polled or break the body.
	assert.Equal(t, "id=a%2Bb%26x%3D1&verbose=true", byQuery.PollRequest("a+b&x=1").RawQuery)
	quoted := JobSnapshot{Job: schemas.GenericJobConfig{Poll: schemas.GenericJobPoll{Path: "/", Body: `{"JobId":"{{job.id}}"}`}}}
	assert.JSONEq(t, `{"JobId":"a\"b"}`, string(quoted.PollRequest(`a"b`).Body))

	// A Go SDK caller cannot stream a job route: only the buffered relay reads the job id.
	_, bifrostErr = provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{}`, map[string]string{"x-amz-target": "Textract.StartDocumentAnalysis"}))
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	usage, detail := snap.Price("job-42", "SUCCEEDED", "application/x-amz-json-1.1", []byte(`{"JobStatus":"SUCCEEDED","DocumentMetadata":{"Pages":7}}`))
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 7*0.0015+0.01, usage.Cost.TotalCost, 1e-9, "settlement prices pages at the job rate plus the inherited fee")
	assert.InDelta(t, 7, *detail.Units, 0)

	// A caller cannot move a job route onto the streaming relay, which never reads the job id.
	withStreams := provider.cfg.Clone()
	withStreams.Streaming = &schemas.GenericStreamingConfig{DetectFromAcceptHeader: true, DetectFromBody: true, Paths: []string{"/**"}}
	streamer := newProvider(t, up, withStreams)
	assert.False(t, streamer.Plan(http.MethodPost, "/", "text/event-stream", true, map[string]string{"x-amz-target": "Textract.StartDocumentAnalysis"}).Streams)

	noID := newProvider(t, jsonUpstream(t, http.StatusOK, `{"Message":"accepted"}`), provider.cfg)
	resp, bifrostErr = noID.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{}`, map[string]string{"x-amz-target": "Textract.StartDocumentAnalysis", "content-type": "application/x-amz-json-1.1"}))
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.PassthroughUsage)
	assert.Nil(t, resp.PassthroughUsage.ProviderJob, "no job id in the response: no job")
}

func TestGenericStreams(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{
		SkipRouteCheck: true,
		Streaming:      &schemas.GenericStreamingConfig{Paths: []string{"/v1/live/**"}, DetectFromAcceptHeader: true},
		Routes: []schemas.GenericRouteConfig{
			{Method: "POST", Path: "/v1/stream-but-not", Stream: schemas.Ptr(false)},
			{Method: "POST", Path: "/v1/events", Stream: schemas.Ptr(true)},
		},
	})
	assert.False(t, provider.Plan("POST", "/v1/stream-but-not", "", true, nil).Streams, "a route's stream flag wins over every heuristic")
	assert.True(t, provider.Plan("POST", "/v1/events", "", false, nil).Streams)
	assert.True(t, provider.Plan("GET", "/v1/live/room/1", "", false, nil).Streams, "streaming.paths glob")
	assert.True(t, provider.Plan("POST", "/v1/chat", "text/event-stream", false, nil).Streams, "Accept header when enabled")
	assert.False(t, provider.Plan("POST", "/v1/chat", "", true, nil).Streams, "a caller's stream flag is not a signal unless the operator opted in: the streaming relay skips response-side billing")
	optIn := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Streaming: &schemas.GenericStreamingConfig{DetectFromBody: true}})
	assert.True(t, optIn.Plan("POST", "/v1/chat", "", true, nil).Streams, "body stream flag when enabled")
	assert.False(t, optIn.Plan("POST", "/v1/chat", "", false, nil).Streams)
	assert.False(t, provider.Plan("POST", "/v1/chat/stream", "", false, nil).Streams, "the path's wording is not a signal: a non-SSE reply would be forwarded unread and under-billed")
	assert.False(t, provider.Plan("GET", "/v1/streams/123", "", false, nil).Streams)
	assert.False(t, provider.Plan("POST", "/v1/chat", "application/json", false, nil).Streams)
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

// BenchmarkGenericPassthroughSigV4 is the unary relay with aws_sigv4 auth and static credentials:
// the bearer benchmark above plus SigV4 signing of a small JSON body.
func BenchmarkGenericPassthroughSigV4(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	b.Cleanup(server.Close)
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 5},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "bench", BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{
			SkipRouteCheck: true,
			Auth:           schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "textract"}},
		}},
	}, &testLogger{})
	if err != nil {
		b.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	b.Cleanup(ctx.Cancel)
	key := sigv4Key("us-east-1")
	req := relayRequest(http.MethodPost, "/", "", `{"Document":{"Bytes":"aGVsbG8="}}`, map[string]string{"content-type": "application/x-amz-json-1.1", "x-amz-target": "Textract.DetectDocumentText"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, bifrostErr := provider.Passthrough(ctx, key, req); bifrostErr != nil {
			b.Fatal(bifrostErr.Error.Message)
		}
	}
}

// BenchmarkGenericRoutePlan measures one request's route matching against a routes list the size
// an OpenAPI import produces. Plan runs once per request in the transport and the relay reuses it
// through the context, so this is the whole per-request matching cost.
func BenchmarkGenericRoutePlan(b *testing.B) {
	cfg := &schemas.GenericProviderConfig{}
	for i := 0; i < 200; i++ {
		cfg.Routes = append(cfg.Routes, schemas.GenericRouteConfig{Method: "POST", Path: fmt.Sprintf("/v1/resource%d/*/action", i)})
	}
	provider := &GenericProvider{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(nil, nil)}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	b.Cleanup(ctx.Cancel)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		plan := provider.Plan("POST", "/v1/resource199/42/action", "", false, nil)
		if !plan.Allowed {
			b.Fatal("route must match")
		}
		WithPlan(ctx, plan)
		if _, _, route := provider.resolve(ctx, "POST", "/v1/resource199/42/action", nil); route == nil {
			b.Fatal("plan must be reused")
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

// x-bf-eh-* headers land in the context, not in the caller headers the relay filters, so the generic
// relay does not apply them: a prefixed credential header would reach the upstream, and a prefixed
// copy of an operator header would replace the operator's value.
func TestGenericSkipsContextExtraHeaders(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5, ExtraHeaders: map[string]schemas.SecretVar{"X-Tenant": *schemas.NewSecretVar("ops")}},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "echo", BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{SkipRouteCheck: true}},
	}, &testLogger{})
	require.NoError(t, err)
	ctx := testCtx(t)
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{"x-tenant": {"evil"}, "ocp-apim-subscription-key": {"caller"}})
	_, bifrostErr := provider.Passthrough(ctx, bearerKey("k"), relayRequest(http.MethodPost, "/search", "", `{}`, nil))
	require.Nil(t, bifrostErr)
	got := up.requests()
	require.Len(t, got, 1)
	assert.Equal(t, "ops", got[0].Header.Get("X-Tenant"), "the operator's value stands")
	assert.Empty(t, got[0].Header.Get("Ocp-Apim-Subscription-Key"), "a prefixed caller credential never reaches the upstream")
}

// An Accept-Encoding the operator or a Go SDK caller sets explicitly would stop net/http from
// decoding the reply, so the caller would get gzip bytes with Content-Encoding stripped and usage
// would read nothing. The relay drops it, so Go negotiates and decodes on both relays.
func TestGenericDecodesCompressedReplies(t *testing.T) {
	const reply = `{"usage":{"total_tokens":5}}`
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			_, _ = w.Write([]byte(reply))
			return
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write([]byte(reply))
		_ = zw.Close()
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buf.Bytes())
	})
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5, ExtraHeaders: map[string]schemas.SecretVar{"Accept-Encoding": *schemas.NewSecretVar("gzip")}},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "echo", BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{SkipRouteCheck: true, Usage: &schemas.GenericUsageConfig{TotalTokensPath: "usage.total_tokens"}}},
	}, &testLogger{})
	require.NoError(t, err)

	resp, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/chat", "", `{}`, map[string]string{"accept-encoding": "gzip"}))
	require.Nil(t, bifrostErr)
	assert.JSONEq(t, reply, string(resp.Body), "the caller gets the decoded body")
	require.NotNil(t, resp.PassthroughUsage)
	require.NotNil(t, resp.PassthroughUsage.LLMUsage)
	assert.Equal(t, 5, resp.PassthroughUsage.LLMUsage.TotalTokens, "usage is read from the decoded body")

	ch, bifrostErr := provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("k"), relayRequest(http.MethodPost, "/chat", "", `{}`, map[string]string{"accept-encoding": "gzip"}))
	require.Nil(t, bifrostErr)
	streamed, _ := drainStream(t, ch)
	assert.JSONEq(t, reply, streamed, "the streaming relay forwards decoded bytes too")
}

// A malformed request URL (a Go SDK caller can pass any path) is refused before the query
// credential is added, so the error the caller sees never carries the key.
func TestGenericURLErrorsNeverCarryTheQueryKey(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider := newProvider(t, up, &schemas.GenericProviderConfig{SkipRouteCheck: true, Auth: schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "api_key"}})
	_, bifrostErr := provider.Passthrough(testCtx(t), bearerKey("SECRET"), relayRequest(http.MethodPost, "/bad\npath", "q=x", `{}`, nil))
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.NotContains(t, bifrostErr.Error.Message, "SECRET")
	_, bifrostErr = provider.PassthroughStream(testCtx(t), noHooks, nil, bearerKey("SECRET"), relayRequest(http.MethodPost, "/bad\npath", "q=x", `{}`, nil))
	require.NotNil(t, bifrostErr)
	assert.NotContains(t, bifrostErr.Error.Message, "SECRET")
	assert.Empty(t, up.requests())
}

// An upstream may echo the key back under any valid percent-encoding (lower-case escapes), not
// only the spelling the relay sent; the auth parameter is removed by name either way.
func TestRedactKeyFromHeadersMatchesAnyEncoding(t *testing.T) {
	headers := map[string]string{
		"Location": "https://api.example.com/x?api_key=AbC%2b%2fw%3d%3d&y=1",
		"X-Echo":   "seen AbC%2b%2fw%3d%3d",
		"X-Plain":  "nothing here",
	}
	redactKeyFromHeaders(headers, "api_key", "AbC+/w==")
	assert.Equal(t, "https://api.example.com/x?y=1", headers["Location"], "the parameter is removed whatever its spelling")
	assert.Equal(t, "[redacted]", headers["X-Echo"], "a non-URL carrier is replaced wholesale")
	assert.Equal(t, "nothing here", headers["X-Plain"])
}

// A job poll is the operator's own configured request, sent by the sweeper with the internal
// marker: its configured headers are applied as the operator's, over extra_headers, so a poll can
// name its own operation even when extra_headers names the start one. A caller still cannot.
func TestGenericJobPollHeadersOverrideExtraHeaders(t *testing.T) {
	up := jsonUpstream(t, http.StatusOK, `{}`)
	provider, err := NewGenericProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: up.server.URL, DefaultRequestTimeoutInSeconds: 5, ExtraHeaders: map[string]schemas.SecretVar{"X-Amz-Target": *schemas.NewSecretVar("Textract.StartDocumentAnalysis")}},
		CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "echo", BaseProviderType: schemas.Generic, GenericConfig: &schemas.GenericProviderConfig{SkipRouteCheck: true}},
	}, &testLogger{})
	require.NoError(t, err)
	poll := map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis", "authorization": "Bearer poll-should-not-pass"}

	internal := testCtx(t)
	internal.SetValue(schemas.BifrostContextKeyInternalJobPoll, true)
	_, bifrostErr := provider.Passthrough(internal, bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{}`, poll))
	require.Nil(t, bifrostErr)
	_, bifrostErr = provider.Passthrough(testCtx(t), bearerKey("k"), relayRequest(http.MethodPost, "/", "", `{}`, poll))
	require.Nil(t, bifrostErr)

	got := up.requests()
	require.Len(t, got, 2)
	assert.Equal(t, "Textract.GetDocumentAnalysis", got[0].Header.Get("X-Amz-Target"), "the poll's configured operation is sent")
	assert.Equal(t, "Bearer k", got[0].Header.Get("Authorization"), "the configured key is still the only credential")
	assert.Equal(t, "Textract.StartDocumentAnalysis", got[1].Header.Get("X-Amz-Target"), "a caller cannot replace an operator header")
}
