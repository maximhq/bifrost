package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// roundTripFunc adapts a function into an HTTP transport for isolated tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip executes the configured in-memory HTTP exchange.
func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

// TestClientRejectsHTTPSToHTTPRedirect verifies a same-host scheme-downgrading
// redirect is refused, since Go's default client only strips sensitive
// headers on a host change, not a scheme change, and would otherwise forward
// the bearer token or virtual key over a plaintext connection.
func TestClientRejectsHTTPSToHTTPRedirect(t *testing.T) {
	api := New("https://gateway.example", Credentials{ManagementKey: "bfst-test"}, time.Second)
	requestCount := 0
	api.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if requestCount > 1 {
			t.Fatalf("request %d to %s: redirect should have been blocked before a second request was sent", requestCount, request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusFound, Header: http.Header{"Location": {"http://gateway.example/api/providers"}},
			Body: io.NopCloser(strings.NewReader("")), Request: request,
		}, nil
	})
	if _, err := api.Do(context.Background(), Request{Path: "/api/providers"}); err == nil {
		t.Fatal("expected the HTTPS to HTTP redirect to be rejected")
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1 (redirect not followed)", requestCount)
	}
}

// TestClientRejectsCrossHostRedirect verifies a same-scheme redirect to a
// different host is refused. Go's http.Client only strips Authorization and
// Cookie-family headers on a cross-host redirect — a custom header like
// x-bf-vk (the virtual key) is always forwarded regardless of host, so an
// unrestricted cross-host redirect would leak it to an arbitrary origin.
func TestClientRejectsCrossHostRedirect(t *testing.T) {
	api := New("https://gateway.example", Credentials{VirtualKey: "sk-bf-test"}, time.Second)
	requestCount := 0
	api.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if requestCount > 1 {
			t.Fatalf("request %d to %s: cross-host redirect should have been blocked before a second request was sent", requestCount, request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://attacker.example/api/providers"}},
			Body: io.NopCloser(strings.NewReader("")), Request: request,
		}, nil
	})
	if _, err := api.Do(context.Background(), Request{Path: "/api/providers"}); err == nil {
		t.Fatal("expected the cross-host redirect to be rejected")
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1 (redirect not followed)", requestCount)
	}
}

// TestClientRejectsCrossHostRedirectToLoopback ensures the local-development
// HTTP exception cannot be used by a remote gateway to forward credentials to
// an unrelated service listening on the user's machine.
func TestClientRejectsCrossHostRedirectToLoopback(t *testing.T) {
	api := New("https://gateway.example", Credentials{VirtualKey: "sk-bf-test"}, time.Second)
	requestCount := 0
	api.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if requestCount > 1 {
			t.Fatalf("request %d to %s: cross-host redirect should have been blocked", requestCount, request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusFound, Header: http.Header{"Location": {"http://127.0.0.1:8080/v1/models"}},
			Body: io.NopCloser(strings.NewReader("")), Request: request,
		}, nil
	})
	if _, err := api.Do(context.Background(), Request{Path: "/v1/models"}); err == nil {
		t.Fatal("expected cross-host redirect to loopback to be rejected")
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
}

// TestClientStreamRejectsCrossHostRedirect verifies the streaming client
// (which uses a copy of the base http.Client) enforces the same redirect
// policy as the buffered client.
func TestClientStreamRejectsCrossHostRedirect(t *testing.T) {
	api := New("https://gateway.example", Credentials{VirtualKey: "sk-bf-test"}, time.Second)
	requestCount := 0
	api.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if requestCount > 1 {
			t.Fatalf("request %d to %s: cross-host redirect should have been blocked before a second request was sent", requestCount, request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://attacker.example/v1/responses"}},
			Body: io.NopCloser(strings.NewReader("")), Request: request,
		}, nil
	})
	var output bytes.Buffer
	if err := api.Stream(context.Background(), Request{Path: "/v1/responses", Auth: AuthInference}, &output); err == nil {
		t.Fatal("expected the cross-host redirect to be rejected")
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1 (redirect not followed)", requestCount)
	}
}

