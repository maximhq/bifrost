package command

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"unicode"

	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/output"
	"github.com/maximhq/bifrost/cli/internal/secrets"
)

// runContext manages non-secret gateway profile metadata.
func (r *Runner) runContext(args []string, format output.Format) error {
	statePath, err := r.resolveStatePath()
	if err != nil {
		return err
	}
	state, err := config.LoadState(statePath)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(r.Out, "Usage: bifrost context <list|show|add|use|delete>\n")
		return err
	}
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		usage := map[string]string{
			"list":    "Usage: bifrost context list\n",
			"show":    "Usage: bifrost context show [name-or-id]\n",
			"current": "Usage: bifrost context current\n",
			"add":     "Usage: bifrost context add <name> --base-url <url> [--default-model MODEL] [--use]\n",
			"use":     "Usage: bifrost context use <name-or-id>\n",
			"delete":  "Usage: bifrost context delete <name-or-id> --yes\n",
		}
		if text, ok := usage[args[0]]; ok {
			_, err := fmt.Fprint(r.Out, text)
			return err
		}
	}
	switch args[0] {
	case "list":
		return r.listContexts(state, format)
	case "show", "current":
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		return r.showContext(state, name, format)
	case "add":
		return r.addContext(statePath, state, args[1:])
	case "use":
		return r.useContext(statePath, state, args[1:])
	case "delete":
		return r.deleteContext(statePath, state, args[1:])
	default:
		return fmt.Errorf("unknown context action %q", args[0])
	}
}

// listContexts prints all configured gateway contexts without credentials.
func (r *Runner) listContexts(state *config.State, format output.Format) error {
	rows := make([]map[string]any, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		rows = append(rows, map[string]any{
			"current": profile.ID == state.LastProfileID,
			"id":      profile.ID, "name": profile.Name, "base_url": profile.BaseURL, "default_model": profile.DefaultModel,
		})
	}
	body, err := json.Marshal(map[string]any{"data": rows})
	if err != nil {
		return err
	}
	return output.Print(r.Out, body, format)
}

// showContext prints one gateway context without credentials.
func (r *Runner) showContext(state *config.State, name string, format output.Format) error {
	profile := selectProfile(state, name)
	if profile == nil {
		return errorsForMissingContext(name)
	}
	body, err := json.Marshal(map[string]any{
		"id": profile.ID, "name": profile.Name, "base_url": profile.BaseURL,
		"default_model": profile.DefaultModel, "current": profile.ID == state.LastProfileID,
	})
	if err != nil {
		return err
	}
	return output.Print(r.Out, body, format)
}

// addContext validates and persists a new gateway context.
func (r *Runner) addContext(statePath string, state *config.State, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bifrost context add <name> --base-url <url>")
	}
	name := strings.TrimSpace(args[0])
	fs := flag.NewFlagSet("bifrost context add", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var baseURL string
	var model string
	var makeCurrent bool
	fs.StringVar(&baseURL, "base-url", "", "gateway base URL")
	fs.StringVar(&model, "default-model", "", "default model")
	fs.BoolVar(&makeCurrent, "use", false, "make this the current context")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if name == "" || strings.TrimSpace(baseURL) == "" {
		return fmt.Errorf("context name and --base-url are required")
	}
	id := contextID(name)
	if id == "" {
		return fmt.Errorf("context name must contain at least one letter or number")
	}
	if state.ResolveProfile(id) != nil || state.ProfileByName(name) != nil {
		return fmt.Errorf("context %q already exists", name)
	}
	profile := config.Profile{ID: id, Name: name, BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), DefaultModel: strings.TrimSpace(model)}
	state.Profiles = append(state.Profiles, profile)
	if state.LastProfileID == "" || makeCurrent {
		state.LastProfileID = id
	}
	if err := config.SaveState(statePath, state); err != nil {
		return err
	}
	_, err := fmt.Fprintf(r.Out, "Added context %q (%s).\n", name, id)
	return err
}

// useContext selects a configured context for subsequent commands and launches.
func (r *Runner) useContext(statePath string, state *config.State, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bifrost context use <name-or-id>")
	}
	profile := state.ResolveProfile(args[0])
	if profile == nil {
		return errorsForMissingContext(args[0])
	}
	state.LastProfileID = profile.ID
	if err := config.SaveState(statePath, state); err != nil {
		return err
	}
	_, err := fmt.Fprintf(r.Out, "Using context %q.\n", profile.Name)
	return err
}

// deleteContext removes a context and all of its keyring credentials.
func (r *Runner) deleteContext(statePath string, state *config.State, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bifrost context delete <name-or-id> --yes")
	}
	profile := state.ResolveProfile(args[0])
	if profile == nil {
		return errorsForMissingContext(args[0])
	}
	fs := flag.NewFlagSet("bifrost context delete", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var yes bool
	fs.BoolVar(&yes, "yes", false, "confirm deletion")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !yes {
		return fmt.Errorf("refusing to delete context %q without --yes", profile.Name)
	}
	id := profile.ID
	displayName := profile.Name
	kinds := []secrets.Kind{
		secrets.VirtualKey,
		secrets.ManagementKey,
		secrets.SessionToken,
		secrets.AgentToken,
		secrets.AgentRefreshToken,
		secrets.AgentUser,
		secrets.AgentVirtualKeyID,
	}
	// Snapshot every credential before deleting any of them, so a failure
	// partway through (a delete, or the state save that follows) can be
	// rolled back: secrets.Delete is idempotent, so simply retrying after a
	// failure cannot recover a credential that was already removed.
	snapshot := make(map[secrets.Kind]string, len(kinds))
	for _, kind := range kinds {
		value, err := r.Secrets.Get(id, kind)
		if err != nil {
			return fmt.Errorf("snapshot credentials before deleting context %q: %w", displayName, err)
		}
		snapshot[kind] = value
	}
	restoreOnFailure := func(cause error) error {
		var restoreErrs []error
		for kind, value := range snapshot {
			if value == "" {
				continue
			}
			if err := r.Secrets.Set(id, kind, value); err != nil {
				restoreErrs = append(restoreErrs, err)
			}
		}
		if len(restoreErrs) > 0 {
			return fmt.Errorf("%w (additionally failed to restore credentials: %w)", cause, errors.Join(restoreErrs...))
		}
		return cause
	}
	for _, kind := range kinds {
		if err := r.Secrets.Delete(id, kind); err != nil {
			return restoreOnFailure(err)
		}
	}
	state.DeleteProfile(id)
	if err := config.SaveState(statePath, state); err != nil {
		return restoreOnFailure(err)
	}
	_, err := fmt.Fprintf(r.Out, "Deleted context %q and its stored credentials.\n", displayName)
	return err
}

// contextID converts a display name into a stable local identifier.
func contextID(name string) string {
	var builder strings.Builder
	lastDash := false
	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

// errorsForMissingContext returns a consistent missing-context error.
func errorsForMissingContext(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("no gateway context is configured")
	}
	return fmt.Errorf("gateway context %q was not found", name)
}
