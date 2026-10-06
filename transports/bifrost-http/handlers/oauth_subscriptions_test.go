package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/antigravity"
	"github.com/maximhq/bifrost/core/providers/kiro"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// fakeClock is a settable clock for the session stores.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

// oauthSubscriptionRequest builds a POST request context. Init attaches a
// server so the context methods used for upstream timeouts are safe to call.
func oauthSubscriptionRequest(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&fasthttp.Request{}, nil, nil)
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetBody([]byte(body))
	return ctx
}

func decodeOAuthResponse[T any](t *testing.T, ctx *fasthttp.RequestCtx) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(ctx.Response.Body(), &out); err != nil {
		t.Fatalf("decode response %q: %v", ctx.Response.Body(), err)
	}
	return out
}

func oauthErrorMessage(t *testing.T, ctx *fasthttp.RequestCtx) string {
	t.Helper()
	body := decodeOAuthResponse[struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}](t, ctx)
	return body.Error.Message
}

func newOAuthSubscriptionHandlerForTest(t *testing.T, config *lib.Config) (*OAuthSubscriptionHandler, *fakeClock) {
	t.Helper()
	SetLogger(&mockLogger{})
	if config == nil {
		config = &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{}}
	}
	h := NewOAuthSubscriptionHandler(config)
	clock := newFakeClock()
	h.antigravitySessions.now = clock.now
	h.kiroSessions.now = clock.now
	return h, clock
}

// stubUpstream swaps a package-level upstream function for the test's duration.
func stubUpstream[F any](t *testing.T, target *F, fake F) {
	t.Helper()
	original := *target
	*target = fake
	t.Cleanup(func() { *target = original })
}

func TestOAuthSessionStoreExpiresAndPrunes(t *testing.T) {
	clock := newFakeClock()
	store := newOAuthSessionStore[int](time.Minute, 4)
	store.now = clock.now

	id, expiresAt, err := store.put(1, time.Time{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !expiresAt.Equal(clock.t.Add(time.Minute)) {
		t.Fatalf("expiresAt = %v, want TTL from now", expiresAt)
	}
	if _, notAfter, _ := store.put(2, clock.t.Add(10*time.Second)); !notAfter.Equal(clock.t.Add(10 * time.Second)) {
		t.Fatalf("notAfter earlier than the TTL must cap the expiry, got %v", notAfter)
	}

	called := false
	if err := store.update(id, func(e *oauthSessionEntry[int], _ time.Time) bool { called = e.value == 1; return false }); err != nil || !called {
		t.Fatalf("update live session: err=%v called=%v", err, called)
	}
	if err := store.update("missing", func(*oauthSessionEntry[int], time.Time) bool { return false }); !errors.Is(err, errOAuthSessionNotFound) {
		t.Fatalf("unknown id: err=%v, want errOAuthSessionNotFound", err)
	}

	clock.advance(time.Minute)
	if err := store.update(id, func(*oauthSessionEntry[int], time.Time) bool {
		t.Fatal("fn must not run for an expired session")
		return false
	}); !errors.Is(err, errOAuthSessionExpired) {
		t.Fatalf("expired: err=%v, want errOAuthSessionExpired", err)
	}
	if err := store.update(id, func(*oauthSessionEntry[int], time.Time) bool { return false }); !errors.Is(err, errOAuthSessionNotFound) {
		t.Fatalf("an expired session is removed on access, got %v", err)
	}
	// The second session expired too; the next put prunes it.
	if _, _, err := store.put(3, time.Time{}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if n := store.len(); n != 1 {
		t.Fatalf("store holds %d sessions after prune, want 1", n)
	}
}

func TestOAuthSessionStoreEvictsOldestAtCapacity(t *testing.T) {
	clock := newFakeClock()
	store := newOAuthSessionStore[int](time.Hour, 3)
	store.now = clock.now
	ids := make([]string, 0, 4)
	for i := range 4 {
		id, _, err := store.put(i, time.Time{})
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		ids = append(ids, id)
		clock.advance(time.Second)
	}
	if n := store.len(); n != 3 {
		t.Fatalf("store holds %d sessions, want cap 3", n)
	}
	noop := func(*oauthSessionEntry[int], time.Time) bool { return false }
	if err := store.update(ids[0], noop); !errors.Is(err, errOAuthSessionNotFound) {
		t.Fatalf("oldest session should be evicted, got %v", err)
	}
	for _, id := range ids[1:] {
		if err := store.update(id, noop); err != nil {
			t.Fatalf("newer session %s evicted: %v", id, err)
		}
	}
}

func TestParseAntigravityCallback(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantCode  string
		wantState string
		wantErr   string
	}{
		{name: "full callback URL", input: "http://127.0.0.1:51121/callback?state=abc&code=4%2F0AbCd&scope=email", wantCode: "4/0AbCd", wantState: "abc"},
		{name: "surrounding whitespace", input: "  http://127.0.0.1:51121/callback?code=c1&state=s1\n", wantCode: "c1", wantState: "s1"},
		{name: "query string only", input: "?code=c2&state=s2", wantCode: "c2", wantState: "s2"},
		{name: "bare query pairs", input: "code=c3&state=s3", wantCode: "c3", wantState: "s3"},
		{name: "bare code", input: "4/0AbCdEf-gh_IJ", wantCode: "4/0AbCdEf-gh_IJ"},
		{name: "percent-encoded bare code", input: "4%2F0AbCd", wantCode: "4/0AbCd"},
		{name: "empty", input: "   ", wantErr: "callback_url is required"},
		{name: "URL without code", input: "http://127.0.0.1:51121/callback?state=abc", wantErr: "no code parameter"},
		{name: "URL without state", input: "http://127.0.0.1:51121/callback?code=abc", wantErr: "no state parameter"},
		{name: "consent denied", input: "http://127.0.0.1:51121/callback?error=access_denied&state=abc", wantErr: "authorization failed: access_denied"},
		{name: "garbage with spaces", input: "not a code", wantErr: "neither a URL nor an authorization code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, state, err := parseAntigravityCallback(tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if code != tc.wantCode || state != tc.wantState {
				t.Fatalf("got code=%q state=%q, want code=%q state=%q", code, state, tc.wantCode, tc.wantState)
			}
		})
	}
}

