package lib

import (
	"encoding/json"
	"testing"
)

func TestHumeConfigDefaults(t *testing.T) {
	config := &HumeConfig{}
	if err := config.CheckAndSetDefaults(); err != nil {
		t.Fatalf("CheckAndSetDefaults() error = %v", err)
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
	config := &HumeConfig{ProsodyPrompt: &HumeProsodyPromptConfig{
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

func TestHumeConfigValidation(t *testing.T) {
	negative := -1
	tests := []HumeConfig{
		{ProsodyPrompt: &HumeProsodyPromptConfig{Scope: "invalid"}},
		{ProsodyPrompt: &HumeProsodyPromptConfig{Scope: HumeProsodyPromptScopeLatestUser, MaxEmotions: &negative}},
	}
	for i := range tests {
		if err := tests[i].CheckAndSetDefaults(); err == nil {
			t.Fatalf("test %d: CheckAndSetDefaults() error = nil", i)
		}
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
