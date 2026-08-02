package temptoken

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// fakeStore is a minimal in-memory configstore that only implements the
// temp-token CRUD surface. Other methods panic via the embedded interface so
// accidental dependencies fail loudly.
type fakeStore struct {
	configstore.ConfigStore

	mu     sync.Mutex
	byID   map[string]*tables.TempToken
	byHash map[string]*tables.TempToken
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byID:   make(map[string]*tables.TempToken),
		byHash: make(map[string]*tables.TempToken),
	}
}

func (f *fakeStore) CreateTempToken(_ context.Context, tok *tables.TempToken, _ ...*gorm.DB) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Mimic BeforeSave: compute hash from plaintext for lookup. We don't run
	// the actual GORM hook here, so do the same computation the hook does.
	hash := encrypt.HashSHA256(tok.Token)
	tok.TokenHash = hash
	f.byID[tok.ID] = tok
	f.byHash[hash] = tok
	return nil
}

func (f *fakeStore) GetTempTokenByHash(_ context.Context, hash string) (*tables.TempToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.byHash[hash]; ok {
		// Return a copy so caller mutations don't bleed into the store.
		cp := *t
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeStore) DeleteTempTokensByResourceID(_ context.Context, scope, resourceID string, _ ...*gorm.DB) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var deleted int64
	for hash, tok := range f.byHash {
		if tok.Scope == scope && tok.ResourceID == resourceID {
			delete(f.byHash, hash)
			delete(f.byID, tok.ID)
			deleted++
		}
	}
	return deleted, nil
}

func (f *fakeStore) DeleteExpiredTempTokens(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var deleted int64
	for hash, tok := range f.byHash {
		if !tok.ExpiresAt.After(before) {
			delete(f.byHash, hash)
			delete(f.byID, tok.ID)
			deleted++
		}
	}
	return deleted, nil
}

// reusable scope used across most tests
func mcpAuthScope() Scope {
	return Scope{
		Name: "mcp_auth",
		AllowedRoutes: []RoutePattern{
			{Method: "GET", Path: "/api/oauth/per-user/flows/{id}"},
			{Method: "GET", Path: "/api/oauth/per-user/flows/{id}/start"},
		},
		ResourceIDInPath: "{id}",
		MaxTTL:           15 * time.Minute,
	}
}

func newServiceWithMcpAuth(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Register(mcpAuthScope()); err != nil {
		t.Fatalf("register scope: %v", err)
	}
	store := newFakeStore()
	return NewService(store, reg), store
}

func TestMintRejectsUnknownScope(t *testing.T) {
	svc, _ := newServiceWithMcpAuth(t)
	_, err := svc.Mint(context.Background(), "no_such_scope", "flow-1", time.Minute)
	if !errors.Is(err, ErrScopeUnknown) {
		t.Fatalf("expected ErrScopeUnknown, got %v", err)
	}
}

func TestMintRejectsTTLOverMax(t *testing.T) {
	svc, _ := newServiceWithMcpAuth(t)
	_, err := svc.Mint(context.Background(), "mcp_auth", "flow-1", time.Hour)
	if !errors.Is(err, ErrTTLExceedsMax) {
		t.Fatalf("expected ErrTTLExceedsMax, got %v", err)
	}
}

func TestMintRejectsNonPositiveTTL(t *testing.T) {
	svc, _ := newServiceWithMcpAuth(t)
	if _, err := svc.Mint(context.Background(), "mcp_auth", "flow-1", 0); !errors.Is(err, ErrTTLExceedsMax) {
		t.Fatalf("expected ErrTTLExceedsMax for zero TTL, got %v", err)
	}
}

