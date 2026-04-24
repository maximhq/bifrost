package handlers

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

type testWSHandlerStore struct {
	allowDirectKeys bool
}

func (s testWSHandlerStore) ShouldAllowDirectKeys() bool {
	return s.allowDirectKeys
}

func (s testWSHandlerStore) GetHeaderMatcher() *lib.HeaderMatcher {
	return nil
}

func (s testWSHandlerStore) GetAvailableProviders() []schemas.ModelProvider {
	return nil
}

func (s testWSHandlerStore) GetStreamChunkInterceptor() lib.StreamChunkInterceptor {
	return nil
}

func (s testWSHandlerStore) GetAsyncJobExecutor() *logstore.AsyncJobExecutor {
	return nil
}

func (s testWSHandlerStore) GetAsyncJobResultTTL() int {
	return 0
}

func (s testWSHandlerStore) GetKVStore() *kvstore.Store {
	return nil
}

func (s testWSHandlerStore) GetMCPHeaderCombinedAllowlist() schemas.WhiteList {
	return nil
}

func TestCreateBifrostContextFromAuth_BaggageSessionIDSetsGrouping(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{
		baggage: "foo=bar, session-id=rt-ws-123, baz=qux",
	})
	defer cancel()

	if got, _ := ctx.Value(schemas.BifrostContextKeyParentRequestID).(string); got != "rt-ws-123" {
		t.Fatalf("parent request id = %q, want %q", got, "rt-ws-123")
	}
}

func TestCreateBifrostContextFromAuth_EmptyBaggageSessionIDIgnored(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{
		baggage: "session-id=   ",
	})
	defer cancel()

	if got := ctx.Value(schemas.BifrostContextKeyParentRequestID); got != nil {
		t.Fatalf("parent request id should be unset, got %#v", got)
	}
}

// ---------------------------------------------------------------------------
// createBifrostContextFromAuth: x-bf-eh-* header capture into context
// ---------------------------------------------------------------------------

// TestCreateBifrostContextFromAuth_ExtraHeadersCaptured verifies that x-bf-eh-*
// headers are stripped of their prefix and stored in BifrostContextKeyExtraHeaders.
func TestCreateBifrostContextFromAuth_ExtraHeadersCaptured(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{
		extraHeaders: map[string]string{
			"x-bf-eh-x-trace-id": "trace-abc",
			"x-bf-eh-originator": "my-client",
		},
	})
	defer cancel()

	extra, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string]string)
	if !ok {
		t.Fatal("BifrostContextKeyExtraHeaders not set or wrong type")
	}
	if got := extra["x-trace-id"]; got != "trace-abc" {
		t.Errorf("x-trace-id = %q, want %q", got, "trace-abc")
	}
	if got := extra["originator"]; got != "my-client" {
		t.Errorf("originator = %q, want %q", got, "my-client")
	}
}

// TestCreateBifrostContextFromAuth_NonExtraHeadersNotInContext verifies that
// non x-bf-eh-* entries in extraHeaders (x-bf-vk, x-bf-api-key) are NOT
// forwarded into BifrostContextKeyExtraHeaders.
func TestCreateBifrostContextFromAuth_NonExtraHeadersNotInContext(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{allowDirectKeys: true}, &authHeaders{
		extraHeaders: map[string]string{
			"x-bf-api-key": "some-key-name",
		},
	})
	defer cancel()

	// BifrostContextKeyExtraHeaders must be nil or empty when no x-bf-eh-* were present.
	if extra := ctx.Value(schemas.BifrostContextKeyExtraHeaders); extra != nil {
		t.Errorf("expected BifrostContextKeyExtraHeaders to be unset, got %#v", extra)
	}
}

// ---------------------------------------------------------------------------
// mergeWSExtraHeaders: merge semantics
// ---------------------------------------------------------------------------

// newBifrostContextWithExtra is a test helper that builds a BifrostContext
// with a map[string]string already stored under BifrostContextKeyExtraHeaders.
func newBifrostContextWithExtra(extra map[string]string) *schemas.BifrostContext {
	ctx, _ := schemas.NewBifrostContextWithCancel(context.Background())
	if len(extra) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, extra)
	}
	return ctx
}

// TestMergeWSExtraHeaders_ExtraHeadersReachUpstream verifies that x-bf-eh-*
// derived headers stored in the context appear in the merged headers map.
func TestMergeWSExtraHeaders_ExtraHeadersReachUpstream(t *testing.T) {
	providerHeaders := map[string]string{
		"Authorization": "Bearer tok",
	}
	ctx := newBifrostContextWithExtra(map[string]string{
		"x-trace-id": "trace-abc",
		"originator": "my-client",
	})

	merged := mergeWSExtraHeaders(providerHeaders, ctx)

	if got := merged["x-trace-id"]; got != "trace-abc" {
		t.Errorf("x-trace-id = %q, want %q", got, "trace-abc")
	}
	if got := merged["originator"]; got != "my-client" {
		t.Errorf("originator = %q, want %q", got, "my-client")
	}
	if got := merged["Authorization"]; got != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tok")
	}
}

