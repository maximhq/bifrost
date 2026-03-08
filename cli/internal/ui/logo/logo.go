package logo

import (
	"fmt"
	"strings"
)

const compact = "BIFROST CLI"

// Render returns the ASCII logo for the given terminal width.
func Render(width int) string {
	if width < 61 {
		return compact
	}

	return strings.Join([]string{
		"╔═══════════════════════════════════════════════════════════╗",
		"║                                                           ║",
		"║   ██████╗ ██╗███████╗██████╗  ██████╗ ███████╗████████╗   ║",
		"║   ██╔══██╗██║██╔════╝██╔══██╗██╔═══██╗██╔════╝╚══██╔══╝   ║",
		"║   ██████╔╝██║█████╗  ██████╔╝██║   ██║███████╗   ██║      ║",
		"║   ██╔══██╗██║██╔══╝  ██╔══██╗██║   ██║╚════██║   ██║      ║",
		"║   ██████╔╝██║██║     ██║  ██║╚██████╔╝███████║   ██║      ║",
		"║   ╚═════╝ ╚═╝╚═╝     ╚═╝  ╚═╝ ╚═════╝ ╚══════╝   ╚═╝      ║",
		"║                                                           ║",
		"║═══════════════════════════════════════════════════════════║",
		"║                          CLI                              ║",
		"║═══════════════════════════════════════════════════════════║",
		"║             https://github.com/maximhq/bifrost            ║",
		"╚═══════════════════════════════════════════════════════════╝",
	}, "\n")
}

// BootHeader builds the full boot header with the ASCII logo and version info.
func BootHeader(width int, version, commit, source string, noColor bool) string {
	if width < 61 {
		meta := fmt.Sprintf("%s (%s)", version, commit)
		return fmt.Sprintf("\n\n%s\n%s", Render(width), meta)
	}

	meta := fmt.Sprintf("%s (%s)  config=%s", version, commit, source)

	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(Render(width))
	b.WriteString("\n")
	if noColor {
		b.WriteString(meta)
	} else {
		b.WriteString("\033[2;36m" + meta + "\033[0m")
	}
	b.WriteString("\n")
	return b.String()
}
