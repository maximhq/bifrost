package modelcatalog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

// TestUpsertLiveFromResponse_NilRespIsNoop guards the API surface: handing a
// nil response into UpsertLiveFromResponse must not clear an existing cache
// entry by storing an empty slice. Without the early-return, extractModelIDs
// would return nil and the live store would publish an empty model list,
// silently removing the provider's previously-fetched models from routing.
func TestUpsertLiveFromResponse_NilRespIsNoop(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.UpsertLive(schemas.OpenAI, "k1", false, []string{"gpt-4o", "o1"})

	mc.UpsertLiveFromResponse(schemas.OpenAI, "k1", false, nil)

	got := mc.GetModelsForProvider(schemas.OpenAI)
	slices.Sort(got)
	want := []string{"gpt-4o", "o1"}
	if !slices.Equal(got, want) {
		t.Errorf("after UpsertLiveFromResponse(nil), GetModelsForProvider = %v, want %v (entry must survive nil resp)", got, want)
	}
}

// TestUpsertLiveFromResponse_PopulatesFromResponse covers the happy path:
// extractModelIDs strips the owning provider prefix and the resulting bare
// names land in the live cache for GetModelsForProvider.
func TestUpsertLiveFromResponse_PopulatesFromResponse(t *testing.T) {
	mc := NewTestCatalog(nil)
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "openai/gpt-4o"},
			{ID: "openai/o1"},
		},
	}
	mc.UpsertLiveFromResponse(schemas.OpenAI, "k1", false, resp)

	got := mc.GetModelsForProvider(schemas.OpenAI)
	slices.Sort(got)
	want := []string{"gpt-4o", "o1"}
	if !slices.Equal(got, want) {
		t.Errorf("GetModelsForProvider = %v, want %v", got, want)
	}
}

func TestGetModelsForProvider_IncludesDeprecatedDatasheetModelsWhenLiveExists(t *testing.T) {
	pricingPath := filepath.Join(t.TempDir(), "pricing.json")
	pricingJSON := []byte(`{
		"deprecated-model": {"provider":"openai","mode":"chat","base_model":"deprecated-model","is_deprecated":true},
		"current-model": {"provider":"openai","mode":"chat","base_model":"current-model"}
	}`)
	if err := os.WriteFile(pricingPath, pricingJSON, 0o600); err != nil {
		t.Fatalf("write pricing testdata: %v", err)
	}

	ds := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + pricingPath})
	if err := ds.LoadFromURLIntoMemory(t.Context()); err != nil {
		t.Fatalf("load pricing testdata: %v", err)
	}
	mc := NewTestCatalogWithDatasheet(ds)
	mc.UpsertLive(schemas.OpenAI, "k1", false, []string{"live-model"})

	got := mc.GetModelsForProvider(schemas.OpenAI)
	slices.Sort(got)
	want := []string{"deprecated-model", "live-model"}
	if !slices.Equal(got, want) {
		t.Errorf("GetModelsForProvider = %v, want %v", got, want)
	}
}

// deprecatedDatasheet returns a datasheet.Store loaded from a temp pricing
// file containing one deprecated model and one current model, both openai.
func deprecatedDatasheet(t *testing.T) *datasheet.Store {
	t.Helper()
	pricingPath := filepath.Join(t.TempDir(), "pricing.json")
	pricingJSON := []byte(`{
		"deprecated-model": {"provider":"openai","mode":"chat","base_model":"deprecated-model","is_deprecated":true},
		"current-model": {"provider":"openai","mode":"chat","base_model":"current-model"}
	}`)
	if err := os.WriteFile(pricingPath, pricingJSON, 0o600); err != nil {
		t.Fatalf("write pricing testdata: %v", err)
	}
	ds := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + pricingPath})
	if err := ds.LoadFromURLIntoMemory(t.Context()); err != nil {
		t.Fatalf("load pricing testdata: %v", err)
	}
	return ds
}

// TestGetModelsForProvider_DeprecatedDatasheetModelsRespectAllowBlock pins the
// allow/block gating that appendAllowedDatasheetModels applies to deprecated
// datasheet models once a live entry exists. The unrestricted path is covered
// by TestGetModelsForProvider_IncludesDeprecatedDatasheetModelsWhenLiveExists;
// these sub-tests cover the keyconfig-restricted branches.
func TestGetModelsForProvider_DeprecatedDatasheetModelsRespectAllowBlock(t *testing.T) {
	tests := []struct {
		name string
		keys []schemas.Key
		want []string
	}{
		{
			// deprecated-model sits in the keyconfig blocklist → excluded even
			// though the key otherwise allows everything (wildcard).
			name: "blocklisted deprecated model is excluded",
			keys: []schemas.Key{
				{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"deprecated-model"}},
			},
			want: []string{"live-model"},
		},
		{
			// explicit allowlist omits deprecated-model → excluded.
			name: "deprecated model absent from allowlist is excluded",
			keys: []schemas.Key{
				{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"live-model"}},
			},
			want: []string{"live-model"},
		},
		{
			// explicit allowlist names deprecated-model → included.
			name: "deprecated model present in allowlist is included",
			keys: []schemas.Key{
				{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"live-model", "deprecated-model"}},
			},
			want: []string{"deprecated-model", "live-model"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kc := keyconfig.New(nil)
			kc.Replace(map[schemas.ModelProvider][]schemas.Key{schemas.OpenAI: tt.keys})
			mc := &ModelCatalog{
				datasheet: deprecatedDatasheet(t),
				live:      live.New(nil),
				keyconf:   kc,
				done:      make(chan struct{}),
			}
			mc.UpsertLive(schemas.OpenAI, "k1", false, []string{"live-model"})

			got := mc.GetModelsForProvider(schemas.OpenAI)
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("GetModelsForProvider = %v, want %v", got, tt.want)
			}
		})
	}
}

