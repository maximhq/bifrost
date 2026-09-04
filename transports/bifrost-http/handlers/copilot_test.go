package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/valyala/fasthttp"
)

// newTestCopilotHandler creates a CopilotHandler with its GitHub endpoints redirected
// to the provided test servers, preventing any real network calls during tests.
func newTestCopilotHandler(deviceCodeSrv, accessTokenSrv *httptest.Server) *CopilotHandler {
	return &CopilotHandler{
		httpClient:     &http.Client{},
		deviceCodeURL:  deviceCodeSrv.URL,
		accessTokenURL: accessTokenSrv.URL,
	}
}

// noopServer returns an httptest server that serves no meaningful response.
func noopServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
}

func TestInitiateDeviceLogin_Success(t *testing.T) {
	deviceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DeviceLoginInitiateResponse{
			DeviceCode:      "device-code-123",
			UserCode:        "ABCD-1234",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       900,
			Interval:        5,
		})
	}))
	defer deviceSrv.Close()
	tokenSrv := noopServer()
	defer tokenSrv.Close()

	SetLogger(&mockLogger{})
	h := newTestCopilotHandler(deviceSrv, tokenSrv)

	ctx := &fasthttp.RequestCtx{}
	h.initiateDeviceLogin(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	var resp DeviceLoginInitiateResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.DeviceCode != "device-code-123" {
		t.Errorf("expected device_code 'device-code-123', got %q", resp.DeviceCode)
	}
	if resp.UserCode != "ABCD-1234" {
		t.Errorf("expected user_code 'ABCD-1234', got %q", resp.UserCode)
	}
}

func TestInitiateDeviceLogin_GitHubError(t *testing.T) {
	deviceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"Service Unavailable"}`))
	}))
	defer deviceSrv.Close()
	tokenSrv := noopServer()
	defer tokenSrv.Close()

	SetLogger(&mockLogger{})
	h := newTestCopilotHandler(deviceSrv, tokenSrv)

	ctx := &fasthttp.RequestCtx{}
	h.initiateDeviceLogin(ctx)

	if ctx.Response.StatusCode() != http.StatusServiceUnavailable {
		t.Errorf("expected 503 from GitHub to be forwarded, got %d", ctx.Response.StatusCode())
	}
}

func TestInitiateDeviceLogin_EmptyDeviceCode(t *testing.T) {
	deviceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return a response with no device_code field.
		_, _ = w.Write([]byte(`{"device_code":"","user_code":"ABCD-1234"}`))
	}))
	defer deviceSrv.Close()
	tokenSrv := noopServer()
	defer tokenSrv.Close()

	SetLogger(&mockLogger{})
	h := newTestCopilotHandler(deviceSrv, tokenSrv)

	ctx := &fasthttp.RequestCtx{}
	h.initiateDeviceLogin(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadGateway {
		t.Errorf("expected 502 for empty device_code, got %d", ctx.Response.StatusCode())
	}
}

func TestPollDeviceLogin_MissingDeviceCode(t *testing.T) {
	deviceSrv := noopServer()
	defer deviceSrv.Close()
	tokenSrv := noopServer()
	defer tokenSrv.Close()

	SetLogger(&mockLogger{})
	h := newTestCopilotHandler(deviceSrv, tokenSrv)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`{"device_code":""}`))
	h.pollDeviceLogin(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Errorf("expected 400 for missing device_code, got %d", ctx.Response.StatusCode())
	}
}

func TestPollDeviceLogin_InvalidJSON(t *testing.T) {
	deviceSrv := noopServer()
	defer deviceSrv.Close()
	tokenSrv := noopServer()
	defer tokenSrv.Close()

	SetLogger(&mockLogger{})
	h := newTestCopilotHandler(deviceSrv, tokenSrv)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`not-json`))
	h.pollDeviceLogin(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON body, got %d", ctx.Response.StatusCode())
	}
}

func TestPollDeviceLogin_AuthorizationPending(t *testing.T) {
	deviceSrv := noopServer()
	defer deviceSrv.Close()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer tokenSrv.Close()

	SetLogger(&mockLogger{})
	h := newTestCopilotHandler(deviceSrv, tokenSrv)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`{"device_code":"some-code"}`))
	h.pollDeviceLogin(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", ctx.Response.StatusCode())
	}
	var resp DeviceLoginPollResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", resp.Status)
	}
}

func TestPollDeviceLogin_ExpiredToken(t *testing.T) {
	deviceSrv := noopServer()
	defer deviceSrv.Close()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"expired_token"}`))
	}))
	defer tokenSrv.Close()

	SetLogger(&mockLogger{})
	h := newTestCopilotHandler(deviceSrv, tokenSrv)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`{"device_code":"some-code"}`))
	h.pollDeviceLogin(ctx)

	var resp DeviceLoginPollResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Status != "expired" {
		t.Errorf("expected status 'expired', got %q", resp.Status)
	}
}

func TestPollDeviceLogin_AccessDenied(t *testing.T) {
	deviceSrv := noopServer()
	defer deviceSrv.Close()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"access_denied"}`))
	}))
	defer tokenSrv.Close()

	SetLogger(&mockLogger{})
	h := newTestCopilotHandler(deviceSrv, tokenSrv)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`{"device_code":"some-code"}`))
	h.pollDeviceLogin(ctx)

	var resp DeviceLoginPollResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Status != "error" {
		t.Errorf("expected status 'error', got %q", resp.Status)
	}
}

func TestPollDeviceLogin_Success(t *testing.T) {
	deviceSrv := noopServer()
	defer deviceSrv.Close()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gho_abc123","token_type":"bearer"}`))
	}))
	defer tokenSrv.Close()

	SetLogger(&mockLogger{})
	h := newTestCopilotHandler(deviceSrv, tokenSrv)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`{"device_code":"some-code"}`))
	h.pollDeviceLogin(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	var resp DeviceLoginPollResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Status != "complete" {
		t.Errorf("expected status 'complete', got %q", resp.Status)
	}
	if resp.AccessToken != "gho_abc123" {
		t.Errorf("expected access_token 'gho_abc123', got %q", resp.AccessToken)
	}
}
