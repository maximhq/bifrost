//go:build windows

package runtime

// BuildCommandString renders argv as an encoded PowerShell invocation so the
// system shell cannot reinterpret characters from the executable path.
func BuildCommandString(arguments ...string) string {
	return buildPowerShellCommand(arguments...)
}
