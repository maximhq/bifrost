package browserauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestSignInCompletesLoopbackPKCE verifies state, verifier, and callback binding end to end.
func TestSignInCompletesLoopbackPKCE(t *testing.T) {
	var authorizeURL *url.URL
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/agent/auth/status":
			_, _ = io.WriteString(writer, `{"idp_configured":true,"virtual_key_auth_enabled":true}`)
		case "/api/agent/auth/token":
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != "one-time-code" || body["redirect_uri"] != authorizeURL.Query().Get("redirect_uri") {
				t.Fatalf("unexpected exchange body: %#v", body)
			}
			sum := sha256.Sum256([]byte(body["code_verifier"]))
			if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != authorizeURL.Query().Get("code_challenge") {
				t.Fatalf("PKCE challenge = %q", got)
			}
			if body["device_name"] != "Bifrost CLI" || body["platform"] == "" || body["agent_version"] == "" {
				t.Fatalf("missing agent metadata: %#v", body)
			}
			_, _ = io.WriteString(writer, `{"agent_access_token":"ck-bf-agent-access","refresh_token":"refresh","expires_in":3600,"user":{"id":"user-1","email":"alice@example.com"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer gateway.Close()

	client := &Client{BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: time.Second, Version: "test"}
	client.OpenBrowser = func(target string) error {
		parsed, err := url.Parse(target)
		if err != nil {
			return err
		}
		authorizeURL = parsed
		if parsed.Path != "/api/agent/auth/authorize" || parsed.Query().Get("client_id") != "bifrost-cli" {
			t.Fatalf("unexpected authorize URL: %s", parsed)
		}
		callback, err := url.Parse(parsed.Query().Get("redirect_uri"))
		if err != nil {
			return err
		}
		query := callback.Query()
		query.Set("code", "one-time-code")
		query.Set("state", parsed.Query().Get("state"))
		callback.RawQuery = query.Encode()
		go func() {
			response, getErr := http.Get(callback.String())
			if getErr == nil {
				_ = response.Body.Close()
			}
		}()
		return nil
	}
	response, err := client.SignIn(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if response.AccessToken != "ck-bf-agent-access" || response.User.Email != "alice@example.com" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestCallbackServerShowsCLICompletionAfterExchange(t *testing.T) {
	results := make(chan callbackResult, 1)
	server := callbackServer(
		"state-1",
		results,
		func(code string) (TokenResponse, error) {
			if code != "one-time-code" {
				t.Fatalf("code = %q, want one-time-code", code)
			}
			return TokenResponse{AccessToken: "access", RefreshToken: "refresh"}, nil
		},
	)
	request := httptest.NewRequest(http.MethodGet, "/callback?code=one-time-code&state=state-1", nil)
	recorder := httptest.NewRecorder()

	server.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if location := recorder.Header().Get("Location"); location != "" {
		t.Fatalf("unexpected completion redirect: %q", location)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"Bifrost CLI sign-in complete",
		"You can close this window",
		"width: min(576px, 100%)",
		"font-size: 20px",
		"font-size: 14px",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("completion page missing %q: %q", want, body)
		}
	}
	if strings.Contains(body, "return to the terminal") {
		t.Fatalf("completion page retained outdated copy: %q", body)
	}
	if policy := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "default-src 'none'") {
		t.Fatalf("completion page CSP = %q", policy)
	}
	result := <-results
	if result.err != nil || result.response.AccessToken != "access" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCallbackServerDoesNotRedirectWhenExchangeFails(t *testing.T) {
	results := make(chan callbackResult, 1)
	wantErr := errors.New("exchange failed")
	server := callbackServer(
		"state-1",
		results,
		func(string) (TokenResponse, error) { return TokenResponse{}, wantErr },
	)
	request := httptest.NewRequest(http.MethodGet, "/callback?code=one-time-code&state=state-1", nil)
	recorder := httptest.NewRecorder()

	server.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if location := recorder.Header().Get("Location"); location != "" {
		t.Fatalf("unexpected redirect after failed exchange: %q", location)
	}
	if result := <-results; !errors.Is(result.err, wantErr) {
		t.Fatalf("result error = %v, want %v", result.err, wantErr)
	}
}

// TestSignInRejectsCallbackStateMismatch verifies login CSRF protection fails closed.
func TestSignInRejectsCallbackStateMismatch(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"idp_configured":true}`)
	}))
	defer gateway.Close()
	client := &Client{BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: time.Second}
	client.OpenBrowser = func(target string) error {
		parsed, _ := url.Parse(target)
		callback, _ := url.Parse(parsed.Query().Get("redirect_uri"))
		query := callback.Query()
		query.Set("code", "attacker-code")
		query.Set("state", "wrong-state")
		callback.RawQuery = query.Encode()
		go func() {
			response, getErr := http.Get(callback.String())
			if getErr == nil {
				_ = response.Body.Close()
			}
		}()
		return nil
	}
	_, err := client.SignIn(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "state did not match") {
		t.Fatalf("error = %v, want state mismatch", err)
	}
}

