package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

func TestClaudePreLaunchPinsSelectedModelAcrossClaudeTiers(t *testing.T) {
	unsetEnvForTest(t, "ENABLE_TOOL_SEARCH")
	unsetEnvForTest(t, "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY")

	env, cleanup, err := claudePreLaunch("https://example.com/anthropic", "test-key", "openai/gpt-5")
	if err != nil {
		t.Fatalf("claudePreLaunch() error = %v", err)
	}
	defer cleanup()

	for _, want := range []string{
		"ENABLE_TOOL_SEARCH=true",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
		"ANTHROPIC_MODEL=openai/gpt-5",
		"ANTHROPIC_DEFAULT_SONNET_MODEL=openai/gpt-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL=openai/gpt-5",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=openai/gpt-5",
	} {
		parts := strings.SplitN(want, "=", 2)
		if got := envValue(env, parts[0]); got != parts[1] {
			t.Fatalf("env[%q] = %q, want %q", parts[0], got, parts[1])
		}
	}

	if got := envValue(env, "CLAUDE_CODE_SIMPLE"); got != "" {
		t.Fatalf("did not expect CLAUDE_CODE_SIMPLE in env, got %#v", env)
	}
}

// unsetEnvForTest removes an environment variable for the duration of the
// test and restores its original state (unset or original value) afterward.
// t.Setenv is not used here because setting an empty value still leaves the
// variable "present" to os.LookupEnv, defeating isolation for code that
// distinguishes absence from an empty value.
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	original, wasSet := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(key, original)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestClaudeWriteNativeConfigPinsTierDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	initial := `{"apiKeyHelper":"stale-helper","env":{"EXISTING":"keep","ANTHROPIC_MODEL":"stale-model","ANTHROPIC_API_KEY":"stale-key"}}`
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
		"EXISTING":                                   "keep",
		"ANTHROPIC_BASE_URL":                         "https://example.com/anthropic",
		"ANTHROPIC_AUTH_TOKEN":                       "test-key",
		"ANTHROPIC_MODEL":                            "openai/gpt-5",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":             "openai/gpt-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":               "openai/gpt-5",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":              "openai/gpt-5",
		"ENABLE_TOOL_SEARCH":                         "true",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
	} {
		if got, _ := envMap[key].(string); got != want {
			t.Fatalf("env[%q] = %q, want %q", key, got, want)
		}
	}

	if _, ok := envMap["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("did not expect competing ANTHROPIC_API_KEY in settings env: %#v", envMap)
	}
	if _, ok := settings["apiKeyHelper"]; ok {
		t.Fatalf("did not expect competing apiKeyHelper in settings: %#v", settings)
	}
	if got, _ := settings["model"].(string); got != "openai/gpt-5" {
		t.Fatalf("settings.model = %q", got)
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
		`"baseURL": "https://example.com/openai/v1"`,
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

	// Pre-existing auth.json should be left untouched.
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

	// auth.json must be unchanged — native configure no longer touches it.
	authBytes, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	if string(authBytes) != initialAuth {
		t.Fatalf("auth.json was modified; want it left untouched, got:\n%s", authBytes)
	}

	// config.toml should select a custom Responses provider with the virtual
	// key inlined, preserve model_reasoning_effort and the [projects.*] table.
	tomlBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	got := string(tomlBytes)
	for _, want := range []string{
		`model_provider = "bifrost"`,
		`model = "gpt-5.5"`,
		`model_reasoning_effort = "medium"`,
		`[model_providers.bifrost]`,
		`name = "Bifrost"`,
		`base_url = "http://localhost:8080/openai/v1"`,
		`wire_api = "responses"`,
		`experimental_bearer_token = "sk-bf-new"`,
		`requires_openai_auth = false`,
		`supports_websockets = false`,
		`[projects."/Users/me/proj"]`,
		`trust_level = "trusted"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected config.toml to contain %q, got:\n%s", want, got)
		}
	}
	// env_key must not appear — the virtual key is inlined instead.
	for _, unwanted := range []string{
		`"http://old.example/openai/v1"`,
		`"stale-literal-key"`,
		`"gpt-old"`,
		`openai_base_url=`,
		`env_key`,
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

	// auth.json should NOT be created — only config.toml is written.
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no auth.json to be created, stat err=%v", err)
	}

	tomlBytes, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	got := string(tomlBytes)
	for _, want := range []string{
		`model_provider = "bifrost"`,
		`[model_providers.bifrost]`,
		`base_url = "http://localhost:8080/openai/v1"`,
		`wire_api = "responses"`,
		`experimental_bearer_token = "sk-bf-fresh"`,
		`requires_openai_auth = false`,
		`supports_websockets = false`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected config.toml to contain %q, got:\n%s", want, got)
		}
	}
	// env_key must not appear when using experimental_bearer_token.
	if strings.Contains(got, `env_key`) {
		t.Fatalf("did not expect env_key when virtual key is inlined, got:\n%s", got)
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
	tomlBytes, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("expected config.toml to still be written: %v", err)
	}
	got := string(tomlBytes)
	// dummy-key should fall back to env_key, not embed the token.
	if !strings.Contains(got, `env_key = "OPENAI_API_KEY"`) {
		t.Fatalf("expected env_key fallback for dummy key, got:\n%s", got)
	}
	if strings.Contains(got, `experimental_bearer_token`) {
		t.Fatalf("did not expect experimental_bearer_token for dummy key, got:\n%s", got)
	}
}

func TestCodexPreLaunchUsesTemporaryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	environment, cleanup, err := codexPreLaunch("http://localhost:8080/openai", "sk-bf-test", "gpt-test")
	if err != nil {
		t.Fatalf("codexPreLaunch() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("codexPreLaunch() returned no cleanup")
	}
	defer cleanup()
	var codexHome string
	for _, entry := range environment {
		if strings.HasPrefix(entry, "CODEX_HOME=") {
			codexHome = strings.TrimPrefix(entry, "CODEX_HOME=")
		}
	}
	if codexHome == "" || codexHome == filepath.Join(home, ".codex") {
		t.Fatalf("CODEX_HOME = %q, want an isolated temporary directory", codexHome)
	}
	configured, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatalf("read temporary config.toml: %v", err)
	}
	if !strings.Contains(string(configured), `model_provider = "bifrost"`) {
		t.Fatalf("temporary config missing Bifrost provider:\n%s", configured)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("pre-launch mutated the real Codex config: %v", err)
	}
}

func TestCodexPreLaunchPreservesUserPreferences(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	userHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	userConfig := `model = "gpt-user"
model_provider = "openai"
model_reasoning_effort = "high"

[mcp_servers.local]
command = "example-mcp"

[projects."/workspace"]
trust_level = "trusted"

[model_providers.bifrost]
name = "Stale Bifrost"
base_url = "https://stale.example/v1"
`
	if err := os.WriteFile(filepath.Join(userHome, "config.toml"), []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	environment, cleanup, err := codexPreLaunch("http://localhost:8080/openai", "sk-bf-test", "gpt-selected")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	codexHome := envValue(environment, "CODEX_HOME")
	configured, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(configured)
	for _, expected := range []string{
		`model = "gpt-selected"`, `model_provider = "bifrost"`, `model_reasoning_effort = "high"`,
		`[mcp_servers.local]`, `command = "example-mcp"`, `[projects."/workspace"]`, `trust_level = "trusted"`,
		`base_url = "http://localhost:8080/openai/v1"`,
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("temporary config does not contain %q:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "stale.example") || strings.Contains(got, `model_provider = "openai"`) {
		t.Fatalf("temporary config retained stale routing:\n%s", got)
	}
	original, err := os.ReadFile(filepath.Join(userHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != userConfig {
		t.Fatal("Codex pre-launch modified the user's config")
	}
}

// TestReplaceTOMLTableRecognizesCommentedHeader verifies a table header with
// a trailing comment is still recognized as the existing table, so it is
// replaced in place instead of duplicated.
func TestReplaceTOMLTableRecognizesCommentedHeader(t *testing.T) {
	t.Parallel()

	input := []byte(`model = "gpt-5.5"

[model_providers.bifrost] # custom provider
base_url = "http://old.example.com"
`)
	out := string(replaceTOMLTable(input, "model_providers.bifrost", []string{`base_url = "http://new.example.com"`}))

	if count := strings.Count(out, "[model_providers.bifrost]"); count != 1 {
		t.Fatalf("expected exactly one [model_providers.bifrost] table, got %d:\n%s", count, out)
	}
	if strings.Contains(out, "http://old.example.com") {
		t.Fatalf("expected old table body removed, got:\n%s", out)
	}
	if !strings.Contains(out, "http://new.example.com") {
		t.Fatalf("expected new table body present, got:\n%s", out)
	}
}

// TestReplaceTOMLTableRecognizesQuotedTableName verifies a table declared
// with a quoted final segment ([a."b"]) is recognized as the same table as
// the bare form (a.b), which TOML treats as identical identifiers, so the
// existing table is replaced instead of duplicated.
func TestReplaceTOMLTableRecognizesQuotedTableName(t *testing.T) {
	t.Parallel()

	input := []byte(`model = "gpt-5.5"

[model_providers."bifrost"]
base_url = "http://old.example.com"
`)
	out := string(replaceTOMLTable(input, "model_providers.bifrost", []string{`base_url = "http://new.example.com"`}))

	if strings.Contains(out, "http://old.example.com") {
		t.Fatalf("expected old (quoted-header) table body removed, got:\n%s", out)
	}
	if !strings.Contains(out, "http://new.example.com") {
		t.Fatalf("expected new table body present, got:\n%s", out)
	}
	if count := strings.Count(out, "base_url ="); count != 1 {
		t.Fatalf("expected exactly one base_url definition, got %d:\n%s", count, out)
	}
}

// TestSetTopLevelTOMLKeysReplacesQuotedExistingKey verifies a top-level key
// written with quotes ("model" = ...) is recognized as the same identifier
// as the bare form when a target sets it, so the existing quoted line is
// replaced instead of a bare duplicate being appended.
func TestSetTopLevelTOMLKeysReplacesQuotedExistingKey(t *testing.T) {
	t.Parallel()

	input := []byte(`"model" = "gpt-old"

[projects."x"]
trust_level = "trusted"
`)
	out := string(setTopLevelTOMLKeys(input, map[string]string{"model": "gpt-5.5"}))

	if strings.Contains(out, "gpt-old") {
		t.Fatalf("expected the quoted key's stale value removed, got:\n%s", out)
	}
	if !strings.Contains(out, `"gpt-5.5"`) {
		t.Fatalf("expected the new model value present, got:\n%s", out)
	}
	if count := strings.Count(out, "model"); count != 1 {
		t.Fatalf("expected exactly one model key (no bare duplicate), got %d:\n%s", count, out)
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

func TestCodexConfigureReplacesNestedAuthentication(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	original := "[model_providers.bifrost]\nname = \"Old\"\n[model_providers.bifrost.auth]\ncommand = \"stale-helper\"\n[model_providers.other]\nname = \"Keep\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := codexWriteNativeConfig("https://gateway/openai", "virtual-key", "model"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "stale-helper") || !strings.Contains(string(body), "Keep") {
		t.Fatalf("stale provider auth survived or unrelated config was lost: %s", body)
	}
}

func TestNativeJSONConfigPreservesUserSettings(t *testing.T) {
	for _, agentID := range []string{"opencode", "gemini"} {
		t.Run(agentID, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("OPENCODE_CONFIG", "")
			paths, err := NativeConfigPaths(agentID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(paths[0]), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths[0], []byte(`{"theme":"keep-me", "provider":{"other":{"name":"Other"}},}`), 0o600); err != nil {
				t.Fatal(err)
			}
			agent, _ := Get(agentID)
			if err := agent.WriteNativeConfig("https://gateway/openai", "sk-bf-literal", "provider/model"); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), "keep-me") || !strings.Contains(string(body), "Other") {
				t.Fatalf("lost user config: %s", body)
			}
			if agentID == "opencode" && (!strings.Contains(string(body), "sk-bf-literal") || !strings.Contains(string(body), "/openai/v1")) {
				t.Fatalf("missing gateway credential or URL: %s", body)
			}
			if agentID == "gemini" {
				env, err := os.ReadFile(paths[1])
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(env), "GEMINI_API_KEY='sk-bf-literal'") || !strings.Contains(string(body), "gemini-api-key") {
					t.Fatalf("missing Gemini API key authentication")
				}
			}
			if err := os.WriteFile(paths[0], []byte("invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := agent.WriteNativeConfig("https://gateway", "key", "model"); err == nil {
				t.Fatal("malformed user config was overwritten")
			}
		})
	}
}

func TestClaudeConfigureRejectsNullSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths, err := NativeConfigPaths("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths[0]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("null config panicked: %v", recovered)
		}
	}()
	if err := claudeWriteNativeConfig("https://gateway", "virtual-key", "model"); err == nil {
		t.Fatal("null settings accepted")
	}
}

func TestOpenCodeConfigureRemovesConflictingAuthHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENCODE_CONFIG", "")
	paths, err := NativeConfigPaths("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths[0]), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"provider":{"bifrost":{"options":{"headers":{"Authorization":"Bearer stale-token","x-bf-vk":"old-key","x-custom":"keep-me"}}}}}`
	if err := os.WriteFile(paths[0], []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := opencodeWriteNativeConfig("https://gateway/openai/v1", "selected-virtual-key", "provider/model"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "stale-token") || strings.Contains(string(body), "old-key") || !strings.Contains(string(body), "keep-me") {
		t.Fatalf("conflicting auth headers survived or unrelated header lost: %s", body)
	}
	if strings.Contains(string(body), "/v1/v1") {
		t.Fatal("API version duplicated")
	}
}
