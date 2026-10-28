package credstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// fakeExchangeProvider narrows fakeOAuth2Provider for the token-exchange
// resolver: only the exchange methods carry configurable behavior.
type fakeExchangeProvider struct {
	fakeOAuth2Provider

	exchangedToken    string
	exchangedTokenErr error
	forceRefreshErr   error

	forceRefreshCalls int
}

func (f *fakeExchangeProvider) GetExchangedAccessToken(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (string, error) {
	if f.exchangedTokenErr != nil {
		return "", f.exchangedTokenErr
	}
	return f.exchangedToken, nil
}

func (f *fakeExchangeProvider) ForceRefreshAccessToken(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) error {
	f.forceRefreshCalls++
	return f.forceRefreshErr
}

func exchangeClientConfig() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:       "client-1",
		Name:     "Exchange Client",
		AuthType: schemas.MCPAuthTypeTokenExchange,
		TokenExchange: &schemas.MCPTokenExchangeConfig{
			Audience: "api://client-1",
			ClientID: schemas.NewSecretVar("exchange-client"),
		},
	}
}

func newExchangeTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func TestTokenExchangeConnectionHeadersSuccess(t *testing.T) {
	r := &tokenExchangeResolver{provider: &fakeExchangeProvider{exchangedToken: "exchanged-token"}}
	headers, err := r.ConnectionHeaders(newExchangeTestContext(), exchangeClientConfig())
	if err != nil {
		t.Fatalf("ConnectionHeaders returned error: %v", err)
	}
	if got := headers.Get("Authorization"); got != "Bearer exchanged-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer exchanged-token")
	}
}

func TestTokenExchangeConnectionHeadersMissingSubject(t *testing.T) {
	r := &tokenExchangeResolver{provider: &fakeExchangeProvider{exchangedTokenErr: schemas.ErrExchangeSubjectTokenMissing}}
	_, err := r.ConnectionHeaders(newExchangeTestContext(), exchangeClientConfig())

	var authErr *schemas.MCPAuthRequiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *MCPAuthRequiredError, got %T: %v", err, err)
	}
	if authErr.Kind != schemas.MCPAuthRequiredKindExchange {
		t.Fatalf("Kind = %q, want %q", authErr.Kind, schemas.MCPAuthRequiredKindExchange)
	}
	if !authErr.SubjectTokenMissing {
		t.Fatal("SubjectTokenMissing = false, want true")
	}
	if authErr.AuthorizeURL != "" || authErr.SubmitURL != "" {
		t.Fatal("exchange auth-required errors must carry no interactive URLs")
	}
	if !strings.Contains(authErr.Message, "Send your identity-provider access token as the Authorization bearer") {
		t.Fatalf("a request that carried no token is told how to carry one, got %q", authErr.Message)
	}
}

// An id_jag client never uses the request's access token, so the fix it names is an SSO sign-in.
func TestTokenExchangeConnectionHeadersMissingIDJAGSubject(t *testing.T) {
	r := &tokenExchangeResolver{provider: &fakeExchangeProvider{exchangedTokenErr: schemas.ErrExchangeSubjectTokenMissing}}
	cfg := exchangeClientConfig()
	cfg.TokenExchange.Profile = schemas.TokenExchangeProfileIDJAG
	_, err := r.ConnectionHeaders(newExchangeTestContext(), cfg)

	var authErr *schemas.MCPAuthRequiredError
	if !errors.As(err, &authErr) || !authErr.SubjectTokenMissing {
		t.Fatalf("expected a subject-missing *MCPAuthRequiredError, got %T: %v", err, err)
	}
	if !strings.Contains(authErr.Message, "Sign in to Bifrost with your identity provider") {
		t.Fatalf("message = %q, want the SSO sign-in instruction", authErr.Message)
	}
}

