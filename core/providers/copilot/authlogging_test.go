package copilot

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// recordingLogger renders messages the way the real logger does (printf via
// zerolog's Msgf), so key/value-style calls show up as %!(EXTRA ...).
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordingLogger) record(level, msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+fmt.Sprintf(msg, args...))
}

func (l *recordingLogger) Debug(msg string, args ...any) { l.record("debug", msg, args...) }
func (l *recordingLogger) Info(msg string, args ...any)  { l.record("info", msg, args...) }
func (l *recordingLogger) Warn(msg string, args ...any)  { l.record("warn", msg, args...) }
func (l *recordingLogger) Error(msg string, args ...any) { l.record("error", msg, args...) }
func (l *recordingLogger) Fatal(msg string, args ...any) { l.record("fatal", msg, args...) }
func (l *recordingLogger) SetLevel(schemas.LogLevel)     {}
func (l *recordingLogger) SetOutputType(schemas.LoggerOutputType) {
}

func (l *recordingLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return nil
}

func (l *recordingLogger) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// assertWellFormed fails if any line carries fmt's marker for arguments that the
// format string never consumed.
func assertWellFormed(t *testing.T, l *recordingLogger) {
	t.Helper()
	out := l.all()
	if out == "" {
		t.Fatal("expected at least one log line")
	}
	if strings.Contains(out, "%!") {
		t.Errorf("log line has unconsumed args (key/value style against a printf logger):\n%s", out)
	}
}

func TestTokenManagerLogs_TransportFailureIncludesValidity(t *testing.T) {
	logger := &recordingLogger{}
	tm := newCopilotTokenManager("oauth-token", &fasthttp.Client{}, logger)
	tm.tokenExchangeURL = "http://127.0.0.1:1"
	tm.apiToken = "cached-jwt"
	tm.apiBase = defaultAPIBaseURL
	tm.expiresAt = time.Now().Add(30 * time.Second)

	if _, _, err := tm.getToken(); err != nil {
		t.Fatalf("valid cached token should be served: %v", err)
	}

	assertWellFormed(t, logger)
	if !strings.Contains(logger.all(), "valid for") {
		t.Errorf("expected remaining validity in the outage warning, got:\n%s", logger.all())
	}
}

func TestTokenManagerLogs_UpstreamFailureIncludesStatusAndValidity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	logger := &recordingLogger{}
	tm := newCopilotTokenManager("oauth-token", &fasthttp.Client{}, logger)
	tm.tokenExchangeURL = srv.URL
	tm.apiToken = "cached-jwt"
	tm.apiBase = defaultAPIBaseURL
	tm.expiresAt = time.Now().Add(30 * time.Second)

	if _, _, err := tm.getToken(); err != nil {
		t.Fatalf("valid cached token should be served: %v", err)
	}

	assertWellFormed(t, logger)
	out := logger.all()
	if !strings.Contains(out, "503") || !strings.Contains(out, "valid for") {
		t.Errorf("expected status and remaining validity in the outage warning, got:\n%s", out)
	}
}

func TestTokenManagerLogs_SuccessfulRefreshRecordsExpiryAndBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"jwt-new","expires_at":` +
			fmt.Sprint(time.Now().Add(30*time.Minute).Unix()) +
			`,"endpoints":{"api":"https://api.enterprise.githubcopilot.com"}}`))
	}))
	defer srv.Close()

	logger := &recordingLogger{}
	tm := newCopilotTokenManager("oauth-token", &fasthttp.Client{}, logger)
	tm.tokenExchangeURL = srv.URL

	if _, _, err := tm.getToken(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertWellFormed(t, logger)
	out := logger.all()
	if !strings.Contains(out, "token refreshed") || !strings.Contains(out, "api.enterprise.githubcopilot.com") {
		t.Errorf("expected refresh debug line with resolved api base, got:\n%s", out)
	}
	if strings.Contains(out, "jwt-new") {
		t.Error("the JWT must never be logged")
	}
}

func TestTokenManagerLogs_UntrustedBaseWarningIsFormatted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"jwt-new","expires_at":` +
			fmt.Sprint(time.Now().Add(30*time.Minute).Unix()) +
			`,"endpoints":{"api":"https://evil.example.com"}}`))
	}))
	defer srv.Close()

	logger := &recordingLogger{}
	tm := newCopilotTokenManager("oauth-token", &fasthttp.Client{}, logger)
	tm.tokenExchangeURL = srv.URL

	_, apiBase, err := tm.getToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiBase != defaultAPIBaseURL {
		t.Errorf("untrusted base should be rejected, got %q", apiBase)
	}

	assertWellFormed(t, logger)
	if !strings.Contains(logger.all(), "evil.example.com") {
		t.Errorf("expected the rejected URL in the warning, got:\n%s", logger.all())
	}
}
