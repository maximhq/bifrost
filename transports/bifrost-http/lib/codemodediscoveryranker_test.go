package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestApplyCodeModeDiscoveryRanker(t *testing.T) {
	SetLogger(&testLogger{})
	fileWith := func(ranker *schemas.MCPCodeModeDiscoveryRanker) *schemas.MCPConfig {
		return &schemas.MCPConfig{ToolManagerConfig: &schemas.MCPToolManagerConfig{CodeModeDiscoveryRanker: ranker}}
	}

	t.Run("unset leaves the tool manager config alone", func(t *testing.T) {
		t.Setenv(envCodeModeDiscoveryRankerURL, "")
		t.Setenv(envCodeModeDiscoveryRankerTokenFile, "")
		runtime := &schemas.MCPConfig{}
		applyCodeModeDiscoveryRanker(runtime, nil)
		if runtime.ToolManagerConfig != nil {
			t.Fatalf("expected no tool manager config, got %+v", runtime.ToolManagerConfig)
		}
	})

	t.Run("file value reaches the store-backed runtime config", func(t *testing.T) {
		t.Setenv(envCodeModeDiscoveryRankerURL, "")
		t.Setenv(envCodeModeDiscoveryRankerTokenFile, "")
		runtime := &schemas.MCPConfig{ToolManagerConfig: &schemas.MCPToolManagerConfig{MaxAgentDepth: 3}}
		applyCodeModeDiscoveryRanker(runtime, fileWith(&schemas.MCPCodeModeDiscoveryRanker{URL: "https://ranker.example/rank", TimeoutSeconds: 2}))
		got := runtime.ToolManagerConfig.CodeModeDiscoveryRanker
		if got == nil || got.URL != "https://ranker.example/rank" || got.TimeoutSeconds != 2 {
			t.Fatalf("unexpected ranker %+v", got)
		}
		if runtime.ToolManagerConfig.MaxAgentDepth != 3 {
			t.Fatalf("other tool manager settings changed: %+v", runtime.ToolManagerConfig)
		}
	})

	t.Run("environment overrides the file", func(t *testing.T) {
		t.Setenv(envCodeModeDiscoveryRankerURL, " http://127.0.0.1:9000/rank ")
		t.Setenv(envCodeModeDiscoveryRankerTokenFile, "/run/secrets/ranker")
		runtime := &schemas.MCPConfig{}
		applyCodeModeDiscoveryRanker(runtime, fileWith(&schemas.MCPCodeModeDiscoveryRanker{URL: "https://ranker.example/rank", TimeoutSeconds: 4}))
		got := runtime.ToolManagerConfig.CodeModeDiscoveryRanker
		if got == nil || got.URL != "http://127.0.0.1:9000/rank" || got.TokenFile != "/run/secrets/ranker" || got.TimeoutSeconds != 4 {
			t.Fatalf("unexpected ranker %+v", got)
		}
	})

	t.Run("invalid settings are dropped", func(t *testing.T) {
		t.Setenv(envCodeModeDiscoveryRankerTokenFile, "")
		for _, ranker := range []schemas.MCPCodeModeDiscoveryRanker{
			{URL: "ftp://ranker.example/rank"},
			{URL: "https://user:pass@ranker.example/rank"},
			{URL: "/rank"},
			{URL: "https://ranker.example/rank", TimeoutSeconds: 31},
			{URL: "https://ranker.example/rank", TimeoutSeconds: -1},
		} {
			t.Setenv(envCodeModeDiscoveryRankerURL, "")
			ranker := ranker
			runtime := &schemas.MCPConfig{ToolManagerConfig: &schemas.MCPToolManagerConfig{}}
			applyCodeModeDiscoveryRanker(runtime, fileWith(&ranker))
			if runtime.ToolManagerConfig.CodeModeDiscoveryRanker != nil {
				t.Fatalf("expected %+v to be rejected", ranker)
			}
		}
	})
}
