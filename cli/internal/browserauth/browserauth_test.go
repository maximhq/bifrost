package browserauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
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

// TestSignInRejectsCallbackStateMismatch verifies a wrong-state callback is
// refused without its code being exchanged, and does not end sign-in: with a
// fixed callback port any local process can reach the listener, so only the
// matching callback may decide the outcome.
func TestSignInRejectsCallbackStateMismatch(t *testing.T) {
	exchanged := make(chan string, 2)
	gateway := ssoGateway(t, exchanged)
	client := &Client{BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: 5 * time.Second}
	forgedStatus := make(chan int, 1)
	client.OpenBrowser = func(target string) error {
		forged := pastedCallbackURL(t, target, "attacker-code", "wrong-state")
		real := pastedCallbackURL(t, target, "real-code", "")
		go func() {
			response, getErr := http.Get(forged)
			if getErr != nil {
				forgedStatus <- 0
				return
			}
			_ = response.Body.Close()
			forgedStatus <- response.StatusCode
			if response, getErr = http.Get(real); getErr == nil {
				_ = response.Body.Close()
			}
		}()
		return nil
	}
	response, err := client.SignIn(context.Background(), false)
	if err != nil {
		t.Fatalf("sign-in ended on a wrong-state callback: %v", err)
	}
	if response.AccessToken != "ck-bf-agent-access" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if status := <-forgedStatus; status != http.StatusBadRequest {
		t.Fatalf("wrong-state callback status = %d, want %d", status, http.StatusBadRequest)
	}
	if got := <-exchanged; !strings.HasPrefix(got, "real-code ") {
		t.Fatalf("exchange = %q, want only the matching callback's code", got)
	}
	select {
	case extra := <-exchanged:
		t.Fatalf("unexpected second exchange %q", extra)
	default:
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

// ssoGateway serves the status and token endpoints and records exchanged codes.
func ssoGateway(t *testing.T, exchanged chan<- string) *httptest.Server {
	t.Helper()
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/agent/auth/status":
			_, _ = io.WriteString(writer, `{"idp_configured":true}`)
		case "/api/agent/auth/token":
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if exchanged != nil {
				exchanged <- body["code"] + " " + body["redirect_uri"]
			}
			_, _ = io.WriteString(writer, `{"agent_access_token":"ck-bf-agent-access","refresh_token":"refresh","user":{"email":"alice@example.com"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(gateway.Close)
	return gateway
}

// maxPortAttempts bounds retries when a released test port is taken before use.
const maxPortAttempts = 5

// freeLoopbackPort reserves and releases a loopback port for a static-port test.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

// pastedCallbackURL turns an authorize URL into the loopback address the
// browser lands on, with the given code and state.
func pastedCallbackURL(t *testing.T, authorize, code, state string) string {
	t.Helper()
	parsed, err := url.Parse(authorize)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(parsed.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	if state == "" {
		state = parsed.Query().Get("state")
	}
	callback.RawQuery = url.Values{"code": {code}, "state": {state}}.Encode()
	return callback.String()
}

// TestSignInUsesConfiguredCallbackPort pins the redirect URI to a fixed port
// so it can be forwarded into a container before sign-in starts.
func TestSignInUsesConfiguredCallbackPort(t *testing.T) {
	exchanged := make(chan string, 1)
	gateway := ssoGateway(t, exchanged)
	var port int
	// freeLoopbackPort releases the port before SignIn binds it, so another
	// process can take it in between; retry only that case.
	for attempt := 1; ; attempt++ {
		port = freeLoopbackPort(t)
		client := &Client{BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: 5 * time.Second, CallbackPort: port}
		client.OpenBrowser = func(target string) error {
			callback := pastedCallbackURL(t, target, "one-time-code", "")
			go func() {
				response, getErr := http.Get(callback)
				if getErr == nil {
					_ = response.Body.Close()
				}
			}()
			return nil
		}
		_, err := client.SignIn(context.Background(), false)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EADDRINUSE) || attempt == maxPortAttempts {
			t.Fatal(err)
		}
	}
	want := fmt.Sprintf("one-time-code http://127.0.0.1:%d/callback", port)
	if got := <-exchanged; got != want {
		t.Fatalf("exchange = %q, want %q", got, want)
	}
}

// TestSignInReportsBusyCallbackPort fails fast instead of silently falling back
// to a random port that the user has not forwarded.
func TestSignInReportsBusyCallbackPort(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	gateway := ssoGateway(t, nil)
	client := &Client{BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: time.Second, CallbackPort: port}
	client.OpenBrowser = func(string) error {
		t.Fatal("browser opened despite an unavailable callback port")
		return nil
	}
	_, err = client.SignIn(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("callback port %d is unavailable", port)) {
		t.Fatalf("error = %v, want unavailable callback port", err)
	}
}

// TestSignInAcceptsPastedCallbackURL completes sign-in when the browser cannot
// reach the CLI's loopback listener and the user pastes the address instead.
// Lines that are not a valid callback for this sign-in are skipped with a warning.
func TestSignInAcceptsPastedCallbackURL(t *testing.T) {
	exchanged := make(chan string, 2)
	gateway := ssoGateway(t, exchanged)
	pasteReader, pasteWriter := io.Pipe()
	defer pasteWriter.Close()
	var warnings []string
	client := &Client{
		BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: 5 * time.Second,
		PastedCallback: pasteReader,
		Warning:        func(err error) { warnings = append(warnings, err.Error()) },
	}
	client.OpenBrowser = func(string) error {
		t.Fatal("browser opened with noBrowser set")
		return nil
	}
	client.Authorization = func(target string) {
		go func() {
			_, _ = io.WriteString(pasteWriter, "\n")
			_, _ = io.WriteString(pasteWriter, "not a url\n")
			_, _ = io.WriteString(pasteWriter, pastedCallbackURL(t, target, "forged-code", "wrong-state")+"\n")
			_, _ = io.WriteString(pasteWriter, "  "+pastedCallbackURL(t, target, "pasted-code", "")+"  \r\n")
		}()
	}
	response, err := client.SignIn(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if response.AccessToken != "ck-bf-agent-access" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if got := <-exchanged; !strings.HasPrefix(got, "pasted-code http://127.0.0.1:") {
		t.Fatalf("exchange = %q, want the pasted code with the listener's redirect URI", got)
	}
	select {
	case extra := <-exchanged:
		t.Fatalf("unexpected second exchange %q", extra)
	default:
	}
	if len(warnings) != 2 || !strings.Contains(warnings[1], "state did not match") {
		t.Fatalf("warnings = %q, want one unparseable line and one state mismatch", warnings)
	}
}

// TestSignInWarnsWhenPastedCallbackReadFails reports a stdin read error instead
// of silently waiting for the timeout, and the loopback listener still works.
func TestSignInWarnsWhenPastedCallbackReadFails(t *testing.T) {
	gateway := ssoGateway(t, nil)
	warnings := make(chan string, 4)
	client := &Client{
		BaseURL: gateway.URL, HTTPClient: gateway.Client(), CallbackWait: 5 * time.Second,
		PastedCallback: iotest.ErrReader(errors.New("terminal closed")),
		Warning:        func(err error) { warnings <- err.Error() },
	}
	client.OpenBrowser = func(string) error {
		t.Fatal("browser opened with noBrowser set")
		return nil
	}
	client.Authorization = func(target string) {
		callback := pastedCallbackURL(t, target, "listener-code", "")
		go func() {
			select {
			case warning := <-warnings:
				warnings <- warning
			case <-time.After(3 * time.Second):
			}
			if response, err := http.Get(callback); err == nil {
				_ = response.Body.Close()
			}
		}()
	}
	if _, err := client.SignIn(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	select {
	case warning := <-warnings:
		if !strings.Contains(warning, "stopped reading pasted callbacks: terminal closed") {
			t.Fatalf("warning = %q, want the read error", warning)
		}
	default:
		t.Fatal("no warning for a failed pasted-callback read")
	}
}

// TestParseCallbackPort accepts an unset value and valid TCP ports only.
func TestParseCallbackPort(t *testing.T) {
	for _, test := range []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "", want: 0},
		{raw: "  ", want: 0},
		{raw: "8765", want: 8765},
		{raw: " 65535 ", want: 65535},
		{raw: "0", wantErr: true},
		{raw: "65536", wantErr: true},
		{raw: "-1", wantErr: true},
		{raw: "http", wantErr: true},
	} {
		got, err := ParseCallbackPort(test.raw)
		if (err != nil) != test.wantErr || got != test.want {
			t.Errorf("ParseCallbackPort(%q) = %d, %v; want %d, error %v", test.raw, got, err, test.want, test.wantErr)
		}
	}
}
