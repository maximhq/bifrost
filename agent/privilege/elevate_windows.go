package privilege

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// elevate uses ShellExecute with "runas" verb to trigger UAC on Windows.
func elevate(execPath string, args ...string) error {
	argStr := strings.Join(args, " ")

	// Use cmd /C to run the command elevated via PowerShell Start-Process
	psCmd := fmt.Sprintf(`Start-Process -FilePath '%s' -ArgumentList '%s' -Verb RunAs -Wait`,
		execPath, argStr)

	cmd := exec.Command("powershell", "-Command", psCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("elevation failed: %w: %s", err, string(out))
	}
	return nil
}
