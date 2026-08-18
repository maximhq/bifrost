package lib

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHumeConfigDefaults(t *testing.T) {
	config := &HumeConfig{DefaultModel: " openai/gpt-4o-mini "}
	if err := config.CheckAndSetDefaults(); err != nil {
		t.Fatalf("CheckAndSetDefaults() error = %v", err)
	}
	if config.DefaultModel != "openai/gpt-4o-mini" {
		t.Fatalf("DefaultModel = %q, want %q", config.DefaultModel, "openai/gpt-4o-mini")
	}
	if config.ProsodyPrompt == nil {
		t.Fatal("ProsodyPrompt is nil")
	}
	if config.ProsodyPrompt.Enabled {
		t.Fatal("prosody prompt injection must default to disabled")
	}
	if config.ProsodyPrompt.Scope != HumeProsodyPromptScopeLatestUser {
		t.Fatalf("Scope = %q, want %q", config.ProsodyPrompt.Scope, HumeProsodyPromptScopeLatestUser)
	}
	if config.ProsodyPrompt.MaxEmotions == nil || *config.ProsodyPrompt.MaxEmotions != DefaultHumeMaxEmotions {
		t.Fatalf("MaxEmotions = %v, want %d", config.ProsodyPrompt.MaxEmotions, DefaultHumeMaxEmotions)
	}
}

func TestHumeConfigPreservesAllScoresSetting(t *testing.T) {
	zero := 0
	config := &HumeConfig{DefaultModel: "openai/gpt-4o-mini", ProsodyPrompt: &HumeProsodyPromptConfig{
		Enabled:     true,
		Scope:       HumeProsodyPromptScopeAllUserMessages,
		MaxEmotions: &zero,
	}}
	if err := config.CheckAndSetDefaults(); err != nil {
		t.Fatalf("CheckAndSetDefaults() error = %v", err)
	}
	if *config.ProsodyPrompt.MaxEmotions != 0 {
		t.Fatalf("MaxEmotions = %d, want 0", *config.ProsodyPrompt.MaxEmotions)
	}
}

func TestHumeConfigAllowsProsodyWithoutDefaultModel(t *testing.T) {
	config := &HumeConfig{ProsodyPrompt: &HumeProsodyPromptConfig{Enabled: true}}
	if err := config.CheckAndSetDefaults(); err != nil {
		t.Fatalf("CheckAndSetDefaults() error = %v", err)
	}
	if config.DefaultModel != "" {
		t.Fatalf("DefaultModel = %q, want empty", config.DefaultModel)
	}
	if config.ProsodyPrompt.Scope != HumeProsodyPromptScopeLatestUser {
		t.Fatalf("Scope = %q, want %q", config.ProsodyPrompt.Scope, HumeProsodyPromptScopeLatestUser)
	}
}

func TestHumeConfigValidation(t *testing.T) {
	negative := -1
	tests := []HumeConfig{
		{DefaultModel: "openai/gpt-4o-mini", ProsodyPrompt: &HumeProsodyPromptConfig{Scope: "invalid"}},
		{DefaultModel: "openai/gpt-4o-mini", ProsodyPrompt: &HumeProsodyPromptConfig{Scope: HumeProsodyPromptScopeLatestUser, MaxEmotions: &negative}},
	}
	for i := range tests {
		if err := tests[i].CheckAndSetDefaults(); err == nil {
			t.Fatalf("test %d: CheckAndSetDefaults() error = nil", i)
		}
	}
}

func TestConfigDataRejectsUnknownHumeProperties(t *testing.T) {
	tests := []struct {
		name         string
		configJSON   string
		unknownField string
	}{
		{
			name:         "top level",
			configJSON:   `{"hume":{"default_model":"openai/gpt-4o-mini","defualt_model":"openai/gpt-4o"}}`,
			unknownField: "defualt_model",
		},
		{
			name:         "prosody prompt",
			configJSON:   `{"hume":{"default_model":"openai/gpt-4o-mini","prosody_prompt":{"enabled":true,"max_emotion":3}}}`,
			unknownField: "max_emotion",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var config ConfigData
			err := json.Unmarshal([]byte(test.configJSON), &config)
			if err == nil {
				t.Fatalf("Unmarshal() error = nil, want unknown field %q", test.unknownField)
			}
			if !strings.Contains(err.Error(), `unknown field "`+test.unknownField+`"`) {
				t.Fatalf("Unmarshal() error = %q, want unknown field %q", err, test.unknownField)
			}
		})
	}
}

func TestConfigDataRejectsNullHumeObjects(t *testing.T) {
	tests := []struct {
		name       string
		configJSON string
		wantError  string
	}{
		{
			name:       "Hume section",
			configJSON: `{"hume":null}`,
			wantError:  "hume configuration must be an object, not null",
		},
		{
			name:       "prosody prompt",
			configJSON: `{"hume":{"default_model":"openai/gpt-4o-mini","prosody_prompt":null}}`,
			wantError:  "hume.prosody_prompt must be an object, not null",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var config ConfigData
			err := json.Unmarshal([]byte(test.configJSON), &config)
			if err == nil {
				t.Fatalf("Unmarshal() error = nil, want %q", test.wantError)
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Unmarshal() error = %q, want %q", err, test.wantError)
			}
		})
	}
}

func TestConfigDataUnmarshalsHumeConfig(t *testing.T) {
	var config ConfigData
	if err := json.Unmarshal([]byte(`{
		"hume": {
			"default_model": "openai/gpt-4o-mini",
			"prosody_prompt": {"enabled": true, "scope": "all_user_messages", "max_emotions": 0}
		}
	}`), &config); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if config.Hume == nil || config.Hume.ProsodyPrompt == nil {
		t.Fatal("Hume config was not unmarshaled")
	}
	if config.Hume.DefaultModel != "openai/gpt-4o-mini" || !config.Hume.ProsodyPrompt.Enabled {
		t.Fatalf("unexpected Hume config: %+v", config.Hume)
	}
	if config.Hume.ProsodyPrompt.MaxEmotions == nil || *config.Hume.ProsodyPrompt.MaxEmotions != 0 {
		t.Fatalf("MaxEmotions = %v, want 0", config.Hume.ProsodyPrompt.MaxEmotions)
	}
}
