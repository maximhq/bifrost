package datasheet

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
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
