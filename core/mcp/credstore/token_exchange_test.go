package credstore

import (
	"context"
	"errors"
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
