package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.GatewayURL != "http://localhost:8080" {
		t.Errorf("default gateway URL = %s, want http://localhost:8080", cfg.GatewayURL)
	}
	if len(cfg.Domains) == 0 {
		t.Error("default config should have domain rules")
	}

	// Check key domains are present
	domains := make(map[string]bool)
	for _, d := range cfg.Domains {
		domains[d.Hostname] = true
	}
	for _, expected := range []string{"api.openai.com", "chatgpt.com", "ab.chatgpt.com", "generativelanguage.googleapis.com", "api.mistral.ai"} {
		if !domains[expected] {
			t.Errorf("default domains missing %s", expected)
		}
	}
}

func TestNewRuntimeConfig(t *testing.T) {
	cfg := DefaultConfig()
	rc, err := NewRuntimeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Check domain map
	if !rc.IsInterceptedDomain("api.openai.com") {
		t.Error("api.openai.com should be intercepted")
	}
	if rc.IsInterceptedDomain("example.com") {
		t.Error("example.com should not be intercepted")
	}

	// All domains should be in the map
	for _, d := range cfg.Domains {
		if !rc.IsInterceptedDomain(d.Hostname) {
			t.Errorf("%s should be intercepted", d.Hostname)
		}
	}
}

func TestRuntimeConfig_GetDomainRule(t *testing.T) {
	cfg := DefaultConfig()
	rc, _ := NewRuntimeConfig(cfg)

	rule := rc.GetDomainRule("api.openai.com")
	if rule == nil {
		t.Fatal("expected rule for api.openai.com")
	}
	if rule.IntegrationPrefix != "/openai" {
		t.Errorf("integration prefix = %s, want /openai", rule.IntegrationPrefix)
	}

	rule = rc.GetDomainRule("api.mistral.ai")
	if rule == nil {
		t.Fatal("expected rule for api.mistral.ai")
	}
	if rule.IntegrationPrefix != "/openai" {
		t.Errorf("integration prefix = %s, want /openai", rule.IntegrationPrefix)
	}

	rule = rc.GetDomainRule("ab.chatgpt.com")
	if rule == nil {
		t.Fatal("expected rule for ab.chatgpt.com")
	}
	if rule.IntegrationPrefix != "/chatgpt" {
		t.Errorf("integration prefix = %s, want /chatgpt", rule.IntegrationPrefix)
	}
	if !rule.ShouldProxyPath("/backend-api/f/conversation") {
		t.Error("expected /backend-api/f/conversation to be proxied")
	}
	if rule.ShouldProxyPath("/backend-api/conversation") {
		t.Error("expected /backend-api/conversation to be direct-relayed")
	}
	if rule.ShouldProxyPath("/backend-api/models") {
		t.Error("expected /backend-api/models to be direct-relayed")
	}
	if rule.ShouldProxyPath("/") {
		t.Error("expected / to be direct-relayed")
	}

	rule = rc.GetDomainRule("nonexistent.com")
	if rule != nil {
		t.Error("expected nil for nonexistent domain")
	}
}

func TestRuntimeConfig_UpdateDomains(t *testing.T) {
	cfg := DefaultConfig()
	rc, _ := NewRuntimeConfig(cfg)

	// Add a new domain and remove one
	newDomains := []DomainRule{
		{Hostname: "api.openai.com", IntegrationPrefix: "/openai", PreservePath: true},
		{Hostname: "new-ai.example.com", IntegrationPrefix: "/openai", PreservePath: true},
	}

	added, removed := rc.UpdateDomains(newDomains)

	// new-ai.example.com should be added
	foundNew := false
	for _, h := range added {
		if h == "new-ai.example.com" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Error("expected new-ai.example.com in added list")
	}

	// api.mistral.ai (and others) should be removed
	foundRemoved := false
	for _, h := range removed {
		if h == "api.mistral.ai" {
			foundRemoved = true
		}
	}
	if !foundRemoved {
		t.Error("expected api.mistral.ai in removed list")
	}

	// Domain map should reflect new state
	if !rc.IsInterceptedDomain("new-ai.example.com") {
		t.Error("new-ai.example.com should be intercepted after update")
	}
	if rc.IsInterceptedDomain("api.mistral.ai") {
		t.Error("api.mistral.ai should not be intercepted after update")
	}
	if !rc.IsInterceptedDomain("api.openai.com") {
		t.Error("api.openai.com should still be intercepted")
	}
}

func TestStore_SaveLoad(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "bifrost-agent-test")
	os.MkdirAll(dir, 0700)
	defer os.RemoveAll(dir)

	store := NewStore(dir)

	cfg := DefaultConfig()
	cfg.GatewayURL = "https://test-gateway.example.com"
	cfg.VirtualKey = "vk-test-12345"

	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("loaded config is nil")
	}

	if loaded.GatewayURL != cfg.GatewayURL {
		t.Errorf("gateway URL = %s, want %s", loaded.GatewayURL, cfg.GatewayURL)
	}
	if loaded.VirtualKey != cfg.VirtualKey {
		t.Errorf("virtual key = %s, want %s", loaded.VirtualKey, cfg.VirtualKey)
	}
	if len(loaded.Domains) != len(cfg.Domains) {
		t.Errorf("domains count = %d, want %d", len(loaded.Domains), len(cfg.Domains))
	}
}

func TestStore_LoadNonExistent(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "bifrost-agent-test-noexist")
	os.RemoveAll(dir)

	store := NewStore(dir)
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Error("expected nil config for non-existent store")
	}
}

func TestDuration_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`"60s"`, "1m0s"},
		{`"1m30s"`, "1m30s"},
		{`"5m"`, "5m0s"},
	}

	for _, tt := range tests {
		var d Duration
		if err := d.UnmarshalJSON([]byte(tt.input)); err != nil {
			t.Errorf("UnmarshalJSON(%s) error: %v", tt.input, err)
			continue
		}
		if d.Duration.String() != tt.expected {
			t.Errorf("UnmarshalJSON(%s) = %s, want %s", tt.input, d.Duration, tt.expected)
		}
	}
}
