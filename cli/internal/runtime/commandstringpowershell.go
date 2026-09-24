package runtime

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

const powerShellCommandPrefix = "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand "

// buildPowerShellCommand keeps executable paths and arguments out of cmd.exe's
// parser. In particular, percent references are expanded by cmd.exe even inside
// quotes, and cmd /s /c strips a command-level quote pair before parsing. An
// encoded PowerShell script leaves cmd.exe only a constant command and Base64
// payload to process.
func buildPowerShellCommand(arguments ...string) string {
	if len(arguments) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted = append(quoted, "'"+strings.ReplaceAll(argument, "'", "''")+"'")
	}
	script := "& " + strings.Join(quoted, " ")
	codeUnits := utf16.Encode([]rune(script))
	bytes := make([]byte, len(codeUnits)*2)
	for index, codeUnit := range codeUnits {
		binary.LittleEndian.PutUint16(bytes[index*2:], codeUnit)
	}
	return powerShellCommandPrefix + base64.StdEncoding.EncodeToString(bytes)
}
