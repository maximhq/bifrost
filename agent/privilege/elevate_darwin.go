package privilege

import (
	"fmt"
	"os/exec"
	"strings"
)

// elevate uses osascript to request admin privileges on macOS.
// This triggers the standard macOS "wants to make changes" authorization dialog.
func elevate(execPath string, args ...string) error {
	// Build the shell command with proper escaping
	quotedArgs := make([]string, len(args))
	for i, arg := range args {
		quotedArgs[i] = shellQuote(arg)
	}
	shellCmd := shellQuote(execPath) + " " + strings.Join(quotedArgs, " ")

	// Use osascript to run with administrator privileges
	script := fmt.Sprintf(`do shell script "%s" with administrator privileges`, escapeAppleScript(shellCmd))
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("elevation failed: %w: %s", err, string(out))
	}
	return nil
}

// shellQuote wraps a string in single quotes for shell safety.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// escapeAppleScript escapes a string for use inside an AppleScript double-quoted string.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}
