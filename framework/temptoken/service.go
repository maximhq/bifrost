package temptoken

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
)

// Errors returned by the service. Callers (notably the auth middleware) should
// treat these as opaque "not authorized" signals — they're typed so tests can
// assert on them without coupling to error message text.
var (
	// ErrTokenNotFound is returned when the presented token does not match any row.
	ErrTokenNotFound = errors.New("temptoken: token not found")
	// ErrTokenExpired is returned when the matched row's expires_at is in the past.
	ErrTokenExpired = errors.New("temptoken: token expired")
	// ErrScopeUnknown is returned when the row's scope column does not match any
	// registered scope. Indicates either a stale row (scope was deregistered) or
	// a corrupted row.
	ErrScopeUnknown = errors.New("temptoken: token scope is not registered")
	// ErrRouteNotAllowed is returned when the (method, path) of the request does
	// not satisfy any of the scope's AllowedRoutes (after resource_id substitution).
	ErrRouteNotAllowed = errors.New("temptoken: request method and path are not allowed by token scope")
	// ErrTTLExceedsMax is returned by Mint when the caller-requested TTL is
	// larger than the scope's MaxTTL.
	ErrTTLExceedsMax = errors.New("temptoken: requested TTL exceeds scope MaxTTL")
)

// ValidatedToken is the result of a successful Validate call. Callers attach
// these values to the request context so handlers can apply defense-in-depth
// checks.
type ValidatedToken struct {
	ID         string
	Scope      string
	ResourceID string
}

// Service mints and validates temp tokens. It composes the scope registry with
// the configstore-backed persistence layer; nothing else needs the row format
// directly.
type Service struct {
	store    configstore.ConfigStore
	registry *Registry
	now      func() time.Time // injectable for tests
}

// NewService constructs a Service backed by the given store and registry. The
// registry can be empty at construction time — scopes can be Register()'d
// before the first Validate call (in practice, at server startup).
func NewService(store configstore.ConfigStore, registry *Registry) *Service {
	return &Service{store: store, registry: registry, now: time.Now}
}

// Registry exposes the underlying scope registry so callers can register
// scopes without holding a separate reference. Useful for transports that
// receive only the Service from server startup.
func (s *Service) Registry() *Registry { return s.registry }

// Mint creates a new temp token under the given scope, bound to resourceID,
// with the requested TTL. The TTL must be > 0 and <= the scope's MaxTTL. The
// returned plaintext is the value the caller embeds in URLs or hands back to
// the user; it is never persisted in plaintext when encryption is enabled.
func (s *Service) Mint(ctx context.Context, scopeName, resourceID string, ttl time.Duration) (string, error) {
	scope, ok := s.registry.Lookup(scopeName)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrScopeUnknown, scopeName)
	}
	if ttl <= 0 || ttl > scope.MaxTTL {
		return "", fmt.Errorf("%w: requested=%s max=%s", ErrTTLExceedsMax, ttl, scope.MaxTTL)
	}
	plaintext, err := generatePlaintext()
	if err != nil {
		return "", fmt.Errorf("temptoken: failed to generate token: %w", err)
	}
	row := &tables.TempToken{
		ID:         uuid.New().String(),
		Token:      plaintext,
		Scope:      scope.Name,
		ResourceID: resourceID,
		ExpiresAt:  s.now().Add(ttl),
	}
	if err := s.store.CreateTempToken(ctx, row); err != nil {
		return "", fmt.Errorf("temptoken: failed to persist token: %w", err)
	}
	return plaintext, nil
}

// Validate authenticates the presented plaintext for the given request
// (method, path).
func (s *Service) Validate(ctx context.Context, plaintext, method, path string) (*ValidatedToken, error) {
	if plaintext == "" {
		return nil, ErrTokenNotFound
	}
	hash := encrypt.HashSHA256(plaintext)
	row, err := s.store.GetTempTokenByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("temptoken: lookup failed: %w", err)
	}
	if row == nil {
		return nil, ErrTokenNotFound
	}
	if !row.ExpiresAt.After(s.now()) {
		return nil, ErrTokenExpired
	}
	scope, ok := s.registry.Lookup(row.Scope)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrScopeUnknown, row.Scope)
	}
	if !scope.matchesRequest(method, path, row.ResourceID) {
		return nil, ErrRouteNotAllowed
	}
	return &ValidatedToken{
		ID:         row.ID,
		Scope:      row.Scope,
		ResourceID: row.ResourceID,
	}, nil
}

// DeleteExpired removes every token row whose expires_at is at or before
// `before`. Called by [SweepWorker] on its tick; callers passing time.Now()
// reap everything currently past its TTL. Returns the number of rows removed.
func (s *Service) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.store.DeleteExpiredTempTokens(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("temptoken: delete expired failed: %w", err)
	}
	return n, nil
}

// DeleteByResourceID removes every token row matching (scope, resourceID).
// Lifecycle owners call this when the underlying resource the token authorized
// is finished — e.g. the OAuth provider after a per-user flow terminates
// (success or failure) so the link stops working immediately instead of
// waiting for TTL. Returns the number of rows removed; both 0 and N are
// considered successful outcomes — callers should not treat 0 as an error.
func (s *Service) DeleteByResourceID(ctx context.Context, scope, resourceID string) (int64, error) {
	if scope == "" || resourceID == "" {
		return 0, nil
	}
	n, err := s.store.DeleteTempTokensByResourceID(ctx, scope, resourceID)
	if err != nil {
		return 0, fmt.Errorf("temptoken: delete by resource_id failed: %w", err)
	}
	return n, nil
}

// generatePlaintext returns a cryptographically random URL-safe string. 32
// bytes of entropy yields ~43 base64url characters — plenty against any
// realistic brute-force budget given the 15-minute TTL ceiling.
func generatePlaintext() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
