package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

func TestClaudePreLaunchPinsSelectedModelAcrossClaudeTiers(t *testing.T) {
	t.Parallel()

	env, cleanup, err := claudePreLaunch("https://example.com/anthropic", "test-key", "openai/gpt-5")
	if err != nil {
		t.Fatalf("claudePreLaunch() error = %v", err)
	}
	defer cleanup()

	for _, want := range []string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL=openai/gpt-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL=openai/gpt-5",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=openai/gpt-5",
	} {
		parts := strings.SplitN(want, "=", 2)
		if got := envValue(env, parts[0]); got != parts[1] {
			t.Fatalf("env[%q] = %q, want %q", parts[0], got, parts[1])
		}
	}

	if got := envValue(env, "ANTHROPIC_MODEL"); got != "" {
		t.Fatalf("did not expect ANTHROPIC_MODEL in env, got %#v", env)
	}
	if got := envValue(env, "CLAUDE_CODE_SIMPLE"); got != "" {
		t.Fatalf("did not expect CLAUDE_CODE_SIMPLE in env, got %#v", env)
	}
}

func TestClaudeWriteNativeConfigPinsTierDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	initial := `{"env":{"EXISTING":"keep","ANTHROPIC_MODEL":"stale-model"}}`
	if err := os.WriteFile(settingsPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial settings: %v", err)
	}

	if err := claudeWriteNativeConfig("https://example.com/anthropic", "test-key", "openai/gpt-5"); err != nil {
		t.Fatalf("claudeWriteNativeConfig() error = %v", err)
	}

	b, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var settings map[string]any
	if err := sonic.Unmarshal(b, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	envRaw, ok := settings["env"]
	if !ok {
		t.Fatalf("expected env map in settings, got %#v", settings)
	}
	envMap, ok := envRaw.(map[string]any)
	if !ok {
		t.Fatalf("env map type = %T, want map[string]any", envRaw)
	}

	for key, want := range map[string]string{
		"EXISTING":                       "keep",
		"ANTHROPIC_BASE_URL":             "https://example.com/anthropic",
		"ANTHROPIC_API_KEY":              "test-key",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "openai/gpt-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "openai/gpt-5",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "openai/gpt-5",
	} {
		if got, _ := envMap[key].(string); got != want {
			t.Fatalf("env[%q] = %q, want %q", key, got, want)
		}
	}

	if _, ok := envMap["ANTHROPIC_MODEL"]; ok {
		t.Fatalf("did not expect legacy ANTHROPIC_MODEL in settings env: %#v", envMap)
	}
}

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

