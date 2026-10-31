package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// fakeGithubDeviceFlow stands in for github.com's device flow endpoints and api.github.com/user.
type fakeGithubDeviceFlow struct {
	server   *httptest.Server
	pollBody atomic.Value // string, the token endpoint's next reply
	clientID atomic.Value // string, client_id on the last form
}

func newFakeGithubDeviceFlow(t *testing.T) *fakeGithubDeviceFlow {
	t.Helper()
	f := &fakeGithubDeviceFlow{}
	f.pollBody.Store(`{"error":"authorization_pending"}`)
	mux := http.NewServeMux()
	mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.clientID.Store(r.PostForm.Get("client_id"))
		_, _ = fmt.Fprint(w, `{"device_code":"dc_1","user_code":"WDJB-MJHT","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.clientID.Store(r.PostForm.Get("client_id"))
		_, _ = fmt.Fprint(w, f.pollBody.Load().(string))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"login":"octocat"}`)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGithubDeviceFlow) handler() *GithubCopilotSignInHandler {
	h := NewGithubCopilotSignInHandler()
	h.endpoints = githubSignInEndpoints{
		deviceCodeURL: f.server.URL + "/login/device/code",
		tokenURL:      f.server.URL + "/login/oauth/access_token",
		userURL:       f.server.URL + "/user",
	}
	return h
}

func signInRequest(provider, body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("provider", provider)
	ctx.Request.SetBodyString(body)
	return ctx
}

// TestGithubCopilotSignInRoutesCoexistWithKeyRoutes pins that the sign-in routes register
// beside the provider routes; a conflicting pattern panics at startup.
func TestGithubCopilotSignInRoutesCoexistWithKeyRoutes(t *testing.T) {
	r := router.New()
	NewProviderHandler(nil, &lib.Config{}, nil).RegisterRoutes(r)
	NewGithubCopilotSignInHandler().RegisterRoutes(r)
}

func TestGithubCopilotSignInRejectsOtherProviders(t *testing.T) {
	ctx := signInRequest("openai", "")
	NewGithubCopilotSignInHandler().start(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("status %d, want 400 for a non-Copilot provider: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

// TestGithubCopilotSignInUsesGlobalProxy pins that the GitHub calls go through the server's
// HTTP client factory, so an operator's global proxy for API traffic applies to sign-in.
func TestGithubCopilotSignInUsesGlobalProxy(t *testing.T) {
	fake := newFakeGithubDeviceFlow(t)
	var proxied atomic.Int32
	// The fake answers as a forward proxy: it sees the absolute GitHub URL and routes by path.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "github.invalid" {
			proxied.Add(1)
		}
		fake.server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)

	previous := network.DefaultHTTPClientFactory()
	network.SetDefaultHTTPClientFactory(network.NewHTTPClientFactory(&network.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: proxy.URL, EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { network.SetDefaultHTTPClientFactory(previous) })

	h := NewGithubCopilotSignInHandler()
	h.endpoints.deviceCodeURL = "http://github.invalid/login/device/code"
	start := signInRequest("github-copilot", "")
	h.start(start)

	if start.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("start: status %d, want 200 through the proxy: %s", start.Response.StatusCode(), start.Response.Body())
	}
	if proxied.Load() != 1 {
		t.Fatalf("proxy saw %d GitHub requests, want 1", proxied.Load())
	}
}

// TestGithubCopilotSignIn pins the device flow: GitHub's Copilot App client ID on every call,
// the poll outcomes, and the token handed back on completion for the form to save.
func TestGithubCopilotSignIn(t *testing.T) {
	fake := newFakeGithubDeviceFlow(t)
	h := fake.handler()

	start := signInRequest("github-copilot", "")
	h.start(start)
	if start.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("start: status %d: %s", start.Response.StatusCode(), start.Response.Body())
	}
	var code githubDeviceCode
	if err := json.Unmarshal(start.Response.Body(), &code); err != nil || code.UserCode != "WDJB-MJHT" || code.DeviceCode != "dc_1" {
		t.Fatalf("start: got %+v, %v", code, err)
	}
	if got := fake.clientID.Load(); got != githubCopilotClientID {
		t.Fatalf("start sent client_id %v, want GitHub's Copilot App %s", got, githubCopilotClientID)
	}

	poll := func(reply string) githubSignInPollResult {
		t.Helper()
		fake.pollBody.Store(reply)
		ctx := signInRequest("github-copilot", `{"device_code":"dc_1"}`)
		h.poll(ctx)
		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("poll: status %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
		}
		var result githubSignInPollResult
		if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	for reply, want := range map[string]githubSignInPollResult{
		`{"error":"authorization_pending"}`:                                 {Status: "pending"},
		`{"error":"slow_down","interval":10}`:                               {Status: "pending", Interval: 10},
		`{"error":"expired_token"}`:                                         {Status: "expired"},
		`{"error":"access_denied"}`:                                         {Status: "denied"},
		`{"access_token":"ghu_signed_in","token_type":"bearer","scope":""}`: {Status: "complete", Token: "ghu_signed_in", Login: "octocat"},
	} {
		if got := poll(reply); got != want {
			t.Errorf("reply %s: got %+v, want %+v", reply, got, want)
		}
	}
	if got := fake.clientID.Load(); got != githubCopilotClientID {
		t.Fatalf("poll sent client_id %v, want %s", got, githubCopilotClientID)
	}
}

// TestGithubCopilotSignInPollUnknownCode pins that a code GitHub does not recognise is the
// caller's to restart (400), not a gateway failure (502).
func TestGithubCopilotSignInPollUnknownCode(t *testing.T) {
	fake := newFakeGithubDeviceFlow(t)
	h := fake.handler()
	for _, code := range []string{"incorrect_device_code", "bad_verification_code"} {
		fake.pollBody.Store(`{"error":"` + code + `"}`)
		ctx := signInRequest("github-copilot", `{"device_code":"dc_stale"}`)
		h.poll(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400: %s", code, ctx.Response.StatusCode(), ctx.Response.Body())
		}
		if !strings.Contains(string(ctx.Response.Body()), "start the sign-in again") {
			t.Fatalf("%s: body %s does not tell the operator to restart", code, ctx.Response.Body())
		}
	}
}

func TestGithubCopilotSignInPollRequiresDeviceCode(t *testing.T) {
	ctx := signInRequest("github-copilot", `{}`)
	NewGithubCopilotSignInHandler().poll(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("status %d, want 400 without a device_code", ctx.Response.StatusCode())
	}
}
