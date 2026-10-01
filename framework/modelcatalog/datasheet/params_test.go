package datasheet

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
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
