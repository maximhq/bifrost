package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// RegisterAutoStart registers the agent to start automatically on login.
func RegisterAutoStart() error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return registerAutoStartDarwin(execPath)
	case "linux":
		return registerAutoStartLinux(execPath)
	case "windows":
		return registerAutoStartWindows(execPath)
	default:
		return fmt.Errorf("auto-start not supported on %s", runtime.GOOS)
	}
}

// UnregisterAutoStart removes the auto-start registration.
func UnregisterAutoStart() error {
	switch runtime.GOOS {
	case "darwin":
		return unregisterAutoStartDarwin()
	case "linux":
		return unregisterAutoStartLinux()
	case "windows":
		return unregisterAutoStartWindows()
	default:
		return fmt.Errorf("auto-start not supported on %s", runtime.GOOS)
	}
}

// IsAutoStartRegistered checks if auto-start is currently registered.
func IsAutoStartRegistered() bool {
	switch runtime.GOOS {
	case "darwin":
		return isAutoStartRegisteredDarwin()
	case "linux":
		return isAutoStartRegisteredLinux()
	case "windows":
		return isAutoStartRegisteredWindows()
	default:
		return false
	}
}

// --- macOS ---

const launchAgentLabel = "com.maxim.bifrost-agent"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func registerAutoStartDarwin(execPath string) error {
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
</dict>
</plist>
`, launchAgentLabel, execPath)

	dir := filepath.Dir(launchAgentPath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(launchAgentPath(), []byte(plist), 0644)
}

func unregisterAutoStartDarwin() error {
	return os.Remove(launchAgentPath())
}

func isAutoStartRegisteredDarwin() bool {
	_, err := os.Stat(launchAgentPath())
	return err == nil
}

// --- Linux ---

func desktopFilePath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "autostart", "bifrost-agent.desktop")
}

func registerAutoStartLinux(execPath string) error {
	desktop := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Bifrost Agent
Comment=AI API traffic proxy
Exec=%s
Terminal=false
Hidden=false
X-GNOME-Autostart-enabled=true
`, execPath)

	dir := filepath.Dir(desktopFilePath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(desktopFilePath(), []byte(desktop), 0644)
}

func unregisterAutoStartLinux() error {
	return os.Remove(desktopFilePath())
}

func isAutoStartRegisteredLinux() bool {
	_, err := os.Stat(desktopFilePath())
	return err == nil
}

// --- Windows ---

func registerAutoStartWindows(execPath string) error {
	// Use reg.exe to add to Run key
	return runRegCmd("ADD",
		`HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\Run`,
		"/v", "BifrostAgent",
		"/t", "REG_SZ",
		"/d", execPath,
		"/f")
}

func unregisterAutoStartWindows() error {
	return runRegCmd("DELETE",
		`HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\Run`,
		"/v", "BifrostAgent",
		"/f")
}

func isAutoStartRegisteredWindows() bool {
	err := runRegCmd("QUERY",
		`HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\Run`,
		"/v", "BifrostAgent")
	return err == nil
}

func runRegCmd(args ...string) error {
	cmd := append([]string{"reg"}, args...)
	_ = cmd
	// Use os/exec to run reg.exe
	// This is a stub on non-Windows; the build tag ensures only the right
	// platform file is compiled.
	return fmt.Errorf("not implemented on this platform")
}
