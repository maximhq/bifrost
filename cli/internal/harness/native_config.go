package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/cli/internal/config"
)

// claudePreLaunch forces Claude Code into its simpler terminal mode when
// launched inside Bifrost's tab multiplexer. This avoids Claude-specific
// full-screen terminal behavior that doesn't restore reliably across tab swaps.
func claudePreLaunch(baseURL, apiKey, model string) ([]string, func(), error) {
	env := []string{"CLAUDE_CODE_SIMPLE=1"}
	if model = strings.TrimSpace(model); model != "" {
		env = append(env, claudeTierModelEnv(model)...)
	}
	return env, func() {}, nil
}

// claudeWriteNativeConfig writes the bifrost endpoint, API key, and model
// into Claude Code's settings file (~/.claude/settings.json) so the same
// configuration is available when users launch Claude Code directly.
//
// It merges into the existing file, preserving any user-defined settings.
func claudeWriteNativeConfig(baseURL, apiKey, model string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	dir := filepath.Join(home, ".claude")
	settingsPath := filepath.Join(dir, "settings.json")

	// Read existing settings or start fresh
	settings := make(map[string]any)
	if b, err := os.ReadFile(settingsPath); err == nil {
		if err := sonic.Unmarshal(b, &settings); err != nil {
			return fmt.Errorf("parse existing claude settings: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read claude settings: %w", err)
	}

	// Get or create the env map
	envRaw, ok := settings["env"]
	var envMap map[string]any
	if ok {
		envMap, ok = envRaw.(map[string]any)
		if !ok {
			envMap = make(map[string]any)
		}
	} else {
		envMap = make(map[string]any)
	}

	envMap["ANTHROPIC_BASE_URL"] = baseURL
	envMap["ANTHROPIC_API_KEY"] = apiKey
	if model = strings.TrimSpace(model); model != "" {
		for key, value := range claudeTierModelEnvMap(model) {
			envMap[key] = value
		}
		delete(envMap, "ANTHROPIC_MODEL")
	}

	settings["env"] = envMap

	b, err := sonic.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude settings: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}
	return config.WriteAtomic(settingsPath, b, 0o600)
}

func claudeTierModelEnv(model string) []string {
	envMap := claudeTierModelEnvMap(model)
	return []string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL=" + envMap["ANTHROPIC_DEFAULT_SONNET_MODEL"],
		"ANTHROPIC_DEFAULT_OPUS_MODEL=" + envMap["ANTHROPIC_DEFAULT_OPUS_MODEL"],
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=" + envMap["ANTHROPIC_DEFAULT_HAIKU_MODEL"],
	}
}

func claudeTierModelEnvMap(model string) map[string]string {
	return map[string]string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL": model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  model,
	}
}
