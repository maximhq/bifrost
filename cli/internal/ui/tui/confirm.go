package tui

import (
	"fmt"
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
}

// runConfirm runs a confirm dialog with the given model and returns the user's
// choice.
func runConfirm(m confirmModel) (bool, error) {
	p := tea.NewProgram(
		m,
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
		tea.WithInputTTY(),
	)
	out, err := p.Run()
	if err != nil {
		return false, err
	}
	fm, ok := out.(confirmModel)
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
	})
}

// RunConfirmSettings displays a yes/no confirmation dialog asking the user
// whether to update the harness's native settings file. Returns true if the
// user confirms.
func RunConfirmSettings(harnessLabel, settingsPath string) (bool, error) {
	return runConfirm(confirmModel{
		prompt:     fmt.Sprintf("Update %s settings? (%s)", harnessLabel, settingsPath),
		clearFirst: true,
	})
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
