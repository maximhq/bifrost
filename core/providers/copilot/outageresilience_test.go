package copilot

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// TestTokenManager_OutageResilienceWithLiveToken pins the outage contract against
// the real token exchange: a still-valid cached JWT must keep serving requests
// while the exchange endpoint is down, and an expired one must surface the
// upstream error instead of being handed out and failing later as an opaque 401.
//
// The stubbed 503 stands in for the exchange outage; everything else is live.
// Skipped unless GITHUB_COPILOT_TOKEN is set.
func TestTokenManager_OutageResilienceWithLiveToken(t *testing.T) {
	oauthToken := strings.TrimSpace(os.Getenv("GITHUB_COPILOT_TOKEN"))
	if oauthToken == "" {
		t.Skip("Skipping live outage test because GITHUB_COPILOT_TOKEN is not set")
	}

	tm := newCopilotTokenManager(oauthToken, &fasthttp.Client{}, nil)
	realJWT, realBase, bifrostErr := tm.getToken()
	if bifrostErr != nil {
		t.Fatalf("live token exchange failed: %s", bifrostErr.Error.Message)
	}
	if realJWT == "" {
		t.Fatal("live token exchange returned an empty JWT")
	}
	// The base is account-dependent (individual vs enterprise), so assert it is
	// trusted rather than pinning one host.
	if !isValidCopilotAPIBase(realBase) {
		t.Fatalf("live token exchange returned an untrusted API base: %q", realBase)
	}
	t.Logf("live exchange OK: api_base=%s valid_for=%s", realBase, time.Until(tm.expiresAt).Round(time.Second))

	outage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`<html>503 Service Unavailable</html>`))
	}))
	defer outage.Close()
	tm.tokenExchangeURL = outage.URL

	// Inside the refresh margin, so a refresh is attempted and fails, but the
	// cached JWT is still valid.
	tm.expiresAt = time.Now().Add(30 * time.Second)
	cachedJWT, cachedBase, bifrostErr := tm.getToken()
	if bifrostErr != nil {
		t.Fatalf("valid cached JWT should survive an exchange outage, got: %s", bifrostErr.Error.Message)
	}
	if cachedJWT != realJWT || cachedBase != realBase {
		t.Fatal("expected the cached JWT and API base to be reused during the outage")
	}

	// The cached JWT is only worth serving if the live API still accepts it.
	if status := liveCopilotModelsStatus(t, cachedBase, cachedJWT); status != http.StatusOK {
		t.Fatalf("cached JWT rejected by live /models during outage: HTTP %d", status)
	}

	tm.expiresAt = time.Now().Add(-5 * time.Minute)
	expiredToken, _, bifrostErr := tm.getToken()
	if bifrostErr == nil {
		t.Fatal("an expired JWT during an outage must surface the upstream error")
	}
	if expiredToken != "" {
		t.Error("no token should be returned alongside the error")
	}
	if *bifrostErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected the upstream 503 to surface, got %d", *bifrostErr.StatusCode)
	}
}

// liveCopilotModelsStatus issues a real /models request with the given JWT and
// returns the HTTP status.
func liveCopilotModelsStatus(t *testing.T, apiBase, jwt string) int {
	t.Helper()
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(apiBase + "/models")
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("Authorization", "Bearer "+jwt)
	for k, v := range copilotRequiredHeaders {
		req.Header.Set(k, v)
	}
	if err := (&fasthttp.Client{}).DoTimeout(req, resp, 30*time.Second); err != nil {
		t.Fatalf("live /models request failed: %v", err)
	}
	return resp.StatusCode()
}
