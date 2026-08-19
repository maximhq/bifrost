package tui

import (
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// confirmModel holds information about current model selection
type confirmModel struct {
	header     string
	prompt     string
	command    string
	idx        int
	quit       bool
	yes        bool
	clearFirst bool
	homeStyle  bool
	width      int
	height     int
}

// runConfirm runs a confirm dialog with the given model and returns the user's
// choice.
func runConfirm(m confirmModel, in io.Reader, out io.Writer) (bool, error) {
	opts := []tea.ProgramOption{
		tea.WithInput(in),
		tea.WithOutput(out),
	}
	// Only grab the controlling TTY when reading real stdin; injected streams
	// (tests, or the App's redirected streams) must not open /dev/tty.
	if in == os.Stdin {
		opts = append(opts, tea.WithInputTTY())
	}
	p := tea.NewProgram(m, opts...)
	final, err := p.Run()
	if err != nil {
		return false, err
	}
	fm, ok := final.(confirmModel)
	if !ok {
		return false, fmt.Errorf("unexpected model type from tui")
	}
	if fm.quit {
		return false, nil
	}
	return fm.yes, nil
}

// RunConfirmInstall displays a yes/no confirmation dialog asking the user
// whether to install a missing harness. Returns true if the user confirms.
func RunConfirmInstall(header, harnessLabel, command string) (bool, error) {
	return runConfirm(confirmModel{
		header:  header,
		prompt:  fmt.Sprintf("%s is not installed. Install now?", harnessLabel),
		command: command,
	}, os.Stdin, os.Stdout)
}

// RunConfirmSettings displays a yes/no confirmation dialog asking the user
// whether to update the harness's native settings file. Returns true if the
// user confirms.
func RunConfirmSettings(harnessLabel, settingsPath string) (bool, error) {
	return runConfirm(confirmModel{
		prompt:     fmt.Sprintf("Update %s settings? (%s)", harnessLabel, settingsPath),
		clearFirst: true,
	}, os.Stdin, os.Stdout)
}

// RunConfirmUpdate displays the mandatory update prompt shown before the main
// chooser when a newer Bifrost CLI version is available.
func RunConfirmUpdate(header, version string) (bool, error) {
	return RunConfirmUpdateIO(header, version, os.Stdin, os.Stdout)
}

// RunConfirmUpdateIO is the I/O-aware variant of RunConfirmUpdate. It renders
// the mandatory update prompt using the provided streams instead of the global
// os.Stdin/os.Stdout, so the App can pass its injected streams and tests can
// drive the prompt without a real terminal.
func RunConfirmUpdateIO(header, version string, in io.Reader, out io.Writer) (bool, error) {
	return runConfirm(confirmModel{
		header:     header,
		prompt:     fmt.Sprintf("Bifrost %s is available. Update now?", strings.TrimSpace(version)),
		idx:        1, // default to No so users can continue into Home.
		clearFirst: true,
		homeStyle:  true,
	}, in, out)
}

// Init implements tea.Model.
func (m confirmModel) Init() tea.Cmd {
	if m.clearFirst {
		return tea.ClearScreen
	}
	return nil
}

// Update implements tea.Model. Handles y/n, arrow keys, and enter for confirmation.
func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		s := msg.String()
		switch s {
		case "ctrl+c", "q", "esc":
			m.quit = true
			return m, tea.Quit
		case "left", "h", "right", "l", "tab":
			if m.idx == 0 {
				m.idx = 1
			} else {
				m.idx = 0
			}
			return m, nil
		case "y":
			m.yes = true
			return m, tea.Quit
		case "n":
			m.yes = false
			return m, tea.Quit
		case "enter":
			m.yes = m.idx == 0
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	}
	return m, nil
}

// View implements tea.Model. Renders the confirmation prompt with Yes/No buttons.
func (m confirmModel) View() string {
	selected := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	normal := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	yes := normal.Render("[ Yes ]")
	no := normal.Render("[ No ]")
	if m.idx == 0 {
		yes = selected.Render("[ Yes ]")
	} else {
		no = selected.Render("[ No ]")
	}

	if m.homeStyle {
		return m.homeStyleView(yes, no)
	}

	var b strings.Builder
	if m.header != "" {
		b.WriteString(m.header)
		b.WriteString("\n")
	}
	b.WriteString(m.prompt)
	b.WriteString("\n")
	if m.command != "" {
		b.WriteString("command: ")
		b.WriteString(m.command)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(yes + "  " + no)
	b.WriteString("\n")
	b.WriteString("enter: confirm, y/n quick choice, q: cancel")
	return b.String()
}

// homeStyleView renders confirmation as a centered Home-screen popup.
func (m confirmModel) homeStyleView(yes, no string) string {
	w := m.width
	if w == 0 {
		w = 80
	}
	h := m.height
	if h == 0 {
		h = 24
	}

	accent := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	boxWidth := 64
	if w > 0 && w < boxWidth+8 {
		boxWidth = w - 8
	}
	// Only enforce the 36-column minimum when the terminal can actually fit it;
	// in narrow tmux panes clamp to the available width instead of overflowing.
	if w >= 44 && boxWidth < 36 {
		boxWidth = 36
	}
	if boxWidth < 1 {
		boxWidth = 1
	}
	box := lipgloss.NewStyle().
		Width(boxWidth).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))

	var content strings.Builder
	content.WriteString(accent.Render("Update available"))
	content.WriteString("\n\n")
	content.WriteString(m.prompt)
	content.WriteString("\n\n")
	content.WriteString(yes + "  " + no)
	content.WriteString("\n")
	content.WriteString(hint.Render("enter: confirm  y/n: quick choice  q: cancel"))

	popup := box.Render(content.String())
	block := centerBlock(accent.Render("Bifrost CLI")+"\n"+hint.Render("Home")+"\n\n"+popup, w)
	lines := strings.Count(block, "\n") + 1
	topPad := (h - lines) / 2
	if topPad < 0 {
		topPad = 0
	}

	var out strings.Builder
	out.WriteString(strings.Repeat("\n", topPad))
	out.WriteString(block)
	return out.String()
}
