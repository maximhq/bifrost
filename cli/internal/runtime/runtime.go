package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/apis"
	"github.com/maximhq/bifrost/cli/internal/harness"
)

// LaunchSpec holds the parameters needed to launch a harness subprocess.
type LaunchSpec struct {
	Harness    harness.Harness
	BaseURL    string
	VirtualKey string
	AgentToken string
	Context    string
	StatePath  string
	// TokenHelperCommand is a secret-free command that prints a current agent
	// access token. Claude receives it through a temporary --settings file.
	TokenHelperCommand string
	Model              string
	Worktree           string // empty = no worktree, non-empty = worktree name (or " " for unnamed)
	Args               []string
}

// BuildEnv constructs the environment variables for the harness process,
// including the provider endpoint, API key, and model overrides.
func BuildEnv(spec LaunchSpec) ([]string, error) {
	endpoint, err := apis.BuildEndpoint(spec.BaseURL, spec.Harness.BasePath)
	if err != nil {
		return nil, err
	}
	env := os.Environ()
	// Provider credentials inherited from the parent shell must never win over
	// the Bifrost credential selected for this launch.
	env = unsetEnv(env, spec.Harness.APIKeyEnv, spec.Harness.AuthTokenEnv, spec.Harness.AgentTokenEnv)
	env = setEnv(env, spec.Harness.BaseURLEnv, endpoint)

	if strings.TrimSpace(spec.TokenHelperCommand) == "" {
		agentToken := strings.TrimSpace(spec.AgentToken)
		if agentToken != "" && spec.Harness.AgentTokenEnv != "" {
			env = setEnv(env, spec.Harness.AgentTokenEnv, agentToken)
		} else if vk := strings.TrimSpace(spec.VirtualKey); vk != "" {
			if spec.Harness.AuthTokenEnv != "" {
				env = setEnv(env, spec.Harness.AuthTokenEnv, vk)
			} else {
				env = setEnv(env, spec.Harness.APIKeyEnv, vk)
			}
		}
	}
	model := strings.TrimSpace(spec.Model)
	if model != "" {
		env = setEnv(env, "BIFROST_MODEL", model)
		if spec.Harness.ModelEnv != "" {
			env = setEnv(env, spec.Harness.ModelEnv, model)
		}
	}

	// Mark session as running inside bifrost
	env = setEnv(env, "BIFROST_SESSION", "1")
	env = setEnv(env, "BIFROST_BASE_URL", spec.BaseURL)
	if strings.TrimSpace(spec.Context) != "" {
		env = setEnv(env, "BIFROST_CONTEXT", spec.Context)
	}
	if strings.TrimSpace(spec.StatePath) != "" {
		env = setEnv(env, "BIFROST_STATE_PATH", spec.StatePath)
	}
	return env, nil
}

// setEnv replaces an environment entry instead of appending a duplicate whose
// precedence differs across operating systems and child runtimes.
func setEnv(env []string, name, value string) []string {
	if strings.TrimSpace(name) == "" {
		return env
	}
	env = unsetEnv(env, name)
	return append(env, name+"="+value)
}

