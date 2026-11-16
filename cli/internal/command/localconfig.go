package command

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/output"
)

// runLocalConfig gets and mutates non-secret values in the selected context.
func (r *Runner) runLocalConfig(env *environment, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(r.Out, `Usage:
  bifrost config get [base-url|default-model|context]
  bifrost config set <base-url|default-model> <value>
  bifrost config unset <default-model>
`)
		return err
	}
	if env.Profile == nil {
		return fmt.Errorf("local config requires a named context")
	}
	switch args[0] {
	case "get":
		return r.getLocalConfig(env, args[1:])
	case "set":
		return r.setLocalConfig(env, args[1:])
	case "unset":
		return r.unsetLocalConfig(env, args[1:])
	default:
		return fmt.Errorf("unknown config action %q", args[0])
	}
}

// getLocalConfig prints either one value or all selected-context metadata.
func (r *Runner) getLocalConfig(env *environment, args []string) error {
	values := map[string]any{
		"context": env.Profile.ID, "base-url": env.Profile.BaseURL, "default-model": env.Profile.DefaultModel,
	}
	if len(args) > 1 {
		return fmt.Errorf("usage: bifrost config get [key]")
	}
	if len(args) == 1 {
		value, ok := values[args[0]]
		if !ok {
			return fmt.Errorf("unknown config key %q", args[0])
		}
		_, err := fmt.Fprintln(r.Out, value)
		return err
	}
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return output.Print(r.Out, body, env.Output)
}

// setLocalConfig validates and persists one selected-context value.
func (r *Runner) setLocalConfig(env *environment, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: bifrost config set <base-url|default-model> <value>")
	}
	value := strings.TrimSpace(args[1])
	if value == "" {
		return fmt.Errorf("config value cannot be empty")
	}
	switch args[0] {
	case "base-url":
		env.Profile.BaseURL = strings.TrimRight(value, "/")
	case "default-model":
		env.Profile.DefaultModel = value
	default:
		return fmt.Errorf("unknown or read-only config key %q", args[0])
	}
	if err := config.SaveState(env.StatePath, env.State); err != nil {
		return err
	}
	_, err := fmt.Fprintf(r.Out, "Set %s for context %q.\n", args[0], env.Profile.Name)
	return err
}

// unsetLocalConfig clears an optional selected-context value.
func (r *Runner) unsetLocalConfig(env *environment, args []string) error {
	if len(args) != 1 || args[0] != "default-model" {
		return fmt.Errorf("usage: bifrost config unset default-model")
	}
	env.Profile.DefaultModel = ""
	if err := config.SaveState(env.StatePath, env.State); err != nil {
		return err
	}
	_, err := fmt.Fprintf(r.Out, "Unset default-model for context %q.\n", env.Profile.Name)
	return err
}