// TestClientAllowsHTTPSToHTTPSRedirect verifies a same-scheme redirect still works.
func TestClientAllowsHTTPSToHTTPSRedirect(t *testing.T) {
	api := New("https://gateway.example", Credentials{ManagementKey: "bfst-test"}, time.Second)
	requestCount := 0
	api.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if requestCount == 1 {
			return &http.Response{
				StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://gateway.example/api/providers/new"}},
				Body: io.NopCloser(strings.NewReader("")), Request: request,
			}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	})
	if _, err := api.Do(context.Background(), Request{Path: "/api/providers"}); err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2 (redirect followed)", requestCount)
	}
}

// TestBuildEndpoint verifies base paths and encoded query values are preserved.
func TestBuildEndpoint(t *testing.T) {
	endpoint, err := BuildEndpoint("https://example.com/gateway/", "/api/models?existing=yes", url.Values{"search": {"a b"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/gateway/api/models?existing=yes&search=a+b"
	if endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}
	if _, err := BuildEndpoint("https://example.com", "https://evil.example/path", nil); err == nil {
		t.Fatal("expected absolute request URL to be rejected")
	}
	escaped, err := BuildEndpoint("https://example.com/gateway", "/api/resources/name%2Fwith%2Fslashes", nil)
	if err != nil {
		t.Fatal(err)
	}
	if escaped != "https://example.com/gateway/api/resources/name%2Fwith%2Fslashes" {
		t.Fatalf("escaped endpoint = %q", escaped)
	}
}

// TestBuildEndpointRejectsInsecureRemoteGateway ensures credentials can never
// be routed to a non-loopback gateway over plaintext HTTP. Local development
// endpoints remain available without TLS.
func TestBuildEndpointRejectsInsecureRemoteGateway(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "HTTPS gateway", baseURL: "https://gateway.example"},
		{name: "localhost", baseURL: "http://localhost:8080"},
		{name: "IPv4 loopback", baseURL: "http://127.0.0.1:8080"},
		{name: "IPv6 loopback", baseURL: "http://[::1]:8080"},
		{name: "remote HTTP gateway", baseURL: "http://gateway.example", wantErr: true},
		{name: "private network HTTP gateway", baseURL: "http://10.0.0.8:8080", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildEndpoint(test.baseURL, "/v1/models", nil)
			if test.wantErr && err == nil {
				t.Fatal("expected plaintext non-loopback gateway to be rejected")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("BuildEndpoint() error = %v", err)
			}
		})
	}
}

// TestClientAuthorization verifies management and inference credentials stay separate.
func TestClientAuthorization(t *testing.T) {
	requests := make(chan *http.Request, 2)
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(context.Background())
		clone.Body = io.NopCloser(strings.NewReader(""))
		requests <- clone
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r,
		}, nil
	})}

	api := New("https://gateway.example", Credentials{VirtualKey: "sk-bf-test", ManagementKey: "bfst-test"}, time.Second)
	api.HTTPClient = httpClient
	if _, err := api.Do(context.Background(), Request{Path: "/api/providers"}); err != nil {
		t.Fatal(err)
	}
	managementRequest := <-requests
	if got := managementRequest.Header.Get("Authorization"); got != "Bearer bfst-test" {
		t.Fatalf("management authorization = %q", got)
	}
	if got := managementRequest.Header.Get("x-bf-vk"); got != "" {
		t.Fatalf("management virtual key = %q", got)
	}

	if _, err := api.Do(context.Background(), Request{Path: "/v1/models"}); err != nil {
		t.Fatal(err)
	}
	inferenceRequest := <-requests
	if got := inferenceRequest.Header.Get("x-bf-vk"); got != "sk-bf-test" {
		t.Fatalf("inference virtual key = %q", got)
	}
	if got := inferenceRequest.Header.Get("Authorization"); got != "" {
		t.Fatalf("inference authorization = %q", got)
	}
}

