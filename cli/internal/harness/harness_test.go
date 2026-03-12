package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeModelRef(t *testing.T) {
	t.Parallel()

	if got := opencodeModelRef("gpt-4.1"); got != "bifrost/gpt-4.1" {
		t.Fatalf("opencodeModelRef() = %q, want %q", got, "bifrost/gpt-4.1")
	}
}

func TestOpencodePreLaunchWritesCustomProviderConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	env, cleanup, err := opencodePreLaunch("https://example.com/openai", "test-key", "gpt-4.1")
	if err != nil {
		t.Fatalf("opencodePreLaunch() error = %v", err)
	}
	defer cleanup()

	if len(env) != 2 {
		t.Fatalf("unexpected env returned: %#v", env)
	}

	configPath := envValue(env, "OPENCODE_CONFIG")
	if configPath == "" {
		t.Fatalf("expected OPENCODE_CONFIG in env, got %#v", env)
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}

	cfg := string(b)
	for _, want := range []string{
		`"model": "bifrost/gpt-4.1"`,
		`"bifrost": {`,
		`"npm": "@ai-sdk/openai-compatible"`,
		`"baseURL": "https://example.com/openai"`,
		`"apiKey": "test-key"`,
		`"gpt-4.1": {`,
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("expected generated config to contain %q, got %s", want, cfg)
		}
	}

	tuiPath := envValue(env, "OPENCODE_TUI_CONFIG")
	if tuiPath == "" {
		t.Fatalf("expected OPENCODE_TUI_CONFIG in env, got %#v", env)
	}
	tuiCfg, err := os.ReadFile(tuiPath)
	if err != nil {
		t.Fatalf("read generated tui config: %v", err)
	}
	if !strings.Contains(string(tuiCfg), `"theme": "system"`) {
		t.Fatalf("expected generated tui config to set system theme, got %s", string(tuiCfg))
	}
}

func TestOpencodePreLaunchPreservesExistingTheme(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	tuiPath := filepath.Join(xdg, "opencode", "tui.json")
	if err := os.MkdirAll(filepath.Dir(tuiPath), 0o755); err != nil {
		t.Fatalf("mkdir tui dir: %v", err)
	}
	if err := os.WriteFile(tuiPath, []byte("{\n  \"theme\": \"light\"\n}\n"), 0o600); err != nil {
		t.Fatalf("write tui config: %v", err)
	}

	env, cleanup, err := opencodePreLaunch("https://example.com/openai", "test-key", "gpt-4.1")
	if err != nil {
		t.Fatalf("opencodePreLaunch() error = %v", err)
	}
	defer cleanup()

	if got := envValue(env, "OPENCODE_TUI_CONFIG"); got != "" {
		t.Fatalf("did not expect OPENCODE_TUI_CONFIG override when user theme exists, got %#v", env)
	}
	if got := envValue(env, "OPENCODE_CONFIG"); got == "" {
		t.Fatalf("expected OPENCODE_CONFIG to remain present, got %#v", env)
	}
}

func TestOpencodePreLaunchAddsSystemThemeWithoutModel(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	env, cleanup, err := opencodePreLaunch("https://example.com/openai", "test-key", "")
	if err != nil {
		t.Fatalf("opencodePreLaunch() error = %v", err)
	}

	tuiPath := envValue(env, "OPENCODE_TUI_CONFIG")
	if tuiPath == "" {
		t.Fatalf("expected OPENCODE_TUI_CONFIG in env, got %#v", env)
	}
	if got := envValue(env, "OPENCODE_CONFIG"); got != "" {
		t.Fatalf("did not expect OPENCODE_CONFIG without a model, got %#v", env)
	}
	if _, err := os.Stat(tuiPath); err != nil {
		t.Fatalf("expected generated tui config to exist: %v", err)
	}

	cleanup()
	if _, err := os.Stat(tuiPath); !os.IsNotExist(err) {
		t.Fatalf("expected generated tui config to be removed after cleanup, stat err=%v", err)
	}
}

func TestLoadOpencodeTUIConfigSupportsJSONC(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tui.json")
	content := "{\n  // keep my choice\n  \"theme\": \"light\",\n  \"foo\": true,\n}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write tui config: %v", err)
	}

	cfg, hasTheme, err := loadOpencodeTUIConfig(path)
	if err != nil {
		t.Fatalf("loadOpencodeTUIConfig() error = %v", err)
	}
	if !hasTheme {
		t.Fatal("expected theme to be detected")
	}
	if cfg["theme"] != "light" {
		t.Fatalf("cfg[theme] = %#v, want %q", cfg["theme"], "light")
	}
}

func TestOpencodeTUIConfigPathPrefersXDG(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got, err := opencodeTUIConfigPath()
	if err != nil {
		t.Fatalf("opencodeTUIConfigPath() error = %v", err)
	}
	want := filepath.Join(xdg, "opencode", "tui.json")
	if got != want {
		t.Fatalf("opencodeTUIConfigPath() = %q, want %q", got, want)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
