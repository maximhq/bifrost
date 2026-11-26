package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memRelayStore is a Store kept in memory. Like the config store, it fills in missing route IDs
// on write, so a test sees what a real save keeps.
type memRelayStore struct {
	mu     sync.Mutex
	relays map[string]*schemas.RelayConfig
}

// newMemRelayStore returns an empty in-memory store.
func newMemRelayStore() *memRelayStore {
	return &memRelayStore{relays: map[string]*schemas.RelayConfig{}}
}

// ListRelays returns a copy of every stored relay.
func (s *memRelayStore) ListRelays(context.Context) ([]*schemas.RelayConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*schemas.RelayConfig, 0, len(s.relays))
	for _, cfg := range s.relays {
		out = append(out, cfg.Clone())
	}
	return out, nil
}

// GetRelay returns a copy of one stored relay.
func (s *memRelayStore) GetRelay(_ context.Context, name string) (*schemas.RelayConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, ok := s.relays[name]
	if !ok {
		return nil, io.EOF
	}
	return cfg.Clone(), nil
}

// CreateRelay stores a new relay.
func (s *memRelayStore) CreateRelay(_ context.Context, cfg *schemas.RelayConfig, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	schemas.AssignRelayRouteIDs(nil, cfg.Routes)
	s.relays[cfg.Name] = cfg.Clone()
	return nil
}

// UpdateRelay replaces a stored relay, keeping route IDs the way the config store does.
func (s *memRelayStore) UpdateRelay(_ context.Context, cfg *schemas.RelayConfig, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.relays[cfg.Name]; ok {
		schemas.AssignRelayRouteIDs(prev.Routes, cfg.Routes)
	}
	s.relays[cfg.Name] = cfg.Clone()
	return nil
}

// DeleteRelay removes a stored relay.
func (s *memRelayStore) DeleteRelay(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.relays, name)
	return nil
}

// GetRelayGrantedVirtualKeyIDs reports no grants; this store keeps none.
func (s *memRelayStore) GetRelayGrantedVirtualKeyIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

// TestServeCarriesMatchedRouteID checks the hooks see the matched route's stable ID, which is what
// a grant names, and none for a call no route matched.
func TestServeCarriesMatchedRouteID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	const searchID = "aaaaaaaa-0000-4000-8000-000000000001"
	cfg := bearerConfig("tavily", upstream.URL)
	cfg.SkipRouteCheck = true
	cfg.Routes = []schemas.RelayRouteConfig{{ID: searchID, Method: "POST", Path: "/search"}}
	m, pipeline := newTestManager(t, cfg)

	_, bErr := m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "POST", Path: "/search", Headers: map[string]string{}})
	require.Nil(t, bErr)
	_, bErr = m.Serve(testCtx(t), Request{Endpoint: "tavily", Method: "POST", Path: "/other", Headers: map[string]string{}})
	require.Nil(t, bErr)

	require.Len(t, pipeline.pre, 2)
	assert.Equal(t, searchID, pipeline.pre[0].RouteID)
	assert.Empty(t, pipeline.pre[1].RouteID, "an unmatched call under skip_route_check has no route")
}

// TestUpdateKeepsRouteIDs checks an edit that resends routes without IDs keeps each surviving
// route's ID, and that the live relay serves the stored IDs.
func TestUpdateKeepsRouteIDs(t *testing.T) {
	store := newMemRelayStore()
	m := NewManager(store, nil)
	cfg := bearerConfig("tavily", "https://api.example.com")
	cfg.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/search"}, {Method: "POST", Path: "/extract"}}
	created, err := m.Create(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, created.Routes, 2)
	searchID, extractID := created.Routes[0].ID, created.Routes[1].ID
	require.NotEmpty(t, searchID)
	require.NotEqual(t, searchID, extractID)

	edit := bearerConfig("tavily", "https://api.example.com")
	edit.Routes = []schemas.RelayRouteConfig{{Method: "POST", Path: "/extract"}, {Method: "GET", Path: "/usage"}}
	updated, err := m.Update(context.Background(), "tavily", edit)
	require.NoError(t, err)
	require.Len(t, updated.Routes, 2)
	assert.Equal(t, extractID, updated.Routes[0].ID, "the kept route keeps its ID")
	assert.NotEqual(t, searchID, updated.Routes[1].ID, "a removed route's ID is not reused")

	live := m.get("tavily")
	require.NotNil(t, live)
	assert.Equal(t, extractID, live.cfg.Routes[0].ID, "the live relay serves the stored IDs")
}