// TestClientSendsSetupTokenOnManagementCalls pins that the CLI works against a gateway
// with no admin account yet: management calls carry BIFROST_SETUP_TOKEN in
// X-Bifrost-Setup-Token, which the management API requires until the first admin
// exists. Inference calls never carry it, and nothing is sent when it is unset.
func TestClientSendsSetupTokenOnManagementCalls(t *testing.T) {
	requests := make(chan *http.Request, 3)
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests <- r.Clone(context.Background())
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r,
		}, nil
	})}
	api := New("https://gateway.example", Credentials{}, time.Second)
	api.HTTPClient = httpClient

	t.Setenv("BIFROST_SETUP_TOKEN", "operator-setup-token")
	if _, err := api.Do(context.Background(), Request{Path: "/api/providers"}); err != nil {
		t.Fatal(err)
	}
	if got := (<-requests).Header.Get("X-Bifrost-Setup-Token"); got != "operator-setup-token" {
		t.Fatalf("management call setup token = %q, want the BIFROST_SETUP_TOKEN value", got)
	}
	if _, err := api.Do(context.Background(), Request{Path: "/v1/models"}); err != nil {
		t.Fatal(err)
	}
	if got := (<-requests).Header.Get("X-Bifrost-Setup-Token"); got != "" {
		t.Fatalf("inference call carried the setup token %q", got)
	}

	t.Setenv("BIFROST_SETUP_TOKEN", "")
	if _, err := api.Do(context.Background(), Request{Path: "/api/providers"}); err != nil {
		t.Fatal(err)
	}
	if got := (<-requests).Header.Get("X-Bifrost-Setup-Token"); got != "" {
		t.Fatalf("setup token %q sent while BIFROST_SETUP_TOKEN is unset", got)
	}
}

// TestClientAgentInferenceAuthorization mirrors the Edge gateway-mode bearer and selection headers.
func TestClientAgentInferenceAuthorization(t *testing.T) {
	var received *http.Request
	api := New("https://gateway.example", Credentials{
		AgentToken: "ck-bf-agent-test", AgentVirtualKeyID: "vk-assigned-1",
	}, time.Second)
	api.UserAgent = "bifrost-cli/test"
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	})}
	if _, err := api.Do(context.Background(), Request{Path: "/v1/models", Auth: AuthInference}); err != nil {
		t.Fatal(err)
	}
	if got := received.Header.Get("Authorization"); got != "Bearer ck-bf-agent-test" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := received.Header.Get("x-bf-agent-vk-id"); got != "vk-assigned-1" {
		t.Fatalf("x-bf-agent-vk-id = %q", got)
	}
	if got := received.Header.Get("X-Bifrost-Agent"); got != "bifrost-cli/test" {
		t.Fatalf("X-Bifrost-Agent = %q", got)
	}
	if got := received.Header.Get("X-Bf-App"); got != "bifrost-cli" {
		t.Fatalf("X-Bf-App = %q", got)
	}
	if got := received.Header.Get("x-bf-vk"); got != "" {
		t.Fatalf("x-bf-vk unexpectedly contains %q", got)
	}
}

// TestClientAgentInferencePrecedesRawVirtualKey verifies the CLI mirrors the
// Edge agent when both user-session and legacy inference credentials exist.
func TestClientAgentInferencePrecedesRawVirtualKey(t *testing.T) {
	var received *http.Request
	api := New("https://gateway.example", Credentials{
		VirtualKey: "sk-bf-legacy", AgentToken: "ck-bf-agent-test",
	}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	})}
	if _, err := api.Do(context.Background(), Request{Path: "/v1/models", Auth: AuthInference}); err != nil {
		t.Fatal(err)
	}
	if got := received.Header.Get("Authorization"); got != "Bearer ck-bf-agent-test" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := received.Header.Get("x-bf-vk"); got != "" {
		t.Fatalf("legacy virtual key unexpectedly used: %q", got)
	}
}

// TestClientDebugOutputDoesNotExposeCredentials pins the metadata-only debug contract.
func TestClientDebugOutputDoesNotExposeCredentials(t *testing.T) {
	api := New("https://gateway.example", Credentials{AgentToken: "ck-bf-agent-super-secret"}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: request}, nil
	})}
	var debug bytes.Buffer
	api.DebugWriter = &debug
	if _, err := api.Do(context.Background(), Request{Path: "/v1/models", Auth: AuthInference}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(debug.String(), "ck-bf-agent-super-secret") || strings.Contains(debug.String(), "Authorization") {
		t.Fatalf("debug output exposed credentials: %q", debug.String())
	}
}

// erroringWriter fails every write, simulating a broken debug output pipe.
type erroringWriter struct{ err error }

func (w *erroringWriter) Write([]byte) (int, error) { return 0, w.err }

