package runtime

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/harness"
)

// applyProjectEnvironment scopes only the prepared child configuration, never shared native files.
func applyProjectEnvironment(spec LaunchSpec, env []string) ([]string, error) {
	if spec.Project == (config.Project{}) {
		return env, nil
	}
	value := func(name string) string {
		for _, entry := range env {
			key, v, _ := strings.Cut(entry, "=")
			if key == name {
				return v
			}
		}
		return ""
	}
	var paths []string
	switch spec.Harness.ID {
	case "claude":
		headers, err := harness.ClaudeProjectHeaders(spec.Project)
		if err != nil {
			return nil, err
		}
		return setEnv(env, "ANTHROPIC_CUSTOM_HEADERS", headers), nil
	case "gemini":
		return setEnv(env, "GEMINI_CLI_CUSTOM_HEADERS", spec.Project.MergeHeaders(value("GEMINI_CLI_CUSTOM_HEADERS"), ",")), nil
	case "codex":
		paths = []string{filepath.Join(value("CODEX_HOME"), "config.toml")}
	case "opencode":
		path := value("OPENCODE_CONFIG")
		if path == "" {
			return nil, fmt.Errorf("project-scoped OpenCode launch requires --model")
		}
		paths = []string{path}
	case "pi":
		paths = []string{filepath.Join(value("PI_CODING_AGENT_DIR"), "models.json")}
	}
	return env, harness.WriteProjectConfig(spec.Harness.ID, paths, spec.Project)
}