// The refusal names what happened to a token the caller did send, when the auth layer recorded it.
// Telling a caller who sent a token to send one gives them nothing to act on.
func TestTokenExchangeConnectionHeadersMissingSubjectNamesTheRejection(t *testing.T) {
	r := &tokenExchangeResolver{provider: &fakeExchangeProvider{exchangedTokenErr: schemas.ErrExchangeSubjectTokenMissing}}
	ctx := newExchangeTestContext()
	ctx.SetValue(schemas.BifrostContextKeyMCPInboundBearerOmitted, schemas.MCPInboundBearerRejected)
	_, err := r.ConnectionHeaders(ctx, exchangeClientConfig())
	var authErr *schemas.MCPAuthRequiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *MCPAuthRequiredError, got %T: %v", err, err)
	}
	if !authErr.SubjectTokenMissing {
		t.Fatal("SubjectTokenMissing = false, want true")
	}
	if !strings.Contains(authErr.Message, "was rejected") {
		t.Fatalf("Message = %q, want it to say the token was rejected", authErr.Message)
	}
	if strings.Contains(authErr.Message, "Send your identity-provider access token as the Authorization bearer") {
		t.Fatalf("a caller who sent a token must not be told to send one, got %q", authErr.Message)
	}
}

// A request that used the stored sign-in is told to sign in to Bifrost again, and where when the
// base URL is known, rather than to send a token it never had to carry.
func TestTokenExchangeConnectionHeadersStoredSignInExpired(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "base URL known", baseURL: "https://bifrost.example.com/", want: "Sign in to Bifrost again at https://bifrost.example.com, then retry"},
		{name: "base URL unknown", want: "Sign in to Bifrost again, then retry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &tokenExchangeResolver{provider: &fakeExchangeProvider{exchangedTokenErr: fmt.Errorf("stored sign-in: %w", schemas.ErrStoredSignInExpired)}}
			ctx := newExchangeTestContext()
			if tt.baseURL != "" {
				ctx.SetValue(schemas.BifrostContextKeyMCPCallbackBaseURL, tt.baseURL)
			}
			_, err := r.ConnectionHeaders(ctx, exchangeClientConfig())

			var authErr *schemas.MCPAuthRequiredError
			if !errors.As(err, &authErr) {
				t.Fatalf("expected *MCPAuthRequiredError, got %T: %v", err, err)
			}
			if authErr.Kind != schemas.MCPAuthRequiredKindExchange || !authErr.SubjectTokenMissing {
				t.Fatalf("Kind = %q, SubjectTokenMissing = %v, want exchange and true", authErr.Kind, authErr.SubjectTokenMissing)
			}
			if !strings.Contains(authErr.Message, tt.want) {
				t.Fatalf("Message = %q, want it to contain %q", authErr.Message, tt.want)
			}
			if strings.Contains(authErr.Message, "Send your identity-provider access token") {
				t.Fatalf("a caller using the stored sign-in must not be told to send a token, got %q", authErr.Message)
			}
		})
	}
}

func TestTokenExchangeConnectionHeadersRejected(t *testing.T) {
	provider := &fakeExchangeProvider{
		exchangedTokenErr: &schemas.TokenExchangeRejectedError{Detail: `{"error":"invalid_grant"}`},
	}
	r := &tokenExchangeResolver{provider: provider}
	_, err := r.ConnectionHeaders(newExchangeTestContext(), exchangeClientConfig())

	var authErr *schemas.MCPAuthRequiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *MCPAuthRequiredError, got %T: %v", err, err)
	}
	if authErr.SubjectTokenMissing {
		t.Fatal("SubjectTokenMissing = true, want false for a rejection")
	}
	if authErr.ExchangeError != `{"error":"invalid_grant"}` {
		t.Fatalf("ExchangeError = %q, want provider detail", authErr.ExchangeError)
	}
}

// warnRecorder is a schemas.Logger that records Warn lines; any other method panics through the nil
// embedded Logger, so a test also fails if the resolver logs at a level it should not.
type warnRecorder struct {
	schemas.Logger
	warnings []string
}

// Warn records the formatted line.
func (w *warnRecorder) Warn(msg string, args ...any) {
	w.warnings = append(w.warnings, fmt.Sprintf(msg, args...))
}

