package oidc

import (
	"context"
	"fmt"
	"strings"
	"sync"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/sync/singleflight"
)

// OIDCProvider manages OIDC discovery, JWKS caching, and token validation.
// It is created once at startup and shared across all request handlers.
type OIDCProvider struct {
	provider *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
	config   *OIDCConfig
	sf       singleflight.Group
	mu       sync.RWMutex
}

// NewOIDCProvider creates a new OIDC provider by performing discovery against
// the issuer URL. Uses context.Background() per D-07 -- never request context,
// because go-oidc caches the JWKS keyset with the context used for creation.
// Fails fast if discovery fails per D-04.
func NewOIDCProvider(config *OIDCConfig) (*OIDCProvider, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid OIDC config: %w", err)
	}

	// CRITICAL: Use context.Background(), NEVER request context (go-oidc issue #339).
	// The provider caches the JWKS keyset internally, and the context used here
	// controls the lifecycle of that cache. Request context would cancel the cache
	// when the request completes.
	provider, err := gooidc.NewProvider(context.Background(), config.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed for %s: %w", config.IssuerURL, err)
	}

	verifier := provider.Verifier(&gooidc.Config{
		ClientID: config.ClientID,
	})

	return &OIDCProvider{
		provider: provider,
		verifier: verifier,
		config:   config,
	}, nil
}

// ValidateToken validates a JWT access token against the OIDC provider's JWKS.
// It first verifies the token signature and standard claims (iss, aud, exp) via
// go-oidc's IDTokenVerifier, then parses the custom Keycloak claims.
//
// On unknown kid (key rotation), retries once with a fresh JWKS fetch via
// singleflight to prevent concurrent refresh races per D-06/D-07.
func (p *OIDCProvider) ValidateToken(ctx context.Context, rawToken string) (*KeycloakClaims, error) {
	// Use the current verifier (protected by RWMutex for hot-swap during JWKS refresh)
	p.mu.RLock()
	verifier := p.verifier
	p.mu.RUnlock()

	// Step 1: Verify token signature and standard claims via go-oidc
	idToken, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		// Check if this is an unknown kid error (key rotation scenario per D-07).
		// go-oidc returns errors containing "failed to verify" when JWKS doesn't
		// have the kid. Retry once with a fresh provider to pick up rotated keys.
		if isUnknownKidError(err) {
			idToken, err = p.refreshAndRetry(ctx, rawToken)
			if err != nil {
				return nil, fmt.Errorf("token verification failed after JWKS refresh: %w", err)
			}
		} else {
			return nil, fmt.Errorf("token verification failed: %w", err)
		}
	}

	// Step 2: Extract custom Keycloak claims
	claims := &KeycloakClaims{}
	if err := idToken.Claims(claims); err != nil {
		return nil, fmt.Errorf("failed to extract claims: %w", err)
	}

	// Set standard claims from the verified token
	claims.Subject = idToken.Subject
	claims.Issuer = idToken.Issuer

	return claims, nil
}

// refreshAndRetry performs a deduplicated JWKS refresh via singleflight and
// retries token verification with the fresh keyset. Per D-06, this prevents
// concurrent requests from all independently hitting the JWKS endpoint when
// a key rotation occurs.
func (p *OIDCProvider) refreshAndRetry(ctx context.Context, rawToken string) (*gooidc.IDToken, error) {
	result, err, _ := p.sf.Do("jwks-refresh", func() (interface{}, error) {
		// Re-create provider to force fresh JWKS fetch.
		// Use context.Background() per D-07 -- never request context for provider creation.
		newProvider, provErr := gooidc.NewProvider(context.Background(), p.config.IssuerURL)
		if provErr != nil {
			return nil, fmt.Errorf("JWKS refresh failed: %w", provErr)
		}

		newVerifier := newProvider.Verifier(&gooidc.Config{
			ClientID: p.config.ClientID,
		})

		// Update provider and verifier atomically
		p.mu.Lock()
		p.provider = newProvider
		p.verifier = newVerifier
		p.mu.Unlock()

		return newVerifier, nil
	})
	if err != nil {
		return nil, err
	}

	// Use the refreshed verifier to retry verification
	verifier := result.(*gooidc.IDTokenVerifier)
	return verifier.Verify(ctx, rawToken)
}

// isUnknownKidError checks if a verification error is due to an unknown key ID,
// which indicates JWKS key rotation has occurred.
func isUnknownKidError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// go-oidc wraps key-not-found errors with these patterns
	return strings.Contains(msg, "failed to verify") ||
		strings.Contains(msg, "no keys") ||
		strings.Contains(msg, "key not found")
}

// IsJWT checks if a token string looks like a JWT (3 dot-separated base64url segments).
// Used to distinguish OIDC JWTs from session UUIDs in the Authorization header.
// Session tokens are 36-char UUID strings; JWTs are much longer with dots.
func IsJWT(token string) bool {
	if len(token) < 10 {
		return false
	}
	parts := strings.SplitN(token, ".", 4)
	return len(parts) == 3 && len(parts[0]) > 0 && len(parts[1]) > 0 && len(parts[2]) > 0
}

// GetConfig returns the OIDC configuration.
func (p *OIDCProvider) GetConfig() *OIDCConfig {
	return p.config
}
