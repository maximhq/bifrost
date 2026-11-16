package runtime

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestBuildPowerShellCommandEncodesShellSensitiveArguments(t *testing.T) {
	arguments := []string{`C:\Program Files\O'Brien & %BIFROST_TEST% 100% Ready\bifrost.exe`, "auth", "print-token"}
	command := buildPowerShellCommand(arguments...)
	const prefix = "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand "
	if !strings.HasPrefix(command, prefix) {
		t.Fatalf("command = %q, want prefix %q", command, prefix)
	}
	if strings.Contains(command, arguments[0]) {
		t.Fatalf("command exposes shell-sensitive executable path: %q", command)
	}

	encoded := strings.TrimPrefix(command, prefix)
	bytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode command: %v", err)
	}
	if len(bytes)%2 != 0 {
		t.Fatalf("decoded command has odd UTF-16LE byte length %d", len(bytes))
	}
	codeUnits := make([]uint16, len(bytes)/2)
	for index := range codeUnits {
		codeUnits[index] = binary.LittleEndian.Uint16(bytes[index*2:])
	}
	gotScript := string(utf16.Decode(codeUnits))
	wantScript := `& 'C:\Program Files\O''Brien & %BIFROST_TEST% 100% Ready\bifrost.exe' 'auth' 'print-token'`
	if gotScript != wantScript {
		t.Fatalf("decoded script = %q, want %q", gotScript, wantScript)
	}
}

// TestBuildCommandStringExecutesPathWithShellMetacharacters exercises the
// rendered helper through cmd.exe, matching how Claude invokes apiKeyHelper.
// The path includes paired and unmatched percent characters so the test fails
// if cmd.exe receives the executable path and expands it before launch.
func TestBuildCommandStringExecutesPathWithShellMetacharacters(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("cmd.exe execution is only available on Windows")
	}
	directory := filepath.Join(t.TempDir(), "App & %BIFROST_HELPER_EXPANSION% 100% Ready")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(directory, "bifrost-helper.cmd")
	script := "@echo off\r\nif not \"%~1\"==\"auth\" exit /b 7\r\nif not \"%~2\"==\"print-token\" exit /b 8\r\necho ck-bf-agent-test\r\n"
	if err := os.WriteFile(helper, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", BuildCommandString(helper, "auth", "print-token"))
	command.Env = append(os.Environ(), "BIFROST_HELPER_EXPANSION=expanded")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("execute helper: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("execute helper: %v: %s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "ck-bf-agent-test" {
		t.Fatalf("helper output = %q, want token only", got)
	}
}
