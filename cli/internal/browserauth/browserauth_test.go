package browserauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
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
			if body["device_name"] != "Bifrost CLI" || body["platform"] == "" || body["agent_version"] == "" || body["hardware_id"] != "cli-device-test" {
				t.Fatalf("missing agent metadata: %#v", body)
			}
			_, _ = io.WriteString(writer, `{"agent_access_token":"ck-bf-agent-access","refresh_token":"refresh","expires_in":3600,"user":{"id":"user-1","email":"alice@example.com"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer gateway.Close()

	client := &Client{BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: time.Second, Version: "test", HardwareID: "cli-device-test"}
	client.OpenBrowser = func(target string) error {
		parsed, err := url.Parse(target)
		if err != nil {
			return err
		}
		authorizeURL = parsed
		if parsed.Path != "/api/agent/auth/authorize" || parsed.Query().Get("client_id") != "bifrost-agent" {
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