func TestSignInBoundsCallbackTokenExchange(t *testing.T) {
	releaseExchange := make(chan struct{})
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/agent/auth/status":
			_, _ = io.WriteString(writer, `{"idp_configured":true}`)
		case "/api/agent/auth/token":
			<-releaseExchange
		default:
			http.NotFound(writer, request)
		}
	}))
	defer gateway.Close()
	defer close(releaseExchange)

	client := &Client{
		BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: time.Second,
		exchangeTimeout: 25 * time.Millisecond, shutdownTimeout: 25 * time.Millisecond,
	}
	client.OpenBrowser = func(target string) error {
		parsed, err := url.Parse(target)
		if err != nil {
			return err
		}
		callback, err := url.Parse(parsed.Query().Get("redirect_uri"))
		if err != nil {
			return err
		}
		query := callback.Query()
		query.Set("code", "one-time-code")
		query.Set("state", parsed.Query().Get("state"))
		callback.RawQuery = query.Encode()
		go func() {
			response, getErr := http.Get(callback.String())
			if getErr == nil {
				_ = response.Body.Close()
			}
		}()
		return nil
	}

	started := time.Now()
	_, err := client.SignIn(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("error = %v, want bounded exchange deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded exchange took %s", elapsed)
	}
}

func TestSignInBoundsCallbackServerShutdown(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"idp_configured":true}`)
	}))
	defer gateway.Close()

	var slowConnection net.Conn
	client := &Client{
		BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: 50 * time.Millisecond,
		shutdownTimeout: 25 * time.Millisecond,
	}
	client.OpenBrowser = func(target string) error {
		parsed, err := url.Parse(target)
		if err != nil {
			return err
		}
		callback, err := url.Parse(parsed.Query().Get("redirect_uri"))
		if err != nil {
			return err
		}
		slowConnection, err = net.Dial("tcp", callback.Host)
		if err != nil {
			return err
		}
		_, err = io.WriteString(slowConnection, "GET /callback HTTP/1.1\r\nHost: "+callback.Host+"\r\n")
		return err
	}
	defer func() {
		if slowConnection != nil {
			_ = slowConnection.Close()
		}
	}()

	started := time.Now()
	_, err := client.SignIn(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "timed out waiting") {
		t.Fatalf("error = %v, want callback timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded callback shutdown took %s", elapsed)
	}
}

// TestRefreshAndLogoutUseOpaqueCredentials verifies refresh tokens stay in JSON and access tokens stay in headers.
func TestRefreshAndLogoutUseOpaqueCredentials(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/agent/auth/refresh":
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), `"refresh_token":"old-refresh"`) || request.Header.Get("Authorization") != "" {
				t.Fatalf("unexpected refresh request body=%s auth=%q", body, request.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(writer, `{"agent_access_token":"ck-bf-agent-new","refresh_token":"new-refresh"}`)
		case "/api/agent/auth/logout":
			if request.Header.Get("Authorization") != "Bearer ck-bf-agent-new" {
				t.Fatalf("logout authorization = %q", request.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(writer, `{"status":"logged_out"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer gateway.Close()
	client := &Client{BaseURL: gateway.URL, HTTPClient: gateway.Client()}
	response, err := client.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Logout(context.Background(), response.AccessToken); err != nil {
		t.Fatal(err)
	}
}