// TestClientDoRequestDebugWriteFailureReturnsError verifies a broken debug
// pipe while logging the outgoing request line is reported.
func TestClientDoRequestDebugWriteFailureReturnsError(t *testing.T) {
	api := New("https://gateway.example", Credentials{}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatal("request unexpectedly sent after debug write failure")
		return nil, nil
	})}
	api.DebugWriter = &erroringWriter{err: errors.New("broken pipe")}
	if _, err := api.Do(context.Background(), Request{Path: "/api/providers"}); err == nil {
		t.Fatal("expected a debug write failure while logging the request to be reported")
	}
}

// TestClientDoResponseDebugWriteFailureReturnsError verifies a broken debug
// pipe while logging the response status line is reported.
func TestClientDoResponseDebugWriteFailureReturnsError(t *testing.T) {
	api := New("https://gateway.example", Credentials{}, time.Second)
	debug := &erroringWriter{}
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		debug.err = errors.New("broken pipe")
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	})}
	api.DebugWriter = debug
	if _, err := api.Do(context.Background(), Request{Path: "/api/providers"}); err == nil {
		t.Fatal("expected a debug write failure while logging the response to be reported")
	}
}

// TestClientStreamDebugWriteFailureReturnsError verifies a broken debug pipe
// while logging the streaming response status line is reported.
func TestClientStreamDebugWriteFailureReturnsError(t *testing.T) {
	api := New("https://gateway.example", Credentials{}, time.Second)
	debug := &erroringWriter{}
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		debug.err = errors.New("broken pipe")
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: x\n\n")), Request: request}, nil
	})}
	api.DebugWriter = debug
	var output bytes.Buffer
	if err := api.Stream(context.Background(), Request{Path: "/v1/responses", Auth: AuthNone}, &output); err == nil {
		t.Fatal("expected a debug write failure while logging the streaming response to be reported")
	}
}

// TestClientRefreshesAgentSessionOnce verifies an inference 401 rotates and retries its bearer.
func TestClientRefreshesAgentSessionOnce(t *testing.T) {
	requestCount := 0
	api := New("https://gateway.example", Credentials{AgentToken: "ck-bf-agent-old"}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		want := "Bearer ck-bf-agent-old"
		status := http.StatusUnauthorized
		if requestCount == 2 {
			want = "Bearer ck-bf-agent-new"
			status = http.StatusOK
		}
		if got := request.Header.Get("Authorization"); got != want {
			t.Fatalf("request %d authorization = %q, want %q", requestCount, got, want)
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	})}
	refreshCount := 0
	api.RefreshAgentToken = func(_ context.Context, stale string) (string, error) {
		refreshCount++
		if stale != "ck-bf-agent-old" {
			t.Fatalf("stale token = %q", stale)
		}
		return "ck-bf-agent-new", nil
	}
	if _, err := api.Do(context.Background(), Request{Path: "/v1/models", Auth: AuthInference}); err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 || refreshCount != 1 {
		t.Fatalf("requests=%d refreshes=%d", requestCount, refreshCount)
	}
}

// TestClientDoesNotRefreshAgentForPolicyRejection verifies a missing or
// unusable assigned key is not mistaken for an expired SSO session.
func TestClientDoesNotRefreshAgentForPolicyRejection(t *testing.T) {
	api := New("https://gateway.example", Credentials{AgentToken: "ck-bf-agent-current"}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(`{"type":"virtual_key_not_found"}`)), Request: request,
		}, nil
	})}
	refreshCount := 0
	api.RefreshAgentToken = func(_ context.Context, _ string) (string, error) {
		refreshCount++
		return "ck-bf-agent-new", nil
	}
	if _, err := api.Do(context.Background(), Request{Path: "/v1/models", Auth: AuthInference}); err == nil {
		t.Fatal("expected policy rejection")
	}
	if refreshCount != 0 {
		t.Fatalf("refresh count = %d, want 0", refreshCount)
	}
}

func TestClientDoesNotRefreshAgentForUnmarkedForbidden(t *testing.T) {
	api := New("https://gateway.example", Credentials{AgentToken: "ck-bf-agent-current"}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(`{"error":"model is not allowed"}`)), Request: request,
		}, nil
	})}
	refreshCount := 0
	api.RefreshAgentToken = func(_ context.Context, _ string) (string, error) {
		refreshCount++
		return "ck-bf-agent-new", nil
	}
	if _, err := api.Do(context.Background(), Request{Path: "/v1/models", Auth: AuthInference}); err == nil {
		t.Fatal("expected policy rejection")
	}
	if refreshCount != 0 {
		t.Fatalf("refresh count = %d, want 0", refreshCount)
	}
}