// unsetEnv removes environment entries case-insensitively for Windows parity.
func unsetEnv(env []string, names ...string) []string {
	remove := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			remove[strings.ToLower(name)] = struct{}{}
		}
	}
	filtered := env[:0]
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if _, ok := remove[strings.ToLower(name)]; ok {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// PreparedCmd holds a command ready to execute along with any cleanup
// function that should be called after the process exits.
type PreparedCmd struct {
	Cmd     *exec.Cmd
	Cleanup func()
}

// PrepareCommand builds the exec.Cmd for a harness launch, including
// environment variables, pre-launch hooks, and CLI arguments.
func PrepareCommand(ctx context.Context, spec LaunchSpec) (*PreparedCmd, error) {
	env, err := BuildEnv(spec)
	if err != nil {
		return nil, err
	}

	var cleanupFunctions []func()
	if spec.Harness.PreLaunch != nil {
		endpoint, err := apis.BuildEndpoint(spec.BaseURL, spec.Harness.BasePath)
		if err != nil {
			return nil, fmt.Errorf("build endpoint for pre-launch: %w", err)
		}
		credential := strings.TrimSpace(spec.AgentToken)
		if strings.TrimSpace(spec.TokenHelperCommand) != "" {
			credential = ""
		}
		if credential == "" {
			credential = strings.TrimSpace(spec.VirtualKey)
		}
		if credential == "" {
			credential = "dummy-key"
		}
		extraEnv, cleanup, err := spec.Harness.PreLaunch(endpoint, credential, spec.Model)
		if err != nil {
			return nil, fmt.Errorf("pre-launch %s: %w", spec.Harness.Label, err)
		}
		if cleanup != nil {
			cleanupFunctions = append(cleanupFunctions, cleanup)
		}
		for _, entry := range extraEnv {
			name, value, ok := strings.Cut(entry, "=")
			if !ok || strings.TrimSpace(name) == "" {
				runCleanup(cleanupFunctions)
				return nil, fmt.Errorf("pre-launch %s returned invalid environment entry %q", spec.Harness.Label, entry)
			}
			env = setEnv(env, name, value)
		}
	}
	settingsArgs, settingsCleanup, err := prepareTokenHelperSettings(spec)
	if err != nil {
		runCleanup(cleanupFunctions)
		return nil, err
	}
	if settingsCleanup != nil {
		cleanupFunctions = append(cleanupFunctions, settingsCleanup)
	}

	args := []string{}
	if spec.Harness.RunArgsForMod != nil {
		args = append(args, spec.Harness.RunArgsForMod(spec.Model)...)
	}
	if spec.Worktree != "" && spec.Harness.WorktreeArgs != nil {
		args = append(args, spec.Harness.WorktreeArgs(spec.Worktree)...)
	}
	args = append(args, spec.Args...)
	args = append(args, settingsArgs...)

	cmd := exec.CommandContext(ctx, spec.Harness.Binary, args...)
	cmd.Env = env

	return &PreparedCmd{Cmd: cmd, Cleanup: func() { runCleanup(cleanupFunctions) }}, nil
}

// PrepareLaunchAuthentication validates whether a harness can consume the
// selected credential and creates Claude's refresh-aware token helper command.
func PrepareLaunchAuthentication(agent harness.Harness, agentToken, virtualKey string) (string, error) {
	agentToken = strings.TrimSpace(agentToken)
	if agentToken == "" {
		return "", nil
	}
	if strings.TrimSpace(agent.AgentTokenEnv) == "" && strings.TrimSpace(virtualKey) == "" {
		return "", fmt.Errorf("%s cannot use an Enterprise SSO agent token directly; configure a virtual key with 'bifrost auth set-virtual-key'", agent.Label)
	}
	if agent.ID != "claude" {
		return "", nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve Bifrost executable for Claude authentication: %w", err)
	}
	return BuildCommandString(executable, "auth", "print-token"), nil
}

// prepareTokenHelperSettings gives Claude a refresh-aware credential command
// without writing the access token or modifying the user's settings file.
func prepareTokenHelperSettings(spec LaunchSpec) ([]string, func(), error) {
	command := strings.TrimSpace(spec.TokenHelperCommand)
	if command == "" {
		return nil, nil, nil
	}
	if spec.Harness.ID != "claude" {
		return nil, nil, fmt.Errorf("%s does not support a session token helper", spec.Harness.Label)
	}
	dir, err := os.MkdirTemp("", "bifrost-claude-auth-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temporary Claude auth settings: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	body, err := json.Marshal(map[string]any{"apiKeyHelper": command})
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("encode temporary Claude auth settings: %w", err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("write temporary Claude auth settings: %w", err)
	}
	return []string{"--settings", path}, cleanup, nil
}

func runCleanup(functions []func()) {
	for index := len(functions) - 1; index >= 0; index-- {
		functions[index]()
	}
}

// RunInteractive launches the harness as an interactive subprocess with full
// TTY access. It prints a bifrost banner before launch and a summary after exit.
func RunInteractive(ctx context.Context, stdout, stderr io.Writer, spec LaunchSpec) error {
	p, err := PrepareCommand(ctx, spec)
	if err != nil {
		return err
	}
	if p.Cleanup != nil {
		defer p.Cleanup()
	}

	fmt.Fprint(stdout, renderBanner(spec))

	if err := runWithPTY(ctx, stdout, p.Cmd); err != nil {
		fmt.Fprintf(stdout, "\n\033[36mbifrost>\033[0m session ended with error: %v\n", err)
		return fmt.Errorf("run harness: %w", err)
	}
	fmt.Fprintf(stdout, "\n\033[36mbifrost>\033[0m session ended\n")
	return nil
}

// renderBanner builds the pre-launch info box showing harness, model,
// endpoint, and the equivalent command.
func renderBanner(spec LaunchSpec) string {
	endpoint, err := apis.BuildEndpoint(spec.BaseURL, spec.Harness.BasePath)
	if err != nil {
		endpoint = spec.BaseURL + " (invalid)"
	}

	authStatus := "none"
	if strings.TrimSpace(spec.AgentToken) != "" {
		authStatus = "Enterprise SSO"
	} else if strings.TrimSpace(spec.VirtualKey) != "" {
		authStatus = "virtual key"
	}

	cmdLine := spec.Harness.Binary
	if spec.Harness.RunArgsForMod != nil {
		if a := spec.Harness.RunArgsForMod(spec.Model); len(a) > 0 {
			cmdLine += " " + strings.Join(a, " ")
		}
	}
	if spec.Worktree != "" && spec.Harness.WorktreeArgs != nil {
		if a := spec.Harness.WorktreeArgs(spec.Worktree); len(a) > 0 {
			cmdLine += " " + strings.Join(a, " ")
		}
	}

	cyan := "\033[36m"
	dim := "\033[2m"
	bold := "\033[1m"
	reset := "\033[0m"

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(dim + "───────────────────────────────────────────────────" + reset + "\n")
	b.WriteString(cyan + "bifrost>" + reset + " " + bold + spec.Harness.Label + reset + "  " + dim + spec.Model + reset + "\n")
	b.WriteString(dim + "  endpoint : " + reset + endpoint + "\n")
	b.WriteString(dim + "  auth     : " + reset + authStatus + "\n")
	b.WriteString(dim + "  command  : " + reset + cmdLine + "\n")
	b.WriteString(dim + "───────────────────────────────────────────────────" + reset + "\n")
	b.WriteString("\n")
	return b.String()
}
