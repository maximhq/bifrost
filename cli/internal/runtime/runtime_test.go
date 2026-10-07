package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/cli/internal/harness"
)

func TestBuildEnvUsesAgentTokenWithoutLeakingCompetingClaudeKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "provider-key-from-shell")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale-token-from-shell")
	claude, _ := harness.Get("claude")
	env, err := BuildEnv(LaunchSpec{
		Harness: claude, BaseURL: "https://gateway.example", AgentToken: "ck-bf-agent-current", Context: "engineering",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := environmentValue(env, "ANTHROPIC_AUTH_TOKEN"); got != "ck-bf-agent-current" {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN = %q", got)
	}
	if got := environmentValue(env, "ANTHROPIC_API_KEY"); got != "" {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want empty", got)
	}
	if got := environmentValue(env, "BIFROST_CONTEXT"); got != "engineering" {
		t.Fatalf("BIFROST_CONTEXT = %q", got)
	}
}

func TestPrepareCommandUsesTemporaryClaudeTokenHelper(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "provider-key-from-shell")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale-token-from-shell")
	claude, _ := harness.Get("claude")
	prepared, err := PrepareCommand(context.Background(), LaunchSpec{
		Harness: claude, BaseURL: "https://gateway.example", AgentToken: "ck-bf-agent-current",
		TokenHelperCommand: "'/opt/bifrost cli' auth print-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Cleanup == nil {
		t.Fatal("expected temporary settings cleanup")
	}
	settingsPath := argumentValue(prepared.Cmd.Args, "--settings")
	if settingsPath == "" {
		t.Fatalf("command args = %#v", prepared.Cmd.Args)
	}
	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "ck-bf-agent-current") {
		t.Fatalf("temporary settings contain the agent token: %s", body)
	}
	var settings map[string]string
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatal(err)
	}
	if got := settings["apiKeyHelper"]; got != "'/opt/bifrost cli' auth print-token" {
		t.Fatalf("apiKeyHelper = %q", got)
	}
	if got := environmentValue(prepared.Cmd.Env, "ANTHROPIC_AUTH_TOKEN"); got != "" {
		t.Fatalf("static ANTHROPIC_AUTH_TOKEN leaked alongside helper: %q", got)
	}
	settingsDir := filepath.Dir(settingsPath)
	prepared.Cleanup()
	if _, err := os.Stat(settingsDir); !os.IsNotExist(err) {
		t.Fatalf("temporary settings directory still exists: %v", err)
	}
}

func TestPrepareCommandDeduplicatesPreLaunchEnvironment(t *testing.T) {
	t.Setenv("BIFROST_TEST_ENV", "inherited")
	agent := harness.Harness{
		ID: "test", Label: "Test", Binary: "test-agent", BasePath: "/openai", BaseURLEnv: "TEST_BASE_URL",
		PreLaunch: func(_, _, _ string) ([]string, func(), error) {
			return []string{"BIFROST_TEST_ENV=override"}, nil, nil
		},
	}
	prepared, err := PrepareCommand(context.Background(), LaunchSpec{Harness: agent, BaseURL: "https://gateway.example"})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range prepared.Cmd.Env {
		if strings.HasPrefix(strings.ToLower(entry), "bifrost_test_env=") {
			count++
			if entry != "BIFROST_TEST_ENV=override" {
				t.Fatalf("environment entry = %q", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("BIFROST_TEST_ENV entries = %d, want 1", count)
	}
}

func environmentValue(env []string, name string) string {
	prefix := strings.ToLower(name) + "="
	for _, entry := range env {
		if strings.HasPrefix(strings.ToLower(entry), prefix) {
			return entry[len(prefix):]
		}
	}
	return ""
}

func argumentValue(arguments []string, name string) string {
	for index := range arguments {
		if arguments[index] == name && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return ""
}