// ptrBool returns a pointer to b, for building schemas.Key fixtures.
func ptrBool(b bool) *bool { return &b }

// TestExtractModelIDs_StripsOwningProviderPrefix verifies the canonical
// shape returned by every provider's ListModels — an ID prefixed with its
// own provider key — gets reduced to a bare model name.
func TestExtractModelIDs_StripsOwningProviderPrefix(t *testing.T) {
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "openai/gpt-4o"},
			{ID: "openai/o1"},
		},
	}
	got := extractModelIDs(resp, schemas.OpenAI)
	slices.Sort(got)
	want := []string{"gpt-4o", "o1"}
	if !slices.Equal(got, want) {
		t.Errorf("extractModelIDs = %v, want %v", got, want)
	}
}

// TestExtractModelIDs_KeepsNestedProviderForGateway covers the
// gateway-provider shape (OpenRouter returns IDs like "openrouter/openai/gpt-4")
// — ParseModelString splits on the first slash, so the parsed prefix matches
// the owning provider and the remainder ("openai/gpt-4") is kept as-is.
func TestExtractModelIDs_KeepsNestedProviderForGateway(t *testing.T) {
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "openrouter/openai/gpt-4"},
			{ID: "openrouter/anthropic/claude-sonnet-4"},
		},
	}
	got := extractModelIDs(resp, schemas.OpenRouter)
	slices.Sort(got)
	want := []string{"anthropic/claude-sonnet-4", "openai/gpt-4"}
	if !slices.Equal(got, want) {
		t.Errorf("extractModelIDs = %v, want %v", got, want)
	}
}

// TestExtractModelIDs_DropsForeignPrefix asserts the defensive filter: an
// ID prefixed with a different provider than the one being upserted is
// excluded. This shouldn't fire in practice (providers self-prefix their
// own list-models output before it reaches here), but the guard exists for
// malformed inputs and the test pins the behavior so refactors don't
// silently invert it.
func TestExtractModelIDs_DropsForeignPrefix(t *testing.T) {
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "openai/gpt-4o"},
			{ID: "anthropic/claude-sonnet"}, // foreign — should be dropped
		},
	}
	got := extractModelIDs(resp, schemas.OpenAI)
	slices.Sort(got)
	want := []string{"gpt-4o"}
	if !slices.Equal(got, want) {
		t.Errorf("extractModelIDs = %v, want %v (anthropic-prefixed entry must be dropped when caller asks for openai)", got, want)
	}
}

// TestExtractModelIDs_NilResp returns nil — the public wrapper relies on
// this to short-circuit cleanly when a list-models call returns no body.
func TestExtractModelIDs_NilResp(t *testing.T) {
	if got := extractModelIDs(nil, schemas.OpenAI); got != nil {
		t.Errorf("extractModelIDs(nil) = %v, want nil", got)
	}
}

// TestExtractModelIDs_Dedup keeps only one entry when the same bare model
// name appears twice in the response (one prefixed, one bare).
func TestExtractModelIDs_Dedup(t *testing.T) {
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "openai/gpt-4o"},
			{ID: "gpt-4o"},
			{ID: "openai/gpt-4o"},
		},
	}
	got := extractModelIDs(resp, schemas.OpenAI)
	if len(got) != 1 || got[0] != "gpt-4o" {
		t.Errorf("extractModelIDs = %v, want [gpt-4o] (deduped)", got)
	}
}

// TestInvalidateLive_DropsBothFiltersForKey verifies the InvalidateLive
// forwarder reaches the live store and clears filtered + unfiltered entries
// for one (provider, keyID) pair in a single call.
func TestInvalidateLive_DropsBothFiltersForKey(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.UpsertLive(schemas.OpenAI, "k1", false, []string{"gpt-4o"})
	mc.UpsertLive(schemas.OpenAI, "k1", true, []string{"gpt-4o", "o1"})
	mc.UpsertLive(schemas.OpenAI, "k2", false, []string{"o1"})

	mc.InvalidateLive(schemas.OpenAI, "k1")

	// k1 entries are gone; k2 survives.
	if got := mc.GetModelsForProvider(schemas.OpenAI); !slices.Equal(got, []string{"o1"}) {
		t.Errorf("filtered union after InvalidateLive(k1) = %v, want [o1] (k1 filtered dropped, k2 survives)", got)
	}
	if got := mc.GetUnfilteredModelsForProvider(schemas.OpenAI); len(got) != 0 {
		t.Errorf("unfiltered union after InvalidateLive(k1) = %v, want [] (k1 unfiltered dropped; k2 has no unfiltered entry)", got)
	}
}

// TestInvalidateLiveProvider_DropsAcrossKeys verifies the provider-wide
// forwarder clears every (keyID, mode) combination for the provider.
func TestInvalidateLiveProvider_DropsAcrossKeys(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.UpsertLive(schemas.OpenAI, "k1", false, []string{"gpt-4o"})
	mc.UpsertLive(schemas.OpenAI, "k2", false, []string{"o1"})
	mc.UpsertLive(schemas.Anthropic, "k1", false, []string{"claude-sonnet"})

	mc.InvalidateLiveProvider(schemas.OpenAI)

	if got := mc.GetModelsForProvider(schemas.OpenAI); len(got) != 0 {
		t.Errorf("OpenAI after InvalidateLiveProvider = %v, want [] (every key dropped)", got)
	}
	// Other providers untouched.
	if got := mc.GetModelsForProvider(schemas.Anthropic); !slices.Equal(got, []string{"claude-sonnet"}) {
		t.Errorf("Anthropic after InvalidateLiveProvider(OpenAI) = %v, want [claude-sonnet]", got)
	}
}