// TestClientRefreshesMarkedAgentRejection verifies the Enterprise rejection
// marker takes precedence even when the response is forbidden.
func TestClientRefreshesMarkedAgentRejection(t *testing.T) {
	requestCount := 0
	api := New("https://gateway.example", Credentials{AgentToken: "ck-bf-agent-old"}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if requestCount == 1 {
			return &http.Response{
				StatusCode: http.StatusForbidden, Header: http.Header{"X-Bf-Agent-Auth": {"rejected"}},
				Body: io.NopCloser(strings.NewReader(`{"type":"virtual_key_not_found"}`)), Request: request,
			}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	})}
	api.RefreshAgentToken = func(_ context.Context, _ string) (string, error) {
		return "ck-bf-agent-new", nil
	}
	if _, err := api.Do(context.Background(), Request{Path: "/v1/models", Auth: AuthInference}); err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
}

// TestClientAPIError verifies gateway response details remain available to callers.
func TestClientAPIError(t *testing.T) {
	api := New("https://gateway.example", Credentials{}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(`{"error":"denied"}`)), Request: request,
		}, nil
	})}
	_, err := api.Do(context.Background(), Request{Path: "/api/providers"})
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error = %T %v, want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusForbidden || !strings.Contains(apiErr.Body, "denied") {
		t.Fatalf("unexpected API error: %#v", apiErr)
	}
}

func TestClientAPIErrorTruncatesRenderedBody(t *testing.T) {
	body := strings.Repeat("x", maxErrorDisplayBytes+500)
	apiErr := &APIError{Method: http.MethodGet, URL: "https://gateway.example/failure", StatusCode: http.StatusBadGateway, Body: body}
	rendered := apiErr.Error()
	if len(apiErr.Body) != len(body) {
		t.Fatalf("stored body length = %d, want %d", len(apiErr.Body), len(body))
	}
	if !strings.Contains(rendered, "truncated") || len(rendered) >= len(body) {
		t.Fatalf("rendered error was not bounded: length=%d value=%q", len(rendered), rendered)
	}
}

// TestClientStream copies response chunks without JSON materialization.
func TestClientStream(t *testing.T) {
	api := New("https://gateway.example", Credentials{VirtualKey: "sk-bf-test"}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("x-bf-vk") != "sk-bf-test" {
			t.Fatalf("stream request virtual key = %q", request.Header.Get("x-bf-vk"))
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("data: one\n\ndata: two\n\n")), Request: request,
		}, nil
	})}
	var output bytes.Buffer
	if err := api.Stream(context.Background(), Request{Method: http.MethodPost, Path: "/v1/responses", Auth: AuthInference}, &output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "data: one\n\ndata: two\n\n" {
		t.Fatalf("stream output = %q", got)
	}
}

func TestClientStreamDoesNotApplyTotalResponseTimeout(t *testing.T) {
	api := New("https://gateway.example", Credentials{}, 10*time.Millisecond)
	api.HTTPClient = &http.Client{
		Timeout: 10 * time.Millisecond,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			reader, writer := io.Pipe()
			go func() {
				_, _ = io.WriteString(writer, "data: one\n\n")
				time.Sleep(30 * time.Millisecond)
				_, _ = io.WriteString(writer, "data: two\n\n")
				_ = writer.Close()
			}()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader, Request: request}, nil
		}),
	}
	var output bytes.Buffer
	if err := api.Stream(context.Background(), Request{Path: "/v1/responses", Auth: AuthNone}, &output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "data: one\n\ndata: two\n\n" {
		t.Fatalf("stream output = %q", got)
	}
}

func TestStreamingClientUsesHeaderTimeout(t *testing.T) {
	api := New("https://gateway.example", Credentials{}, 3*time.Second)
	streaming := api.streamingHTTPClient()
	if streaming.Timeout != 0 {
		t.Fatalf("streaming total timeout = %s, want 0", streaming.Timeout)
	}
	transport, ok := streaming.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("streaming transport = %T", streaming.Transport)
	}
	if transport.ResponseHeaderTimeout != 3*time.Second {
		t.Fatalf("response header timeout = %s", transport.ResponseHeaderTimeout)
	}
	if api.HTTPClient.Timeout != 3*time.Second {
		t.Fatalf("buffered timeout changed to %s", api.HTTPClient.Timeout)
	}
}

