package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/apis"
	"github.com/maximhq/bifrost/cli/internal/harness"
)

// LaunchSpec holds the parameters needed to launch a harness subprocess.
type LaunchSpec struct {
	Harness    harness.Harness
	BaseURL    string
	VirtualKey string
	Model      string
	Worktree   string // empty = no worktree, non-empty = worktree name (or " " for unnamed)
}

// BuildEnv constructs the environment variables for the harness process,
// including the provider endpoint, API key, and model overrides.
func BuildEnv(spec LaunchSpec) ([]string, error) {
	endpoint, err := apis.BuildEndpoint(spec.BaseURL, spec.Harness.BasePath)
	if err != nil {
		return nil, err
	}
	env := os.Environ()
	env = append(env, spec.Harness.BaseURLEnv+"="+endpoint)

	vk := strings.TrimSpace(spec.VirtualKey)
	if vk != "" {
		env = append(env, spec.Harness.APIKeyEnv+"="+vk)
	} else {
		env = append(env, spec.Harness.APIKeyEnv+"=dummy-key")
	}

	model := strings.TrimSpace(spec.Model)
	if model != "" {
		env = append(env, "BIFROST_MODEL="+model)
		if spec.Harness.ModelEnv != "" {
			env = append(env, spec.Harness.ModelEnv+"="+model)
		}
	}

	// Mark session as running inside bifrost
	env = append(env, "BIFROST_SESSION=1")
	env = append(env, "BIFROST_BASE_URL="+spec.BaseURL)
	return env, nil
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

	var cleanup func()
	if spec.Harness.PreLaunch != nil {
		endpoint, err := apis.BuildEndpoint(spec.BaseURL, spec.Harness.BasePath)
		if err != nil {
			return nil, fmt.Errorf("build endpoint for pre-launch: %w", err)
		}
		vk := strings.TrimSpace(spec.VirtualKey)
		if vk == "" {
			vk = "dummy-key"
		}
		extraEnv, c, err := spec.Harness.PreLaunch(endpoint, vk, spec.Model)
		if err != nil {
			return nil, fmt.Errorf("pre-launch %s: %w", spec.Harness.Label, err)
		}
		cleanup = c
		env = append(env, extraEnv...)
	}

	args := []string{}
	if spec.Harness.RunArgsForMod != nil {
		args = append(args, spec.Harness.RunArgsForMod(spec.Model)...)
	}
	if spec.Worktree != "" && spec.Harness.WorktreeArgs != nil {
		args = append(args, spec.Harness.WorktreeArgs(spec.Worktree)...)
	}

	cmd := exec.CommandContext(ctx, spec.Harness.Binary, args...)
	cmd.Env = env

	return &PreparedCmd{Cmd: cmd, Cleanup: cleanup}, nil
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

	vkStatus := "no"
	if strings.TrimSpace(spec.VirtualKey) != "" {
		vkStatus = "yes"
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
	b.WriteString(dim + "  vk       : " + reset + vkStatus + "\n")
	b.WriteString(dim + "  command  : " + reset + cmdLine + "\n")
	b.WriteString(dim + "───────────────────────────────────────────────────" + reset + "\n")
	b.WriteString("\n")
	return b.String()
}
