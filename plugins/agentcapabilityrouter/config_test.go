package agentcapabilityrouter

import "testing"

func TestResolveConfigDefaultsAreSafe(t *testing.T) {
	cfg, err := resolveConfig(nil)
	if err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}
	if !cfg.ShadowMode {
		t.Fatal("shadow mode must default to true")
	}
	if !cfg.ActiveRoles[roleMain] || !cfg.ActiveRoles[roleWorker] {
		t.Fatalf("active roles = %#v, want main and worker enabled", cfg.ActiveRoles)
	}
	if cfg.Aliases.Main != "agent-main-auto" || cfg.Aliases.Worker != "agent-worker-auto" {
		t.Fatalf("aliases = %#v", cfg.Aliases)
	}
}

func TestResolveConfigMergesPartialOverrides(t *testing.T) {
	shadow := false
	worker := false
	threshold := 0.8
	cfg, err := resolveConfig(&Config{
		ShadowMode:          &shadow,
		ConfidenceThreshold: &threshold,
		Aliases:             &AliasConfig{Main: "main-dynamic"},
		ActiveRoles:         &ActiveRolesConfig{Worker: &worker},
		Keywords:            map[string][]string{CapabilityExplore: {"inventory"}},
	})
	if err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}
	if cfg.ShadowMode {
		t.Fatal("shadow mode override was not applied")
	}
	if cfg.Aliases.Main != "main-dynamic" || cfg.Aliases.Worker != "agent-worker-auto" {
		t.Fatalf("aliases = %#v", cfg.Aliases)
	}
	if !cfg.ActiveRoles[roleMain] || cfg.ActiveRoles[roleWorker] {
		t.Fatalf("active roles = %#v", cfg.ActiveRoles)
	}
	if got := cfg.Keywords[CapabilityExplore]; len(got) != 1 || got[0] != "inventory" {
		t.Fatalf("explore keywords = %#v", got)
	}
	if len(cfg.Keywords[CapabilityDebug]) == 0 {
		t.Fatal("unmodified keyword groups must retain defaults")
	}
}

func TestResolveConfigRejectsInvalidValues(t *testing.T) {
	zero := 0.0
	tooMany := 33
	tests := []struct {
		name  string
		input *Config
	}{
		{"zero confidence", &Config{ConfidenceThreshold: &zero}},
		{"too much history", &Config{HistoryMessages: &tooMany}},
		{"duplicate aliases", &Config{Aliases: &AliasConfig{Main: "same", Worker: "same"}}},
		{"unknown keyword group", &Config{Keywords: map[string][]string{"other": {"value"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveConfig(test.input); err == nil {
				t.Fatal("resolveConfig() error = nil, want error")
			}
		})
	}
}

func TestResolvedConfigDoesNotAliasCallerKeywordSlices(t *testing.T) {
	keywords := []string{"inventory"}
	cfg, err := resolveConfig(&Config{Keywords: map[string][]string{CapabilityExplore: keywords}})
	if err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}
	keywords[0] = "changed"
	if cfg.Keywords[CapabilityExplore][0] != "inventory" {
		t.Fatal("resolved config retained caller-owned keyword slice")
	}
}