// startAntigravityForTest runs the start endpoint and returns the session id and
// the state embedded in the auth URL.
func startAntigravityForTest(t *testing.T, h *OAuthSubscriptionHandler) (string, string) {
	t.Helper()
	stubUpstream(t, &antigravityNewPKCE, func() (string, string, error) { return "verifier-1", "challenge-1", nil })
	stubUpstream(t, &antigravityBuildAuthURL, func(redirectURI, state, challenge string) string {
		return "https://accounts.example/auth?" + url.Values{"redirect_uri": {redirectURI}, "state": {state}, "code_challenge": {challenge}}.Encode()
	})
	ctx := oauthSubscriptionRequest(`{}`)
	h.startAntigravityLogin(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("start status = %d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	resp := decodeOAuthResponse[antigravityStartResponse](t, ctx)
	if resp.SessionID == "" || resp.RedirectURI != antigravity.DefaultRedirectURI {
		t.Fatalf("unexpected start response: %+v", resp)
	}
	if _, err := time.Parse(time.RFC3339, resp.ExpiresAt); err != nil {
		t.Fatalf("expires_at %q is not RFC3339: %v", resp.ExpiresAt, err)
	}
	authURL, err := url.Parse(resp.AuthURL)
	if err != nil {
		t.Fatalf("auth_url: %v", err)
	}
	if got := authURL.Query().Get("code_challenge"); got != "challenge-1" {
		t.Fatalf("auth_url code_challenge = %q", got)
	}
	state := authURL.Query().Get("state")
	if state == "" {
		t.Fatal("auth_url carries no state")
	}
	return resp.SessionID, state
}

func completeAntigravityForTest(h *OAuthSubscriptionHandler, sessionID, callback string) *fasthttp.RequestCtx {
	body, _ := json.Marshal(antigravityCompleteRequest{SessionID: sessionID, CallbackURL: callback})
	ctx := oauthSubscriptionRequest(string(body))
	h.completeAntigravityLogin(ctx)
	return ctx
}

func TestAntigravityCompleteRejectsBadSessions(t *testing.T) {
	h, clock := newOAuthSubscriptionHandlerForTest(t, nil)
	var exchanges atomic.Int32
	stubUpstream(t, &antigravityExchangeCode, func(context.Context, string, string, string) (*antigravity.Credentials, error) {
		exchanges.Add(1)
		return &antigravity.Credentials{RefreshToken: "1//refresh"}, nil
	})
	sessionID, state := startAntigravityForTest(t, h)

	ctx := oauthSubscriptionRequest(`{"session_id":`)
	h.completeAntigravityLogin(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("malformed body: status %d", ctx.Response.StatusCode())
	}

	ctx = completeAntigravityForTest(h, "", "code-only")
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(oauthErrorMessage(t, ctx), "session_id is required") {
		t.Fatalf("missing session id: status %d body %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	ctx = completeAntigravityForTest(h, "does-not-exist", "code-only")
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(oauthErrorMessage(t, ctx), "unknown login session") {
		t.Fatalf("unknown session: status %d body %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	ctx = completeAntigravityForTest(h, sessionID, "http://127.0.0.1:51121/callback?code=c&state=someone-else")
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(oauthErrorMessage(t, ctx), "state mismatch") {
		t.Fatalf("state mismatch: status %d body %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	clock.advance(oauthSubscriptionSessionTTL)
	ctx = completeAntigravityForTest(h, sessionID, "http://127.0.0.1:51121/callback?code=c&state="+url.QueryEscape(state))
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(oauthErrorMessage(t, ctx), "expired") {
		t.Fatalf("expired session: status %d body %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if n := exchanges.Load(); n != 0 {
		t.Fatalf("rejected completions must not reach upstream, got %d exchanges", n)
	}
}

func TestAntigravityCompleteExchangesAndSuggestsName(t *testing.T) {
	config := &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.Antigravity: {Keys: []schemas.Key{{ID: "k1", Name: "antigravity-account-2"}}},
	}}
	h, _ := newOAuthSubscriptionHandlerForTest(t, config)

	fail := true
	var gotCode, gotRedirect, gotVerifier string
	stubUpstream(t, &antigravityExchangeCode, func(_ context.Context, code, redirectURI, verifier string) (*antigravity.Credentials, error) {
		gotCode, gotRedirect, gotVerifier = code, redirectURI, verifier
		if fail {
			return nil, errors.New("invalid_grant")
		}
		return &antigravity.Credentials{RefreshToken: "1//refresh", ProjectID: "proj-1"}, nil
	})
	sessionID, state := startAntigravityForTest(t, h)
	callback := "http://127.0.0.1:51121/callback?code=4%2F0Ab&state=" + url.QueryEscape(state)

	ctx := completeAntigravityForTest(h, sessionID, callback)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(oauthErrorMessage(t, ctx), "invalid_grant") {
		t.Fatalf("failed exchange: status %d body %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	// A failed exchange releases the session so a corrected code can be retried.
	fail = false
	ctx = completeAntigravityForTest(h, sessionID, callback)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("retry status %d body %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if gotCode != "4/0Ab" || gotRedirect != antigravity.DefaultRedirectURI || gotVerifier != "verifier-1" {
		t.Fatalf("exchange got code=%q redirect=%q verifier=%q", gotCode, gotRedirect, gotVerifier)
	}
	resp := decodeOAuthResponse[antigravityCompleteResponse](t, ctx)
	creds, err := antigravity.ParseCredentials(resp.Credential)
	if err != nil {
		t.Fatalf("credential does not parse: %v", err)
	}
	if creds.RefreshToken != "1//refresh" || creds.ProjectID != "proj-1" || resp.ProjectID != "proj-1" {
		t.Fatalf("unexpected credential %+v / response %+v", creds, resp)
	}
	// One key exists, so n starts at 2; that name is taken, so 3.
	if resp.SuggestedName != "antigravity-account-3" {
		t.Fatalf("suggested_name = %q, want antigravity-account-3", resp.SuggestedName)
	}

	// Success consumes the session.
	ctx = completeAntigravityForTest(h, sessionID, callback)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("reused session: status %d", ctx.Response.StatusCode())
	}
}

func TestAntigravityCompleteSuggestsEmailWhenKnown(t *testing.T) {
	h, _ := newOAuthSubscriptionHandlerForTest(t, nil)
	stubUpstream(t, &antigravityExchangeCode, func(context.Context, string, string, string) (*antigravity.Credentials, error) {
		return &antigravity.Credentials{RefreshToken: "1//refresh", Email: "dev@example.com"}, nil
	})
	sessionID, _ := startAntigravityForTest(t, h)
	ctx := completeAntigravityForTest(h, sessionID, "4/0Ab")
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status %d body %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	resp := decodeOAuthResponse[antigravityCompleteResponse](t, ctx)
	if resp.SuggestedName != "dev@example.com" || resp.Email != "dev@example.com" {
		t.Fatalf("unexpected response %+v", resp)
	}
}

func TestKiroStartValidatesMethod(t *testing.T) {
	h, _ := newOAuthSubscriptionHandlerForTest(t, nil)
	var starts atomic.Int32
	stubUpstream(t, &kiroStartDeviceLogin, func(context.Context, string) (*kiro.DeviceAuthorization, error) {
		starts.Add(1)
		return nil, errors.New("unreachable")
	})
	for _, body := range []string{`{}`, `{"method":"facebook"}`, `not json`} {
		ctx := oauthSubscriptionRequest(body)
		h.startKiroLogin(ctx)
		if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
			t.Fatalf("body %s: status %d", body, ctx.Response.StatusCode())
		}
	}
	if n := starts.Load(); n != 0 {
		t.Fatalf("invalid requests reached upstream %d times", n)
	}

	ctx := oauthSubscriptionRequest(`{"method":"github"}`)
	h.startKiroLogin(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadGateway {
		t.Fatalf("upstream failure: status %d", ctx.Response.StatusCode())
	}
}

func startKiroForTest(t *testing.T, h *OAuthSubscriptionHandler, clock *fakeClock, method string) kiroStartResponse {
	t.Helper()
	stubUpstream(t, &kiroStartDeviceLogin, func(_ context.Context, m string) (*kiro.DeviceAuthorization, error) {
		return &kiro.DeviceAuthorization{
			Method:          m,
			DeviceCode:      "device-code",
			UserCode:        "ABCD-EFGH",
			VerificationURI: "https://device.example/verify",
			ExpiresAt:       clock.t.Add(10 * time.Minute),
			Interval:        5 * time.Second,
		}, nil
	})
	ctx := oauthSubscriptionRequest(`{"method":"` + method + `"}`)
	h.startKiroLogin(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("start status %d body %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	resp := decodeOAuthResponse[kiroStartResponse](t, ctx)
	if resp.SessionID == "" || resp.UserCode != "ABCD-EFGH" || resp.IntervalSeconds != 5 {
		t.Fatalf("unexpected start response %+v", resp)
	}
	expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil || !expiresAt.Equal(clock.t.Add(10*time.Minute)) {
		t.Fatalf("expires_at %q (err %v) should follow the device code lifetime", resp.ExpiresAt, err)
	}
	return resp
}

func pollKiroForTest(t *testing.T, h *OAuthSubscriptionHandler, sessionID string) (int, kiroPollResponse) {
	t.Helper()
	ctx := oauthSubscriptionRequest(`{"session_id":"` + sessionID + `"}`)
	h.pollKiroLogin(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		return ctx.Response.StatusCode(), kiroPollResponse{}
	}
	return fasthttp.StatusOK, decodeOAuthResponse[kiroPollResponse](t, ctx)
}

func TestKiroPollThrottlesAndCompletes(t *testing.T) {
	h, clock := newOAuthSubscriptionHandlerForTest(t, nil)
	start := startKiroForTest(t, h, clock, "builder-id")

	var polls atomic.Int32
	status := kiroPollStatusPending
	stubUpstream(t, &kiroPollDeviceLogin, func(_ context.Context, auth *kiro.DeviceAuthorization) (*kiro.Credentials, string, error) {
		polls.Add(1)
		if auth.DeviceCode != "device-code" {
			t.Errorf("poll got device code %q", auth.DeviceCode)
		}
		if status == kiroPollStatusComplete {
			return &kiro.Credentials{RefreshToken: "aorAAAA", ClientID: "cid", ClientSecret: "csecret", AuthMethod: "builder-id"}, status, nil
		}
		return nil, status, nil
	})

	// Before the interval elapses: pending without an upstream call.
	if code, resp := pollKiroForTest(t, h, start.SessionID); code != fasthttp.StatusOK || resp.Status != kiroPollStatusPending || resp.IntervalSeconds != 5 {
		t.Fatalf("early poll: code %d resp %+v", code, resp)
	}
	if n := polls.Load(); n != 0 {
		t.Fatalf("early poll reached upstream %d times", n)
	}

	clock.advance(5 * time.Second)
	if _, resp := pollKiroForTest(t, h, start.SessionID); resp.Status != kiroPollStatusPending {
		t.Fatalf("due poll: %+v", resp)
	}
	if n := polls.Load(); n != 1 {
		t.Fatalf("due poll made %d upstream calls, want 1", n)
	}
	// Immediately again: throttled.
	pollKiroForTest(t, h, start.SessionID)
	if n := polls.Load(); n != 1 {
		t.Fatalf("throttled poll reached upstream, calls=%d", n)
	}

	// slow_down widens the interval by 5s and is reported as pending.
	clock.advance(5 * time.Second)
	status = kiroPollStatusSlowDown
	if _, resp := pollKiroForTest(t, h, start.SessionID); resp.Status != kiroPollStatusPending || resp.IntervalSeconds != 10 {
		t.Fatalf("slow_down poll: %+v", resp)
	}
	clock.advance(5 * time.Second)
	pollKiroForTest(t, h, start.SessionID)
	if n := polls.Load(); n != 2 {
		t.Fatalf("poll inside the widened interval reached upstream, calls=%d", n)
	}

	clock.advance(5 * time.Second)
	status = kiroPollStatusComplete
	_, resp := pollKiroForTest(t, h, start.SessionID)
	if resp.Status != kiroPollStatusComplete {
		t.Fatalf("complete poll: %+v", resp)
	}
	creds, err := kiro.ParseCredentials(resp.Credential)
	if err != nil {
		t.Fatalf("credential does not parse: %v", err)
	}
	if creds.RefreshToken != "aorAAAA" || creds.ClientID != "cid" || creds.ClientSecret != "csecret" {
		t.Fatalf("unexpected credential %+v", creds)
	}
	if want := "kiro-builder-id-" + start.SessionID[:6]; resp.SuggestedName != want {
		t.Fatalf("suggested_name = %q, want %q", resp.SuggestedName, want)
	}

	// A completed login is consumed.
	if code, _ := pollKiroForTest(t, h, start.SessionID); code != fasthttp.StatusBadRequest {
		t.Fatalf("poll after completion: status %d", code)
	}
}

func TestKiroPollReportsExpiryAndErrors(t *testing.T) {
	h, clock := newOAuthSubscriptionHandlerForTest(t, nil)

	if code, _ := pollKiroForTest(t, h, "does-not-exist"); code != fasthttp.StatusBadRequest {
		t.Fatalf("unknown session: status %d", code)
	}
	ctx := oauthSubscriptionRequest(`{}`)
	h.pollKiroLogin(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("missing session id: status %d", ctx.Response.StatusCode())
	}

	expired := startKiroForTest(t, h, clock, "google")
	failed := startKiroForTest(t, h, clock, "github")
	stubUpstream(t, &kiroPollDeviceLogin, func(context.Context, *kiro.DeviceAuthorization) (*kiro.Credentials, string, error) {
		return nil, "", errors.New("access_denied")
	})

	clock.advance(5 * time.Second)
	_, resp := pollKiroForTest(t, h, failed.SessionID)
	if resp.Status != kiroPollStatusError || !strings.Contains(resp.Error, "access_denied") {
		t.Fatalf("failed login: %+v", resp)
	}
	if code, _ := pollKiroForTest(t, h, failed.SessionID); code != fasthttp.StatusBadRequest {
		t.Fatalf("a failed login is terminal, poll status %d", code)
	}

	clock.advance(10 * time.Minute)
	if _, resp := pollKiroForTest(t, h, expired.SessionID); resp.Status != kiroPollStatusExpired {
		t.Fatalf("expired session: %+v", resp)
	}
}
