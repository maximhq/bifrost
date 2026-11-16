package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/cli/internal/config"
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

// TestProjectLaunchIsolation keeps concurrent sessions and SSO helper settings independent.
func TestProjectLaunchIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Trace: keep\nx-BF-project-ID: stale\nx-bf-project-name: stale-name")
	agent, _ := harness.Get("claude")
	var paths []string
	for _, project := range []string{"alpha", "beta"} {
		prepared, err := PrepareCommand(context.Background(), LaunchSpec{Harness: agent, BaseURL: "https://gateway.example/prefix", Project: config.Project{Name: project}, TokenHelperCommand: "bifrost auth print-token"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(prepared.Cleanup)
		path := argumentValue(prepared.Cmd.Args, "--settings")
		paths = append(paths, path)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var settings struct {
			APIKeyHelper string            `json:"apiKeyHelper"`
			Env          map[string]string `json:"env"`
		}
		if err := json.Unmarshal(body, &settings); err != nil {
			t.Fatal(err)
		}
		want := "X-Trace: keep\nx-bf-project-name: " + project
		if settings.Env["ANTHROPIC_CUSTOM_HEADERS"] != want || environmentValue(prepared.Cmd.Env, "ANTHROPIC_CUSTOM_HEADERS") != want {
			t.Fatalf("wrong headers: %s", body)
		}
		if settings.APIKeyHelper != "bifrost auth print-token" {
			t.Fatal("lost helper")
		}
	}
	if paths[0] == paths[1] {
		t.Fatal("sessions share settings")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("modified native settings")
	}
}

// TestProjectLaunchNativeFiles validates the actual child config for each supported adapter.
func TestProjectLaunchNativeFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "pi"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	for _, id := range []string{"codex", "opencode", "pi", "gemini"} {
		t.Run(id, func(t *testing.T) {
			agent, _ := harness.Get(id)
			prepared, err := PrepareCommand(context.Background(), LaunchSpec{Harness: agent, BaseURL: "https://gateway.example", VirtualKey: "sk-bf-test", Model: "test/model", Project: config.Project{ID: "project-one"}})
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Cleanup()
			var path string
			switch id {
			case "codex":
				path = filepath.Join(environmentValue(prepared.Cmd.Env, "CODEX_HOME"), "config.toml")
			case "opencode":
				path = environmentValue(prepared.Cmd.Env, "OPENCODE_CONFIG")
			case "pi":
				path = filepath.Join(environmentValue(prepared.Cmd.Env, "PI_CODING_AGENT_DIR"), "models.json")
			case "gemini":
				if environmentValue(prepared.Cmd.Env, "GEMINI_CLI_CUSTOM_HEADERS") != "x-bf-project-id: project-one" {
					t.Fatal("missing Gemini header")
				}
				return
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), "x-bf-project-id") || !strings.Contains(string(body), "project-one") {
				t.Fatalf("missing header: %s", body)
			}
		})
	}
}

// TestProjectLaunchRejectsConflictingOverrides leaves native files untouched on unsupported overrides.
func TestProjectLaunchRejectsConflictingOverrides(t *testing.T) {
	for _, id := range []string{"claude", "opencode", "antigravity"} {
		t.Run(id, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			sentinel := filepath.Join(home, "config.json")
			if err := os.WriteFile(sentinel, []byte(`{"keep":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OPENCODE_CONFIG", sentinel)
			agent, _ := harness.Get(id)
			spec := LaunchSpec{Harness: agent, BaseURL: "https://gateway.example", Project: config.Project{ID: "p"}}
			if id == "claude" {
				spec.Args = []string{"--settings", "custom.json"}
			}
			if _, err := PrepareCommand(context.Background(), spec); err == nil {
				t.Fatalf("accepted conflicting %s launch", id)
			}
			body, err := os.ReadFile(sentinel)
			if err != nil || string(body) != `{"keep":true}` {
				t.Fatalf("modified native config: %s (%v)", body, err)
			}
		})
	}
}