// TestClientStreamRefreshesAgentSession verifies streaming requests receive the
// same one-time Enterprise SSO recovery as buffered requests.
func TestClientStreamRefreshesAgentSession(t *testing.T) {
	requestCount := 0
	api := New("https://gateway.example", Credentials{AgentToken: "ck-bf-agent-old"}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		status := http.StatusUnauthorized
		body := `{}`
		want := "Bearer ck-bf-agent-old"
		if requestCount == 2 {
			status = http.StatusOK
			body = "data: complete\n\n"
			want = "Bearer ck-bf-agent-new"
		}
		if got := request.Header.Get("Authorization"); got != want {
			t.Fatalf("request %d authorization = %q, want %q", requestCount, got, want)
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	api.RefreshAgentToken = func(_ context.Context, stale string) (string, error) {
		if stale != "ck-bf-agent-old" {
			t.Fatalf("stale token = %q", stale)
		}
		return "ck-bf-agent-new", nil
	}
	var output bytes.Buffer
	if err := api.Stream(context.Background(), Request{Path: "/v1/responses", Auth: AuthInference}, &output); err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 || output.String() != "data: complete\n\n" {
		t.Fatalf("requests=%d output=%q", requestCount, output.String())
	}
}

// TestClientDoesNotRefreshAgentTokenAlreadyRotatedByConcurrentRequest verifies that
// when a request's old-token rejection is only observed after another concurrent
// request has already completed rotation, the delayed request recognizes the
// token it actually used is no longer current and does not trigger a second,
// unnecessary refresh.
func TestClientDoesNotRefreshAgentTokenAlreadyRotatedByConcurrentRequest(t *testing.T) {
	api := New("https://gateway.example", Credentials{AgentToken: "ck-bf-agent-old"}, time.Second)

	aSent := make(chan struct{})
	var aSentOnce sync.Once
	bRotated := make(chan struct{})
	var bRotatedOnce sync.Once

	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/a"):
			if request.Header.Get("Authorization") == "Bearer ck-bf-agent-old" {
				// Signal that request A's rejection is in flight using the
				// original token, then hold the response until request B has
				// fully rotated the token, simulating network latency on A's
				// response arriving after B's refresh already completed.
				aSentOnce.Do(func() { close(aSent) })
				<-bRotated
				return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
		case strings.HasSuffix(request.URL.Path, "/b"):
			// Wait for request A to already be in flight with the original
			// token before this request's own rejection/refresh cycle runs.
			<-aSent
			if request.Header.Get("Authorization") == "Bearer ck-bf-agent-new" {
				bRotatedOnce.Do(func() { close(bRotated) })
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
			}
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
		default:
			t.Fatalf("unexpected request path %q", request.URL.Path)
			return nil, nil
		}
	})}

	refreshCount := 0
	api.RefreshAgentToken = func(_ context.Context, stale string) (string, error) {
		refreshCount++
		if stale != "ck-bf-agent-old" {
			t.Errorf("refresh %d called with stale=%q, want the token actually used by the failed request", refreshCount, stale)
		}
		return "ck-bf-agent-new", nil
	}

	var wait sync.WaitGroup
	errs := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, err := api.Do(context.Background(), Request{Path: "/v1/models/a", Auth: AuthInference})
		errs <- err
	}()
	go func() {
		defer wait.Done()
		_, err := api.Do(context.Background(), Request{Path: "/v1/models/b", Auth: AuthInference})
		errs <- err
	}()
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if refreshCount != 1 {
		t.Fatalf("refresh count = %d, want 1", refreshCount)
	}
}

// TestClientConcurrentAgentRefresh verifies parallel callers share safe token rotation.
func TestClientConcurrentAgentRefresh(t *testing.T) {
	api := New("https://gateway.example", Credentials{AgentToken: "ck-bf-agent-old"}, time.Second)
	api.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusUnauthorized
		if request.Header.Get("Authorization") == "Bearer ck-bf-agent-new" {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	})}
	api.RefreshAgentToken = func(_ context.Context, _ string) (string, error) {
		return "ck-bf-agent-new", nil
	}
	const callers = 24
	start := make(chan struct{})
	errors := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := api.Do(context.Background(), Request{Path: "/v1/models", Auth: AuthInference})
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}
