//go:build !windows

package runtime

import "strings"

// BuildCommandString renders argv as a POSIX-shell-safe helper command.
func BuildCommandString(arguments ...string) string {
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		if argument == "" {
			quoted = append(quoted, "''")
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(argument, "'", "'\\''")+"'")
	}
	return strings.Join(quoted, " ")
}
