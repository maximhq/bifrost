package command

import (
	"fmt"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/harness"
)

var builtInCompletionCommands = []string{
	"api", "auth", "chat", "completion", "config", "configure", "context", "doctor", "http",
	"help", "infer", "keys", "launch", "request", "resources", "run", "status", "unconfigure",
	"update", "usage", "version",
}

// runCompletion writes a dependency-free completion script for a supported shell.
func (r *Runner) runCompletion(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bifrost completion <bash|zsh|fish>")
	}
	switch strings.ToLower(args[0]) {
	case "bash":
		completionCommands := strings.Join(allCompletionCommands(), " ")
		_, err := fmt.Fprintf(r.Out, `_bifrost_complete() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  COMPREPLY=( $(compgen -W %q -- "$cur") )
}
complete -F _bifrost_complete bifrost
`, completionCommands)
		return err
	case "zsh":
		completionCommands := strings.Join(allCompletionCommands(), " ")
		_, err := fmt.Fprintf(r.Out, `#compdef bifrost
_bifrost() {
  local -a commands
  commands=(%s)
  _describe 'command' commands
}
compdef _bifrost bifrost
`, quoteZshWords(completionCommands))
		return err
	case "fish":
		for _, command := range allCompletionCommands() {
			if _, err := fmt.Fprintf(r.Out, "complete -c bifrost -f -n '__fish_use_subcommand' -a %s\n", command); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported shell %q; use bash, zsh, or fish", args[0])
	}
}

// allCompletionCommands combines built-in and registry-backed commands without drift.
func allCompletionCommands() []string {
	seen := make(map[string]struct{}, len(builtInCompletionCommands)+len(resourceRegistry))
	commands := make([]string, 0, len(builtInCompletionCommands)+len(resourceRegistry))
	for _, command := range builtInCompletionCommands {
		if _, ok := seen[command]; ok {
			continue
		}
		seen[command] = struct{}{}
		commands = append(commands, command)
	}
	for command := range resourceRegistry {
		if _, ok := seen[command]; ok {
			continue
		}
		seen[command] = struct{}{}
		commands = append(commands, command)
	}
	for _, command := range harness.IDs() {
		if _, ok := seen[command]; ok {
			continue
		}
		seen[command] = struct{}{}
		commands = append(commands, command)
	}
	sort.Strings(commands)
	return commands
}

// quoteZshWords returns a safely quoted zsh array payload for static command names.
func quoteZshWords(words string) string {
	parts := strings.Fields(words)
	for index := range parts {
		parts[index] = "'" + strings.ReplaceAll(parts[index], "'", "'\\''") + "'"
	}
	return strings.Join(parts, " ")
}