// An exchange failure the caller cannot fix can name deployment configuration, such as hosts and
// endpoints, in its text. The caller gets a message without it, and the detail goes to the log.
// Synthetic payloads.
func TestTokenExchangeConnectionHeadersHidesDeploymentDetail(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		detail string
	}{
		{
			name:   "resolution failed",
			err:    fmt.Errorf("failed to resolve identity provider for token exchange: %w", errors.New("synthetic resolver failure for https://as.example.net/token")),
			detail: "as.example.net",
		},
		{
			name:   "token endpoint unreachable",
			err:    fmt.Errorf("token exchange for MCP client Exchange Client failed: %w", &url.Error{Op: "Post", URL: "https://idp.example.com/token", Err: errors.New("connection refused")}),
			detail: "idp.example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &warnRecorder{}
			store := NewCredStore(&fakeExchangeProvider{exchangedTokenErr: tt.err}, nil, logger)
			_, err := store.ConnectionHeaders(newExchangeTestContext(), exchangeClientConfig())
			if err == nil {
				t.Fatal("ConnectionHeaders returned no error")
			}
			for _, leaked := range []string{"example.com", "example.net"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("caller error = %q, must not contain %q", err, leaked)
				}
			}
			if !strings.Contains(err.Error(), "Exchange Client") || !strings.Contains(err.Error(), "administrator") {
				t.Fatalf("caller error = %q, want it to name the server and send the caller to an administrator", err)
			}
			if len(logger.warnings) != 1 || !strings.Contains(logger.warnings[0], tt.detail) {
				t.Fatalf("warnings = %q, want one line carrying %q", logger.warnings, tt.detail)
			}
		})
	}
}

func TestTokenExchangeRequiresPerCallConnection(t *testing.T) {
	r := &tokenExchangeResolver{}
	if !r.RequiresPerCallConnection() {
		t.Fatal("token exchange must require per-call connections")
	}
}

func TestTokenExchangeForceRefreshDelegates(t *testing.T) {
	provider := &fakeExchangeProvider{}
	r := &tokenExchangeResolver{provider: provider}
	if err := r.ForceRefresh(newExchangeTestContext(), exchangeClientConfig()); err != nil {
		t.Fatalf("ForceRefresh returned error: %v", err)
	}
	if provider.forceRefreshCalls != 1 {
		t.Fatalf("ForceRefreshAccessToken calls = %d, want 1", provider.forceRefreshCalls)
	}
}

func TestTokenExchangeAdminConnectionHeaders(t *testing.T) {
	// The retained admin credential resolves via GetAdminAccessToken, the
	// same path the per-user OAuth resolver uses.
	provider := &fakeExchangeProvider{fakeOAuth2Provider: fakeOAuth2Provider{adminAccessToken: "admin-token"}}
	r := &tokenExchangeResolver{provider: provider}

	headers, err := r.AdminConnectionHeaders(context.Background(), exchangeClientConfig())
	if err != nil {
		t.Fatalf("AdminConnectionHeaders returned error: %v", err)
	}
	if got := headers.Get("Authorization"); got != "Bearer admin-token" {
		t.Fatalf("Authorization = %q, want admin bearer", got)
	}

	// A dead/missing retained credential surfaces as a plain error the tool
	// syncer logs and retries.
	dead := &tokenExchangeResolver{provider: &fakeExchangeProvider{fakeOAuth2Provider: fakeOAuth2Provider{adminAccessTokenErr: errors.New("needs reauth")}}}
	if _, err := dead.AdminConnectionHeaders(context.Background(), exchangeClientConfig()); err == nil {
		t.Fatal("expected error when the retained admin credential is unavailable")
	}
}

func TestTokenExchangeNilProvider(t *testing.T) {
	r := &tokenExchangeResolver{}
	if _, err := r.ConnectionHeaders(newExchangeTestContext(), exchangeClientConfig()); err == nil {
		t.Fatal("expected error with nil provider")
	}
	if err := r.ForceRefresh(newExchangeTestContext(), exchangeClientConfig()); err == nil {
		t.Fatal("expected error with nil provider")
	}
	if _, err := r.AdminConnectionHeaders(context.Background(), exchangeClientConfig()); err == nil {
		t.Fatal("expected error with nil provider")
	}
}