func TestCodexWriteNativeConfigMergesAuthAndTOML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}

	authPath := filepath.Join(dir, "auth.json")
	initialAuth := `{"auth_mode":"apikey","OPENAI_API_KEY":"stale-key","tokens":{"id_token":"keep-me"}}`
	if err := os.WriteFile(authPath, []byte(initialAuth), 0o600); err != nil {
		t.Fatalf("write initial auth: %v", err)
	}

	configPath := filepath.Join(dir, "config.toml")
	initialTOML := `openai_base_url="http://old.example/openai/v1"
env_key="stale-literal-key"
model = "gpt-old"
model_reasoning_effort = "medium"

[projects."/Users/me/proj"]
trust_level = "trusted"
`
	if err := os.WriteFile(configPath, []byte(initialTOML), 0o600); err != nil {
		t.Fatalf("write initial config.toml: %v", err)
	}

	if err := codexWriteNativeConfig("http://localhost:8080/openai/v1", "sk-bf-new", "gpt-5.5"); err != nil {
		t.Fatalf("codexWriteNativeConfig() error = %v", err)
	}

	// auth.json should keep tokens.id_token but flip OPENAI_API_KEY.
	authBytes, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	var auth map[string]any
	if err := sonic.Unmarshal(authBytes, &auth); err != nil {
		t.Fatalf("unmarshal auth.json: %v", err)
	}
	if got, _ := auth["OPENAI_API_KEY"].(string); got != "sk-bf-new" {
		t.Fatalf("auth.OPENAI_API_KEY = %q, want %q", got, "sk-bf-new")
	}
	if got, _ := auth["auth_mode"].(string); got != "apikey" {
		t.Fatalf("auth.auth_mode = %q, want %q", got, "apikey")
	}
	tokens, ok := auth["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("expected tokens map preserved, got %#v", auth["tokens"])
	}
	if got, _ := tokens["id_token"].(string); got != "keep-me" {
		t.Fatalf("tokens.id_token = %q, want %q", got, "keep-me")
	}

	// config.toml should have new values for top-level keys, preserve
	// model_reasoning_effort and the [projects.*] table.
	tomlBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	got := string(tomlBytes)
	for _, want := range []string{
		`openai_base_url = "http://localhost:8080/openai/v1"`,
		`env_key = "OPENAI_API_KEY"`,
		`model = "gpt-5.5"`,
		`model_reasoning_effort = "medium"`,
		`[projects."/Users/me/proj"]`,
		`trust_level = "trusted"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected config.toml to contain %q, got:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		`"http://old.example/openai/v1"`,
		`"stale-literal-key"`,
		`"gpt-old"`,
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("expected config.toml to no longer contain %q, got:\n%s", unwanted, got)
		}
	}
}

func TestCodexWriteNativeConfigCreatesFilesWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := codexWriteNativeConfig("http://localhost:8080/openai/v1", "sk-bf-fresh", ""); err != nil {
		t.Fatalf("codexWriteNativeConfig() error = %v", err)
	}

	authBytes, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	var auth map[string]any
	if err := sonic.Unmarshal(authBytes, &auth); err != nil {
		t.Fatalf("unmarshal auth.json: %v", err)
	}
	if got, _ := auth["OPENAI_API_KEY"].(string); got != "sk-bf-fresh" {
		t.Fatalf("auth.OPENAI_API_KEY = %q", got)
	}

	tomlBytes, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	got := string(tomlBytes)
	for _, want := range []string{
		`openai_base_url = "http://localhost:8080/openai/v1"`,
		`env_key = "OPENAI_API_KEY"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected config.toml to contain %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, `model =`) {
		t.Fatalf("did not expect model line when model is empty, got:\n%s", got)
	}
}

func TestCodexWriteNativeConfigSkipsAuthForDummyKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := codexWriteNativeConfig("http://localhost:8080/openai/v1", "dummy-key", ""); err != nil {
		t.Fatalf("codexWriteNativeConfig() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no auth.json for dummy key, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Fatalf("expected config.toml to still be written, stat err=%v", err)
	}
}

func TestSetTopLevelTOMLKeysAppendsBeforeFirstTable(t *testing.T) {
	t.Parallel()

	input := []byte(`# header comment
model_reasoning_effort = "medium"

[projects."x"]
trust_level = "trusted"
`)
	out := string(setTopLevelTOMLKeys(input, map[string]string{
		"openai_base_url": "http://localhost:8080/openai/v1",
		"env_key":         "OPENAI_API_KEY",
		"model":           "gpt-5.5",
	}))

	preTable, _, ok := strings.Cut(out, `[projects."x"]`)
	if !ok {
		t.Fatalf("expected [projects.\"x\"] header preserved, got:\n%s", out)
	}
	for _, want := range []string{
		`# header comment`,
		`model_reasoning_effort = "medium"`,
		`openai_base_url = "http://localhost:8080/openai/v1"`,
		`env_key = "OPENAI_API_KEY"`,
		`model = "gpt-5.5"`,
	} {
		if !strings.Contains(preTable, want) {
			t.Fatalf("expected pre-table section to contain %q, got:\n%s", want, preTable)
		}
	}
	if !strings.Contains(out, `trust_level = "trusted"`) {
		t.Fatalf("expected table body preserved, got:\n%s", out)
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
