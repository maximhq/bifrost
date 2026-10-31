package datasheet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestModelParameterCandidates(t *testing.T) {
	s := NewTestStore(map[string]string{
		"gpt-4o-2024-08-06": "gpt-4o",
	})
	s.mu.Lock()
	s.supportedParams = map[string][]string{
		"gpt-5.5":                          {"temperature"},
		"openrouter/moonshotai/kimi-k2.5":  {"temperature"},
		"openrouter/openai/gpt-5.5":        {"temperature"},
		"openrouter/moonshotai/kimi-k2.7":  {"temperature"},
		"openrouter/moonshotai2/kimi-k2.5": {"temperature"},
	}
	s.mu.Unlock()

	tests := []struct {
		name  string
		model string
		want  []string
	}{
		{
			name:  "bare model stays first",
			model: "gpt-5.5",
			want:  []string{"gpt-5.5", "openrouter/openai/gpt-5.5"},
		},
		{
			name:  "provider-qualified strips to bare",
			model: "openai/gpt-5.5",
			want:  []string{"openai/gpt-5.5", "gpt-5.5", "openrouter/openai/gpt-5.5"},
		},
		{
			name:  "double-qualified openrouter id strips progressively",
			model: "openrouter/openai/gpt-5.5",
			want:  []string{"openrouter/openai/gpt-5.5", "openai/gpt-5.5", "gpt-5.5"},
		},
		{
			name:  "bare alias finds qualified datasheet keys sorted",
			model: "kimi-k2.5",
			want:  []string{"kimi-k2.5", "openrouter/moonshotai/kimi-k2.5", "openrouter/moonshotai2/kimi-k2.5"},
		},
		{
			name:  "dated model includes canonical base name",
			model: "gpt-4o-2024-08-06",
			want:  []string{"gpt-4o-2024-08-06", "gpt-4o"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.modelParameterCandidates(tt.model)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("modelParameterCandidates(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// A model-parameters sync must invalidate the capability cache, and must keep
// doing so after UpdateSyncConfig runs — operators change the sheet URL or
// interval from the config API at runtime and must not have to restart for
// sheet edits to take effect.
func TestApplyModelParameters_FiresAppliedHook(t *testing.T) {
	usable := map[string]json.RawMessage{
		"gpt-4o": json.RawMessage(`{"supports_reasoning":true}`),
	}

	t.Run("fires on a usable record", func(t *testing.T) {
		s := &Store{}
		fired := 0
		s.SetOnModelParametersApplied(func() { fired++ })
		if applied := s.applyModelParameters(usable); applied != 1 {
			t.Fatalf("applied = %d, want 1", applied)
		}
		if fired != 1 {
			t.Fatalf("applied hook fired %d times, want 1", fired)
		}
	})

	t.Run("survives a runtime sync-config update", func(t *testing.T) {
		s := &Store{}
		fired := 0
		s.SetOnModelParametersApplied(func() { fired++ })
		s.UpdateSyncConfig(Config{
			URL:                "file:///pricing.json",
			ModelParametersURL: "file:///params.json",
			SyncInterval:       time.Hour,
		})
		if applied := s.applyModelParameters(usable); applied != 1 {
			t.Fatalf("applied = %d, want 1", applied)
		}
		if fired != 1 {
			t.Fatalf("applied hook fired %d times after UpdateSyncConfig, want 1", fired)
		}
	})

	// A feed with nothing usable must leave the derived indexes alone too, or
	// the deployment is split: an empty parameter allowlist alongside capability
	// records that still describe the previous sheet.
	t.Run("leaves the derived indexes untouched on an empty feed", func(t *testing.T) {
		s := &Store{}
		if applied := s.applyModelParameters(usable); applied != 1 {
			t.Fatalf("seed applied = %d, want 1", applied)
		}
		s.mu.RLock()
		seeded := len(s.supportedParams)
		s.mu.RUnlock()

		if applied := s.applyModelParameters(map[string]json.RawMessage{
			"a": json.RawMessage(`{}`),
		}); applied != 0 {
			t.Fatalf("applied = %d, want 0", applied)
		}
		s.mu.RLock()
		after := len(s.supportedParams)
		s.mu.RUnlock()
		if after != seeded {
			t.Errorf("supportedParams size = %d after an empty feed, want %d (unchanged)", after, seeded)
		}
	})

	// The flush is what refills the cache, so a feed carrying nothing usable
	// must not drop records it cannot replace.
	t.Run("does not fire when every record is empty", func(t *testing.T) {
		s := &Store{}
		fired := 0
		s.SetOnModelParametersApplied(func() { fired++ })
		applied := s.applyModelParameters(map[string]json.RawMessage{
			"a": json.RawMessage(`{}`),
			"b": json.RawMessage(`{"provider":"openai"}`),
		})
		if applied != 0 {
			t.Fatalf("applied = %d, want 0 for empty records", applied)
		}
		if fired != 0 {
			t.Fatalf("applied hook fired %d times on an empty feed, want 0", fired)
		}
	})
}

// paramsOnlyConfigStore answers model-parameter reads and nothing else. The
// embedded interface is nil, so any other call panics rather than passing.
type paramsOnlyConfigStore struct {
	configstore.ConfigStore
	rows map[string]string // model key -> raw JSON
}

func (s paramsOnlyConfigStore) GetModelParametersByModel(_ context.Context, model string) (*configstoreTables.TableModelParameters, error) {
	data, ok := s.rows[model]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return &configstoreTables.TableModelParameters{Model: model, Data: data}, nil
}

// normalizeProvider folds every "*bedrock*" row provider onto "bedrock", so a
// bedrock_mantle lookup matched nothing and every capability check on that
// provider silently fell back — routing /v1/responses natively for models the
// sheet only lists on /v1/chat/completions.
func TestLoadModelCapabilities_MatchesBedrockMantleRows(t *testing.T) {
	const model = "openai.gpt-oss-safeguard-20b"
	rows := map[string]string{
		model:                     `{"provider":"bedrock","mode":"chat"}`,
		"bedrock_mantle/" + model: `{"provider":"bedrock_mantle","mode":"chat","supported_endpoints":["/v1/chat/completions"]}`,
	}
	s := NewTestStore(nil)
	s.configStore = paramsOnlyConfigStore{rows: rows}
	s.SetSupportedParamsForTest(map[string][]string{"bedrock_mantle/" + model: {"temperature"}})

	caps, err := s.LoadModelCapabilities(context.Background(), schemas.BedrockMantle, model)
	if err != nil {
		t.Fatalf("LoadModelCapabilities: %v", err)
	}
	if caps == nil {
		t.Fatal("no capabilities for a bedrock_mantle model the sheet has a row for")
	}
	if !slices.Equal(caps.SupportedEndpoints, []string{"/v1/chat/completions"}) {
		t.Fatalf("SupportedEndpoints = %v, want the bedrock_mantle row's", caps.SupportedEndpoints)
	}
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return caps })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
	if schemas.ResolveModelCaps(schemas.BedrockMantle, model).SupportsResponsesEndpoint(true) {
		t.Error("responses reported as supported for a chat-completions-only model")
	}

	// The plain bedrock row still resolves for a bedrock lookup.
	bedrockCaps, err := s.LoadModelCapabilities(context.Background(), schemas.Bedrock, model)
	if err != nil {
		t.Fatalf("LoadModelCapabilities(bedrock): %v", err)
	}
	if bedrockCaps == nil || len(bedrockCaps.SupportedEndpoints) != 0 {
		t.Errorf("bedrock lookup resolved to %+v, want the plain bedrock row", bedrockCaps)
	}
}

// server_side_model rides the model-parameters row (no pricing column, no
// migration), so it must survive the row -> capability record decode for the
// provider's runtime lookup.
func TestLoadModelCapabilities_CarriesServerSideModel(t *testing.T) {
	s := NewTestStore(nil)
	s.configStore = paramsOnlyConfigStore{rows: map[string]string{
		"deepseek/deepseek-v4-flash": `{"provider":"deepseek","mode":"chat","base_model":"deepseek-v4-flash","server_side_model":"deepseek-flash"}`,
	}}
	s.SetSupportedParamsForTest(map[string][]string{"deepseek/deepseek-v4-flash": {"temperature"}})

	caps, err := s.LoadModelCapabilities(context.Background(), schemas.DeepSeek, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("LoadModelCapabilities: %v", err)
	}
	if caps == nil || caps.ServerSideModel == nil || *caps.ServerSideModel != "deepseek-flash" {
		t.Fatalf("ServerSideModel = %+v, want deepseek-flash", caps)
	}
}

// A URL sync must retain provider-qualified rows that exist only in the DB.
func TestSyncModelParamsFromURL_PreservesDBOnlyRows(t *testing.T) {
	ctx := t.Context()
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(context.Background()) })

	const model = "custom-params-model"
	const customKey = "custom-anthropic/" + model
	const customData = `{"provider":"custom-anthropic","max_output_tokens":32000}`
	const emptyKey = "custom-anthropic/empty-feed-model"
	const dbOnlyKey = "custom-anthropic/db-only-model"
	if err := cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: customKey, Data: customData},
		{Model: emptyKey, Data: customData},
		{Model: dbOnlyKey, Data: customData},
	}); err != nil {
		t.Fatal(err)
	}

	feedPath := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(feedPath, []byte(`{
		"custom-params-model":{"provider":"anthropic","max_output_tokens":16000},
		"custom-anthropic/custom-params-model":{"max_output_tokens":"invalid"},
		"custom-anthropic/empty-feed-model":{"supported_endpoints":[]}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(cs, nil, Config{ModelParametersURL: "file://" + feedPath})
	if _, err := s.LoadModelParamsFromDB(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncModelParamsFromURL(ctx); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{model, "empty-feed-model", "db-only-model"} {
		caps, err := s.LoadModelCapabilities(ctx, "custom-anthropic", name)
		if err != nil || caps == nil || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != 32000 {
			t.Fatalf("stored capabilities for %s after sync = %+v, err = %v; want max_output_tokens 32000", name, caps, err)
		}
	}
	row, err := cs.GetModelParametersByModel(ctx, customKey)
	if err != nil || row == nil || row.Data != customData {
		t.Fatalf("DB-only row after sync = %+v, err = %v", row, err)
	}
	emptyRow, err := cs.GetModelParametersByModel(ctx, emptyKey)
	if err != nil || emptyRow == nil || emptyRow.Data != customData {
		t.Fatalf("row shadowed by empty feed data = %+v, err = %v", emptyRow, err)
	}
}

// TestSyncModelParamsFromURL_ProviderCollisions checks both persistence and lookup
// after sync and reload, including legitimate updates beside rejected records.
func TestSyncModelParamsFromURL_ProviderCollisions(t *testing.T) {
	ctx := t.Context()
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(context.Background()) })

	const stored = `{"provider":"custom-anthropic","max_output_tokens":32000,"supported_endpoints":["/v1/chat/completions"]}`
	const providerless = `{"max_output_tokens":16000,"supported_endpoints":["/v1/responses"]}`
	const explicitValues = `{"provider":"custom-anthropic","max_output_tokens":0,"supports_reasoning":false,"synthetic_structured_output_tool_prefix":"","supported_endpoints":["/v1/responses"]}`
	const replacement = `{"provider":"other-provider","max_output_tokens":16000,"supported_endpoints":["/v1/responses"]}`
	for i, tc := range []struct {
		name, existing, incoming, want string
	}{
		{"missing provider", stored, providerless, stored},
		{"missing provider with explicit zero", stored, `{"max_output_tokens":0}`, stored},
		{"empty provider", stored, `{"provider":"","max_output_tokens":0}`, stored},
		{"null provider", stored, `{"provider":null,"max_output_tokens":0}`, stored},
		{"blank provider", stored, `{"provider":" \t","max_output_tokens":0}`, stored},
		{"numeric provider", stored, `{"provider":123,"max_output_tokens":0}`, stored},
		{"boolean provider", stored, `{"provider":false,"max_output_tokens":0}`, stored},
		{"array provider", stored, `{"provider":["custom-anthropic"],"max_output_tokens":0}`, stored},
		{"object provider", stored, `{"provider":{"name":"custom-anthropic"},"max_output_tokens":0}`, stored},
		{"null record", stored, `null`, stored},
		{"malformed capability", stored, `{"provider":"custom-anthropic","max_output_tokens":"bad"}`, stored},
		{"empty nested capabilities", stored, `{"provider":"custom-anthropic","reasoning_budget":{},"model_parameters":[{}]}`, stored},
		{"explicit values with provider", stored, explicitValues, explicitValues},
		{"explicit provider change", stored, replacement, replacement},
		{"new providerless record", "", providerless, providerless},
		{"providerless update", `{"max_output_tokens":32000}`, providerless, providerless},
		{"provider added", providerless, stored, stored},
		{"new provider record", "", stored, stored},
		{"empty stored provider", `{"provider":"","max_output_tokens":32000}`, providerless, providerless},
		{"null stored provider", `{"provider":null,"max_output_tokens":32000}`, providerless, providerless},
		{"blank stored provider", `{"provider":" ","max_output_tokens":32000}`, providerless, providerless},
		{"malformed stored provider", `{"provider":123,"max_output_tokens":32000}`, providerless, providerless},
	} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t", tc.name, mixed), func(t *testing.T) {
				model := fmt.Sprintf("collision-model-%d-%t", i, mixed)
				key := "custom-anthropic/" + model
				if tc.existing != "" {
					require.NoError(t, cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{{Model: key, Data: tc.existing}}))
				}
				path := filepath.Join(t.TempDir(), "params.json")
				// A healthy sibling must still be applied when the colliding row is rejected.
				siblingKey := "healthy-" + model
				feed := fmt.Sprintf(`{%q:%s}`, key, tc.incoming)
				if mixed {
					feed = fmt.Sprintf(`{%q:%s,%q:%s}`, key, tc.incoming, siblingKey, providerless)
				}
				require.NoError(t, os.WriteFile(path, []byte(feed), 0o600))
				s := New(cs, nil, Config{ModelParametersURL: "file://" + path})
				_, err := s.LoadModelParamsFromDB(ctx)
				require.NoError(t, err)
				require.NoError(t, s.SyncModelParamsFromURL(ctx))
				row, err := cs.GetModelParametersByModel(ctx, key)
				require.NoError(t, err)
				require.JSONEq(t, tc.want, row.Data)
				if mixed {
					sibling, err := cs.GetModelParametersByModel(ctx, siblingKey)
					require.NoError(t, err)
					require.JSONEq(t, providerless, sibling.Data)
				}

				var want struct {
					Provider string
					schemas.ModelCapabilities
				}
				require.NoError(t, json.Unmarshal([]byte(tc.want), &want))
				for _, reload := range []bool{false, true} {
					if reload {
						s = New(cs, nil, Config{})
						_, err := s.LoadModelParamsFromDB(ctx)
						require.NoError(t, err)
					}
					caps, err := s.LoadModelCapabilities(ctx, schemas.ModelProvider(want.Provider), model)
					require.NoError(t, err)
					if want.Provider == "" {
						require.Nil(t, caps)
					} else {
						require.Equal(t, &want.ModelCapabilities, caps)
					}
					require.Equal(t, slices.Contains(want.SupportedEndpoints, "/v1/chat/completions"), s.IsRequestTypeSupported(key, schemas.ChatCompletionRequest))
					require.Equal(t, slices.Contains(want.SupportedEndpoints, "/v1/responses"), s.IsRequestTypeSupported(key, schemas.ResponsesRequest))
					require.Contains(t, s.GetSupportedParameters(key), "max_tokens")
				}
			})
		}
	}
}