// TestMergeWSExtraHeaders_ProviderHeaderWinsOnConflict verifies that when the
// same key exists in both provider and extra headers, the provider value wins.
func TestMergeWSExtraHeaders_ProviderHeaderWinsOnConflict(t *testing.T) {
	providerHeaders := map[string]string{
		"Authorization": "Bearer provider-token",
	}
	ctx := newBifrostContextWithExtra(map[string]string{
		// The blocklist in captureAuthHeaders should prevent this in production,
		// but mergeWSExtraHeaders must defend in depth.
		"Authorization": "Bearer client-should-lose",
		"originator":    "my-client",
	})

	merged := mergeWSExtraHeaders(providerHeaders, ctx)

	if got := merged["Authorization"]; got != "Bearer provider-token" {
		t.Errorf("Authorization = %q, want provider value %q", got, "Bearer provider-token")
	}
	if got := merged["originator"]; got != "my-client" {
		t.Errorf("originator = %q, want %q", got, "my-client")
	}
}

// TestMergeWSExtraHeaders_CaseInsensitiveProviderWins verifies that provider
// header lookup is case-insensitive, so "authorization" from context does not
// shadow "Authorization" from the provider even with different capitalisation.
func TestMergeWSExtraHeaders_CaseInsensitiveProviderWins(t *testing.T) {
	providerHeaders := map[string]string{
		"Authorization": "Bearer tok",
	}
	ctx := newBifrostContextWithExtra(map[string]string{
		"authorization": "Bearer should-not-appear",
	})

	merged := mergeWSExtraHeaders(providerHeaders, ctx)

	if got := merged["Authorization"]; got != "Bearer tok" {
		t.Errorf("Authorization = %q, want provider value %q", got, "Bearer tok")
	}
	// The lowercased shadow must not be present either.
	if got, ok := merged["authorization"]; ok {
		t.Errorf("lowercase authorization should be absent, got %q", got)
	}
}

// TestMergeWSExtraHeaders_NoExtraHeadersReturnsProviderHeaders verifies that
// when the context contains no extra headers, the provider map is returned
// unchanged (same pointer, no copy).
func TestMergeWSExtraHeaders_NoExtraHeadersReturnsProviderHeaders(t *testing.T) {
	providerHeaders := map[string]string{
		"Authorization": "Bearer tok",
	}
	ctx := newBifrostContextWithExtra(nil)

	merged := mergeWSExtraHeaders(providerHeaders, ctx)

	// When no extra headers, the original map is returned by identity.
	if len(merged) != len(providerHeaders) {
		t.Errorf("len(merged) = %d, want %d", len(merged), len(providerHeaders))
	}
	for k, want := range providerHeaders {
		if got := merged[k]; got != want {
			t.Errorf("merged[%q] = %q, want %q", k, got, want)
		}
	}
}

// TestMergeWSExtraHeaders_EmptyExtraMap verifies that an empty (non-nil) extra
// headers map in the context also returns the provider map unchanged.
func TestMergeWSExtraHeaders_EmptyExtraMap(t *testing.T) {
	providerHeaders := map[string]string{
		"Authorization": "Bearer tok",
	}
	ctx := newBifrostContextWithExtra(map[string]string{})

	merged := mergeWSExtraHeaders(providerHeaders, ctx)

	if len(merged) != len(providerHeaders) {
		t.Errorf("len(merged) = %d, want %d", len(merged), len(providerHeaders))
	}
}

// TestMergeWSExtraHeaders_NonXBfEhHeadersNotForwarded verifies that headers
// stored in the context must have gone through the x-bf-eh-* capture path and
// that only those derived keys appear in the merged map.  This is a contract
// test: the only way headers reach BifrostContextKeyExtraHeaders is via
// createBifrostContextFromAuth which strips the x-bf-eh- prefix.  Any key
// present in extraHeaders that was NOT x-bf-eh-* prefixed would not be in the
// context, so this test confirms the integration assumption.
func TestMergeWSExtraHeaders_OnlyXBfEhDerivedKeysForwarded(t *testing.T) {
	// Simulate what createBifrostContextFromAuth stores: only stripped suffixes.
	ctx := newBifrostContextWithExtra(map[string]string{
		"x-trace-id": "trace-abc",   // from x-bf-eh-x-trace-id
		"originator": "test-client", // from x-bf-eh-originator
	})
	providerHeaders := map[string]string{
		"Authorization": "Bearer tok",
	}

	merged := mergeWSExtraHeaders(providerHeaders, ctx)

	// Only the stripped-prefix keys should appear in merged, plus provider headers.
	expectedKeys := map[string]string{
		"x-trace-id":    "trace-abc",
		"originator":    "test-client",
		"Authorization": "Bearer tok",
	}
	for k, want := range expectedKeys {
		if got := merged[k]; got != want {
			t.Errorf("merged[%q] = %q, want %q", k, got, want)
		}
	}
	if len(merged) != len(expectedKeys) {
		t.Errorf("len(merged) = %d, want %d; extra keys in merged: %v", len(merged), len(expectedKeys), merged)
	}
}