func TestValidateHappyPath(t *testing.T) {
	svc, _ := newServiceWithMcpAuth(t)
	tok, err := svc.Mint(context.Background(), "mcp_auth", "flow-abc", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	got, err := svc.Validate(context.Background(), tok, "GET", "/api/oauth/per-user/flows/flow-abc")
	if err != nil {
		t.Fatalf("validate detail: %v", err)
	}
	if got.Scope != "mcp_auth" || got.ResourceID != "flow-abc" {
		t.Fatalf("got %+v", got)
	}
	if _, err := svc.Validate(context.Background(), tok, "GET", "/api/oauth/per-user/flows/flow-abc/start"); err != nil {
		t.Fatalf("validate start: %v", err)
	}
}

func TestValidateRejectsWrongResourceID(t *testing.T) {
	svc, _ := newServiceWithMcpAuth(t)
	tok, err := svc.Mint(context.Background(), "mcp_auth", "flow-abc", 5*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, err = svc.Validate(context.Background(), tok, "GET", "/api/oauth/per-user/flows/flow-xyz")
	if !errors.Is(err, ErrRouteNotAllowed) {
		t.Fatalf("expected ErrRouteNotAllowed, got %v", err)
	}
}

func TestValidateRejectsWrongMethod(t *testing.T) {
	svc, _ := newServiceWithMcpAuth(t)
	tok, _ := svc.Mint(context.Background(), "mcp_auth", "flow-abc", 5*time.Minute)
	_, err := svc.Validate(context.Background(), tok, "POST", "/api/oauth/per-user/flows/flow-abc")
	if !errors.Is(err, ErrRouteNotAllowed) {
		t.Fatalf("expected ErrRouteNotAllowed, got %v", err)
	}
}

func TestValidateRejectsUnrelatedPath(t *testing.T) {
	svc, _ := newServiceWithMcpAuth(t)
	tok, _ := svc.Mint(context.Background(), "mcp_auth", "flow-abc", 5*time.Minute)
	_, err := svc.Validate(context.Background(), tok, "GET", "/api/config/core")
	if !errors.Is(err, ErrRouteNotAllowed) {
		t.Fatalf("expected ErrRouteNotAllowed, got %v", err)
	}
}

func TestValidateRejectsExpired(t *testing.T) {
	svc, _ := newServiceWithMcpAuth(t)
	// Freeze time inside the service so we can advance it past the TTL.
	now := time.Now()
	svc.now = func() time.Time { return now }
	tok, err := svc.Mint(context.Background(), "mcp_auth", "flow-abc", 1*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	svc.now = func() time.Time { return now.Add(2 * time.Minute) }
	_, err = svc.Validate(context.Background(), tok, "GET", "/api/oauth/per-user/flows/flow-abc")
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestValidateRejectsUnknownToken(t *testing.T) {
	svc, _ := newServiceWithMcpAuth(t)
	_, err := svc.Validate(context.Background(), "definitely-not-a-real-token", "GET", "/api/oauth/per-user/flows/flow-abc")
	if !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("expected ErrTokenNotFound, got %v", err)
	}
}

func TestDeleteExpiredReapsPastTTL(t *testing.T) {
	svc, store := newServiceWithMcpAuth(t)
	now := time.Now()
	svc.now = func() time.Time { return now }
	// Two live + one already-expired.
	if _, err := svc.Mint(context.Background(), "mcp_auth", "flow-a", 5*time.Minute); err != nil {
		t.Fatalf("mint a: %v", err)
	}
	if _, err := svc.Mint(context.Background(), "mcp_auth", "flow-b", 5*time.Minute); err != nil {
		t.Fatalf("mint b: %v", err)
	}
	svc.now = func() time.Time { return now.Add(-10 * time.Minute) }
	if _, err := svc.Mint(context.Background(), "mcp_auth", "flow-c", 1*time.Minute); err != nil {
		t.Fatalf("mint c: %v", err)
	}
	svc.now = func() time.Time { return now }

	n, err := svc.DeleteExpired(context.Background(), now)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row reaped, got %d", n)
	}
	if len(store.byID) != 2 {
		t.Fatalf("expected 2 rows remaining, got %d", len(store.byID))
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(mcpAuthScope()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := reg.Register(mcpAuthScope()); err == nil {
		t.Fatalf("expected error on duplicate Register")
	}
}

func TestRegistryRejectsInvalidScope(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(Scope{Name: ""}); err == nil {
		t.Fatalf("expected error on empty Name")
	}
	if err := reg.Register(Scope{Name: "x", MaxTTL: time.Minute}); err == nil {
		t.Fatalf("expected error on missing routes")
	}
	if err := reg.Register(Scope{
		Name:             "x",
		AllowedRoutes:    []RoutePattern{{Method: "GET", Path: "/static"}},
		ResourceIDInPath: "{id}",
		MaxTTL:           time.Minute,
	}); err == nil {
		t.Fatalf("expected error when ResourceIDInPath is declared but routes don't contain the placeholder")
	}
}
