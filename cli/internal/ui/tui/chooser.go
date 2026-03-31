package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	textInput "github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maximhq/bifrost/cli/internal/ui/logo"
)

const issuesURL = "https://github.com/maximhq/bifrost/issues/new"
const repoURL = "https://github.com/maximhq/bifrost"
const docsURL = "https://docs.getbifrost.ai/quickstart/cli/getting-started"

// HarnessOption represents a selectable coding harness (e.g. Claude Code, Codex)
// with its installation status.
type HarnessOption struct {
	ID                    string
	Label                 string
	Version               string
	Installed             bool
	SupportsWorktree      bool
	SupportsModelOverride bool // true when the harness accepts an external model setting
}

// ChooserConfig holds the initial values and callbacks for the interactive chooser TUI.
type ChooserConfig struct {
	Version       string
	Commit        string
	ConfigSrc     string
	Message       string
	UpdateVersion string
	BaseURL       string
	VirtualKey    string
	Harness       string
	Model         string
	Worktree      string
	Harnesses     []HarnessOption
	AfterSession  bool // true when returning from a harness session; blocks input until ready
	ReservedRows  int              // rows reserved by the tab bar; subtracted from the available height
	TabBarLine    func() string    // returns the current tab bar content; rendered as the last line
	FetchModels   func(ctx context.Context, baseURL, virtualKey string) ([]string, error)
	Notify        func(message string, isError bool)
	Input         io.Reader // optional stdin override; when nil, os.Stdin is used
}

// ChooserResult holds the user's selections after the chooser TUI completes.
type ChooserResult struct {
	Quit            bool
	BackToTabs      bool // true when the user pressed Ctrl+B to return to tab command mode
	UpdateRequested bool
	InstallHarness  bool // true when user selected a harness that needs installation
	BaseURL         string
	VirtualKey      string
	Harness         string
	Model           string
	Worktree        string
}

type chooserPhase int

const (
	phaseBaseURL chooserPhase = iota
	phaseVirtualKey
	phaseHarness
	phaseModel
	phaseWorktree
	phaseSummary
)

type modelsMsg struct {
	models []string
	err    error
}

type warmupDoneMsg struct{}

type chooserModel struct {
	cfg ChooserConfig

	phase           chooserPhase
	quit            bool
	backToTabs      bool
	done            bool
	installHarness  bool
	updateRequested bool
	returnToSummary bool

	width  int
	height int

	baseInput     textInput.Model
	vkInput       textInput.Model
	worktreeInput textInput.Model

	harnessIdx int
	modelIdx   int
	models     []string

	filterInput textInput.Model
	filtered    []int // indices into models
	loading     bool
	loadErr     string

	message string
	warming bool // true while ignoring input after session ended

	plainLayout bool // conservative layout for terminals with flaky full-screen rendering
}

// RunChooser launches the interactive multi-phase chooser TUI. It walks the user
// through selecting a base URL, virtual key, harness, and model, then returns
// the collected selections. Returns ChooserResult with Quit=true if the user aborts.
func RunChooser(cfg ChooserConfig) (ChooserResult, error) {
	m := newChooserModel(cfg)
	input := cfg.Input
	if input == nil {
		input = os.Stdin
	}
	p := tea.NewProgram(
		m,
		tea.WithInput(input),
		tea.WithOutput(os.Stdout),
	)
	final, err := p.Run()
	if err != nil {
		return ChooserResult{}, err
	}
	fm, ok := final.(chooserModel)
	if !ok {
		return ChooserResult{}, fmt.Errorf("unexpected model type from TUI")
	}
	if fm.backToTabs {
		return ChooserResult{BackToTabs: true}, nil
	}
	if fm.updateRequested {
		return ChooserResult{UpdateRequested: true}, nil
	}
	if fm.quit {
		return ChooserResult{Quit: true}, nil
	}
	return ChooserResult{
		InstallHarness: fm.installHarness,
		BaseURL:        strings.TrimSpace(fm.baseInput.Value()),
		VirtualKey:     strings.TrimSpace(fm.vkInput.Value()),
		Harness:        fm.currentHarness().ID,
		Model:          strings.TrimSpace(fm.currentModel()),
		Worktree:       strings.TrimSpace(fm.worktreeInput.Value()),
	}, nil
}

// newChooserModel initializes the chooser BubbleTea model with text inputs
// and pre-populates fields from the config. Skips completed phases when
// values are already provided.
func newChooserModel(cfg ChooserConfig) chooserModel {
	base := textInput.New()
	base.Placeholder = "http://localhost:8080"
	base.Prompt = ""
	base.SetValue(strings.TrimSpace(cfg.BaseURL))
	base.Focus()
	base.CharLimit = 512

	vk := textInput.New()
	vk.Placeholder = "optional (x-bf-vk)"
	vk.Prompt = ""
	vk.SetValue(strings.TrimSpace(cfg.VirtualKey))
	vk.Blur()
	vk.CharLimit = 512

	wt := textInput.New()
	wt.Placeholder = "optional worktree name"
	wt.Prompt = ""
	wt.SetValue(strings.TrimSpace(cfg.Worktree))
	wt.Blur()
	wt.CharLimit = 256

	filter := textInput.New()
	filter.Placeholder = "type to search models..."
	filter.Prompt = "> "
	filter.Blur()
	filter.CharLimit = 128

	hIdx := 0
	for i, h := range cfg.Harnesses {
		if h.ID == strings.TrimSpace(cfg.Harness) {
			hIdx = i
			break
		}
	}

	m := chooserModel{
		cfg:           cfg,
		phase:         phaseBaseURL,
		baseInput:     base,
		vkInput:       vk,
		worktreeInput: wt,
		harnessIdx:    hIdx,
		filterInput:   filter,
		message:       strings.TrimSpace(cfg.Message),
		plainLayout:   prefersPlainChooserLayout(),
	}

	if strings.TrimSpace(cfg.BaseURL) != "" {
		m.phase = phaseHarness
		m.baseInput.Blur()
	}
	if strings.TrimSpace(cfg.Harness) != "" && strings.TrimSpace(cfg.Model) != "" && strings.TrimSpace(cfg.BaseURL) != "" {
		m.phase = phaseSummary
		m.models = []string{strings.TrimSpace(cfg.Model)}
		m.modelIdx = 0
	}

	if cfg.AfterSession {
		m.warming = true
	}

	return m
}

// Init implements tea.Model.
func (m chooserModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	if msg := strings.TrimSpace(m.message); msg != "" && m.cfg.Notify != nil {
		cmds = append(cmds, func() tea.Msg {
			m.cfg.Notify(msg, false)
			return nil
		})
	}
	if m.warming {
		cmds = append(cmds, tea.Tick(10*time.Millisecond, func(time.Time) tea.Msg {
			return warmupDoneMsg{}
		}))
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model. It handles keyboard input for all chooser phases:
// base URL entry, virtual key entry, harness selection, model search/selection,
// worktree name entry, and launch summary.
func (m chooserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case warmupDoneMsg:
		m.warming = false
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height - m.cfg.ReservedRows
		if m.height < 10 {
			m.height = 10
		}
		return m, tea.ClearScreen

	case tea.KeyMsg:
		if m.warming {
			return m, nil
		}
		s := msg.String()
		if s == "ctrl+c" {
			m.quit = true
			return m, tea.Quit
		}
		if s == "ctrl+b" {
			m.backToTabs = true
			return m, tea.Quit
		}
		canTriggerUpdate := strings.TrimSpace(m.cfg.UpdateVersion) != "" &&
			m.phase != phaseBaseURL &&
			m.phase != phaseVirtualKey &&
			m.phase != phaseModel &&
			m.phase != phaseWorktree
		if canTriggerUpdate && (s == "y" || s == "Y") {
			m.updateRequested = true
			return m, tea.Quit
		}
		// Only handle 'q' as quit when not in a text input phase
		if s == "q" && m.phase != phaseBaseURL && m.phase != phaseVirtualKey && m.phase != phaseModel && m.phase != phaseWorktree {
			m.quit = true
			return m, tea.Quit
		}

		switch m.phase {
		case phaseBaseURL:
			if s == "enter" {
				if strings.TrimSpace(m.baseInput.Value()) == "" {
					m.notify("base URL is required", true)
					return m, nil
				}
				m.baseInput.Blur()
				if m.returnToSummary {
					m.returnToSummary = false
					m.phase = phaseSummary
					return m, nil
				}
				m.phase = phaseVirtualKey
				m.vkInput.Focus()
				return m, nil
			}
			if s == "esc" && m.returnToSummary {
				m.returnToSummary = false
				m.baseInput.Blur()
				m.phase = phaseSummary
				return m, nil
			}
			var cmd tea.Cmd
			m.baseInput, cmd = m.baseInput.Update(msg)
			return m, cmd

		case phaseVirtualKey:
			if s == "enter" {
				m.vkInput.Blur()
				if m.returnToSummary {
					m.returnToSummary = false
					m.phase = phaseSummary
					return m, nil
				}
				m.phase = phaseHarness
				return m, nil
			}
			if s == "esc" {
				m.vkInput.Blur()
				if m.returnToSummary {
					m.returnToSummary = false
					m.phase = phaseSummary
					return m, nil
				}
				m.phase = phaseBaseURL
				m.baseInput.Focus()
				return m, nil
			}
			if s == "f1" {
				baseURL := strings.TrimSpace(m.baseInput.Value())
				if baseURL != "" {
					openBrowser(baseURL)
					m.notify("opened bifrost dashboard", false)
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.vkInput, cmd = m.vkInput.Update(msg)
			return m, cmd

		case phaseHarness:
			if s == "up" || s == "k" {
				if m.harnessIdx > 0 {
					m.harnessIdx--
				}
				return m, nil
			}
			if s == "down" || s == "j" {
				if m.harnessIdx < len(m.cfg.Harnesses)-1 {
					m.harnessIdx++
				}
				return m, nil
			}
			if s == "enter" {
				selected := m.currentHarness()
				if !selected.Installed {
					m.installHarness = true
					return m, tea.Quit
				}
				if m.returnToSummary {
					m.returnToSummary = false
					m.phase = phaseSummary
					return m, nil
				}
				m.phase = phaseModel
				m.loading = true
				m.loadErr = ""
				m.models = nil
				m.filtered = nil
				m.filterInput.SetValue("")
				return m, m.fetchModelsCmd()
			}
			if s == "esc" {
				if m.returnToSummary {
					m.returnToSummary = false
					m.phase = phaseSummary
					return m, nil
				}
				m.phase = phaseVirtualKey
				m.vkInput.Focus()
				return m, nil
			}

		case phaseModel:
			if m.loading {
				if s == "esc" {
					if m.returnToSummary {
						m.returnToSummary = false
						m.phase = phaseSummary
						return m, nil
					}
					m.phase = phaseHarness
					return m, nil
				}
				return m, nil
			}

			if s == "esc" {
				m.filterInput.SetValue("")
				m.filterInput.Blur()
				m.filtered = nil
				if m.returnToSummary {
					m.returnToSummary = false
					m.phase = phaseSummary
					return m, nil
				}
				m.phase = phaseHarness
				return m, nil
			}

			if s == "up" {
				visible := m.visibleModels()
				if m.modelIdx > 0 {
					m.modelIdx--
				} else if len(visible) > 0 {
					m.modelIdx = len(visible) - 1
				}
				return m, nil
			}
			if s == "down" {
				visible := m.visibleModels()
				if m.modelIdx < len(visible)-1 {
					m.modelIdx++
				} else {
					m.modelIdx = 0
				}
				return m, nil
			}

			if s == "enter" {
				model := m.currentModel()
				if model == "" {
					// If filter text is non-empty, use it as manual model name
					ft := strings.TrimSpace(m.filterInput.Value())
					if ft != "" {
						model = ft
					} else {
						m.notify("select a model", true)
						return m, nil
					}
				}
				// Pin the selected model so the summary always shows it correctly
				m.models = []string{model}
				m.modelIdx = 0
				m.filtered = nil
				m.filterInput.SetValue("")
				m.filterInput.Blur()
				m.phase = phaseSummary
				return m, nil
			}

			// All other keys go to the filter input
			var cmd tea.Cmd
			m.filterInput, cmd = m.filterInput.Update(msg)
			query := strings.ToLower(strings.TrimSpace(m.filterInput.Value()))
			if query == "" {
				m.filtered = nil
			} else {
				terms := strings.Fields(query)
				var indices []int
				for i, model := range m.models {
					lower := strings.ToLower(model)
					match := true
					for _, t := range terms {
						if !strings.Contains(lower, t) {
							match = false
							break
						}
					}
					if match {
						indices = append(indices, i)
					}
				}
				m.filtered = indices
			}
			m.modelIdx = 0
			return m, cmd

		case phaseWorktree:
			if s == "enter" {
				m.worktreeInput.Blur()
				m.returnToSummary = false
				m.phase = phaseSummary
				return m, nil
			}
			if s == "esc" {
				m.worktreeInput.Blur()
				m.returnToSummary = false
				m.phase = phaseSummary
				return m, nil
			}
			var cmd tea.Cmd
			m.worktreeInput, cmd = m.worktreeInput.Update(msg)
			return m, cmd

		case phaseSummary:
			switch s {
			case "enter":
				m.done = true
				return m, tea.Quit
			case "u":
				m.phase = phaseBaseURL
				m.returnToSummary = true
				m.baseInput.Focus()
				return m, nil
			case "v":
				m.phase = phaseVirtualKey
				m.returnToSummary = true
				m.vkInput.Focus()
				return m, nil
			case "w":
				if m.currentHarness().SupportsWorktree {
					m.phase = phaseWorktree
					m.returnToSummary = true
					m.worktreeInput.Focus()
					return m, nil
				}
			case "h":
				m.phase = phaseHarness
				m.returnToSummary = true
				return m, nil
			case "m":
				if !m.currentHarness().SupportsModelOverride {
					m.notify(m.currentHarness().Label+" manages its own model selection", true)
					return m, nil
				}
				m.phase = phaseModel
				m.returnToSummary = true
				m.loading = true
				return m, m.fetchModelsCmd()
			case "d":
				baseURL := strings.TrimSpace(m.baseInput.Value())
				if baseURL != "" {
					openBrowser(baseURL)
					m.notify("opened bifrost dashboard", false)
				}
				return m, nil
			case "r":
				openBrowser(docsURL)
				m.notify("opened docs", false)
				return m, nil
			case "i":
				openBrowser(issuesURL)
				m.notify("opened GitHub issues", false)
				return m, nil
			case "s":
				openBrowser(repoURL)
				m.notify("opened GitHub repo", false)
				return m, nil
			case "esc":
				m.quit = true
				return m, tea.Quit
			}
		}

	case modelsMsg:
		m.loading = false
		if msg.err != nil {
			m.loadErr = msg.err.Error()
			m.notify(m.loadErr, true)
			m.models = nil
		} else {
			m.models = msg.models
			if len(m.models) == 0 {
				m.loadErr = "no models found \u2014 type a model name manually"
				m.notify(m.loadErr, false)
			}
		}
		m.modelIdx = 0
		m.filtered = nil
		m.filterInput.SetValue("")
		m.filterInput.Focus()
	}

	return m, nil
}

// View implements tea.Model. It renders the current phase of the chooser TUI.
func (m chooserModel) View() string {
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	accent := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("28"))
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	logoColor := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	w := m.width
	if w == 0 {
		w = 80
	}
	h := m.height
	if h == 0 {
		h = 24
	}

	// Build logo block (gray/white)
	logoBlock := logoColor.Render(logo.Render(w))

	// Meta line
	meta := hint.Render(fmt.Sprintf("%s (%s)  config=%s", m.cfg.Version, m.cfg.Commit, m.cfg.ConfigSrc))

	// Build the phase content as two parts: a centered title and a left-aligned body
	var title string
	var body strings.Builder
	var footer string

	if m.message != "" && m.cfg.Notify == nil {
		body.WriteString(hint.Render(m.message))
		body.WriteString("\n\n")
	}
	if updateVersion := strings.TrimSpace(m.cfg.UpdateVersion); updateVersion != "" {
		body.WriteString(accent.Render("Update available: "))
		body.WriteString(hint.Render("bifrost " + updateVersion + "  "))
		body.WriteString(accent.Render("press y to update now"))
		body.WriteString("\n\n")
	}
	if m.loadErr != "" && m.cfg.Notify == nil {
		body.WriteString(errorStyle.Render(m.loadErr))
		body.WriteString("\n\n")
	}

	switch m.phase {
	case phaseBaseURL:
		title = accent.Render("Base URL (Bifrost Base URL)")
		body.WriteString(m.baseInput.View())
		if m.returnToSummary {
			footer = hint.Render("enter: update  esc: cancel")
		} else {
			footer = hint.Render("enter: continue  ctrl+c: quit")
		}

	case phaseVirtualKey:
		title = accent.Render("Virtual Key") + label.Render(" (optional)")
		body.WriteString(m.vkInput.View())
		if m.returnToSummary {
			footer = hint.Render("enter: update  esc: cancel  f1: open dashboard")
		} else {
			footer = hint.Render("enter: continue  esc: back  f1: open dashboard")
		}

	case phaseHarness:
		title = accent.Render("Choose Harness")
		body.WriteString("\n")
		for i, ho := range m.cfg.Harnesses {
			cursor := "  "
			style := label
			if i == m.harnessIdx {
				cursor = accent.Render("> ")
				style = lipgloss.NewStyle().Bold(true)
			}
			status := cyan.Render("installed")
			if !ho.Installed {
				status = hint.Render("not installed")
			}
			ver := ""
			if ho.Version != "" {
				ver = hint.Render(" " + ho.Version)
			}
			fmt.Fprintf(&body, "%s%s%s  %s\n", cursor, style.Render(ho.Label), ver, status)
		}
		if m.returnToSummary {
			footer = hint.Render("up/down: move  enter: select  esc: cancel")
		} else {
			footer = hint.Render("up/down: move  enter: select  esc: back")
		}

	case phaseModel:
		title = accent.Render("Model")
		if m.loading {
			body.WriteString(hint.Render("loading models from /v1/models..."))
			footer = hint.Render("esc: back")
		} else {
			body.WriteString(m.filterInput.View())
			body.WriteString("\n\n")

			visible := m.visibleModels()
			maxShow := 12
			if len(visible) == 0 {
				ft := strings.TrimSpace(m.filterInput.Value())
				if ft != "" {
					body.WriteString(hint.Render("  no matches \u2014 enter to use as model name"))
				} else {
					body.WriteString(hint.Render("  type to filter models"))
				}
			} else {
				start, end := scrollWindow(m.modelIdx, len(visible), maxShow)
				if start > 0 {
					body.WriteString(hint.Render(fmt.Sprintf("  ... %d more above", start)))
					body.WriteString("\n")
				}
				for i := start; i < end; i++ {
					if i == m.modelIdx {
						body.WriteString(accent.Render("> " + visible[i]))
						body.WriteString("\n")
					} else {
						body.WriteString("  " + visible[i] + "\n")
					}
				}
				if end < len(visible) {
					body.WriteString(hint.Render(fmt.Sprintf("  ... %d more below", len(visible)-end)))
					body.WriteString("\n")
				}
				body.WriteString("\n")
				body.WriteString(hint.Render(fmt.Sprintf("  %d/%d models", len(visible), len(m.models))))
			}
			if m.returnToSummary {
				footer = hint.Render("type: filter  up/down: move  enter: select  esc: cancel")
			} else {
				footer = hint.Render("type: filter  up/down: move  enter: select  esc: back")
			}
		}

	case phaseWorktree:
		title = accent.Render("Worktree") + label.Render(" (optional)")
		body.WriteString(m.worktreeInput.View())
		footer = hint.Render("enter: update  esc: cancel")

	case phaseSummary:
		ho := m.currentHarness()
		vkState := "no"
		if strings.TrimSpace(m.vkInput.Value()) != "" {
			vkState = "yes"
		}
		baseURL := strings.TrimSpace(m.baseInput.Value())
		model := m.currentModel()
		harnessStr := ho.Label
		if ho.Version != "" {
			harnessStr += " (" + ho.Version + ")"
		}

		title = accent.Render("Ready to launch")

		body.WriteString(label.Render("  Base URL     ") + " " + baseURL + "\n")
		body.WriteString(label.Render("  Harness      ") + " " + harnessStr + "\n")
		if ho.SupportsModelOverride {
			body.WriteString(label.Render("  Model        ") + " " + accent.Render(model) + "\n")
		} else {
			body.WriteString(label.Render("  Model        ") + " " + hint.Render("managed by "+ho.Label) + "\n")
		}
		if ho.ID == "claude" && strings.Contains(strings.ToLower(model), "gemini") {
			warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
			body.WriteString(label.Render("               ") + " " + warnStyle.Render("⚠ Gemini function calling is not compatible with") + "\n")
			body.WriteString(label.Render("               ") + " " + warnStyle.Render("  Claude Code and may not work as intended") + "\n")
		}
		body.WriteString(label.Render("  Virtual Key  ") + " " + vkState + "\n")
		if ho.SupportsWorktree {
			wtState := "no"
			if wt := strings.TrimSpace(m.worktreeInput.Value()); wt != "" {
				wtState = wt
			}
			body.WriteString(label.Render("  Worktree     ") + " " + wtState + "\n")
		}

		var fb strings.Builder
		fb.WriteString(accent.Render("enter") + hint.Render(" launch  "))
		fb.WriteString(accent.Render("u") + hint.Render(" url  "))
		fb.WriteString(accent.Render("v") + hint.Render(" virtual key  "))
		if ho.SupportsWorktree {
			fb.WriteString(accent.Render("w") + hint.Render(" worktree  "))
		}
		fb.WriteString(accent.Render("h") + hint.Render(" harness  "))
		if ho.SupportsModelOverride {
			fb.WriteString(accent.Render("m") + hint.Render(" model  "))
		}
		fb.WriteString(accent.Render("d") + hint.Render(" dashboard  "))
		fb.WriteString(accent.Render("r") + hint.Render(" docs  "))
		fb.WriteString(accent.Render("i") + hint.Render(" report issue  "))
		fb.WriteString(accent.Render("s") + hint.Render(" star  "))
		fb.WriteString(accent.Render("q") + hint.Render(" quit"))
		footer = fb.String()
	}

	// Compose: vertically center logo+content, footer at bottom
	bodyStr := body.String()

	// Center body: per-line for input phases, block-aligned for harness/summary
	var alignedBody string
	switch m.phase {
	case phaseBaseURL, phaseVirtualKey, phaseWorktree, phaseModel:
		alignedBody = centerBlock(bodyStr, w)
	default:
		alignedBody = centerBlockLeft(bodyStr, w)
	}

	// Combine title (if any) + body into content
	var content strings.Builder
	if title != "" {
		content.WriteString(centerLine(title, w))
		content.WriteString("\n\n")
	}
	content.WriteString(alignedBody)
	contentStr := content.String()

	if m.plainLayout {
		return renderPlainChooserView(title, bodyStr, footer)
	}

	logoLines := strings.Count(logoBlock, "\n") + 1
	contentLines := strings.Count(contentStr, "\n") + 1

	// Calculate how many lines the footer will occupy after wrapping
	footerLines := 1
	if lipgloss.Width(footer) > w {
		footerLines = strings.Count(wrapFooter(footer, w), "\n") + 1
	}

	// Actual rendered lines between topPad and bottomPad:
	//   logoLines + 1 (meta) + 1 (blank gap line) + contentLines
	bodyHeight := logoLines + 2 + contentLines
	topPad := (h - bodyHeight - footerLines) / 2
	if topPad < 0 {
		topPad = 0
	}
	bottomPad := h - topPad - bodyHeight - footerLines
	if bottomPad < 1 {
		bottomPad = 1
	}

	centeredLogo := centerBlock(logoBlock, w)
	centeredMeta := centerLine(meta, w)

	var out strings.Builder
	if topPad > 0 {
		out.WriteString(strings.Repeat("\n", topPad))
	}
	out.WriteString(centeredLogo)
	out.WriteString("\n")
	out.WriteString(centeredMeta)
	out.WriteString("\n\n")
	out.WriteString(contentStr)
	// N newlines between two text blocks produce N-1 visible blank lines,
	// so emit bottomPad+1 to get exactly bottomPad blank lines.
	out.WriteString(strings.Repeat("\n", bottomPad+1))
	// Wrap footer into multiple centered lines if it exceeds terminal width
	if lipgloss.Width(footer) > w {
		out.WriteString(wrapFooter(footer, w))
	} else {
		out.WriteString(centerLine(footer, w))
	}

	// Append the tab bar so it is part of Bubble Tea's render and survives
	// screen clears on resize.
	if m.cfg.TabBarLine != nil {
		out.WriteString("\n\n")
		out.WriteString(m.cfg.TabBarLine())
	}

	return out.String()
}

func prefersPlainChooserLayout() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TERM_PROGRAM")), "Apple_Terminal")
}

func renderPlainChooserView(title, body, footer string) string {
	var out strings.Builder

	out.WriteString("BIFROST CLI\n\n")
	if title != "" {
		out.WriteString(title)
		out.WriteString("\n\n")
	}

	trimmedBody := strings.TrimRight(body, "\n")
	if trimmedBody != "" {
		out.WriteString(trimmedBody)
		out.WriteString("\n")
	}

	if footer != "" {
		out.WriteString("\n")
		out.WriteString(strings.TrimSpace(footer))
	}

	return out.String()
}

func (m *chooserModel) notify(message string, isError bool) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if m.cfg.Notify != nil {
		m.cfg.Notify(message, isError)
		return
	}
	m.message = message
}

// centerBlock centers each line of a multi-line string within the given width.
func centerBlock(block string, width int) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		lines[i] = centerLine(line, width)
	}
	return strings.Join(lines, "\n")
}

// centerBlockLeft centers a multi-line block as a whole: it finds the widest
// line, calculates padding to center that width, then applies the same padding
// to every line so the block stays left-aligned internally.
func centerBlockLeft(block string, width int) string {
	lines := strings.Split(block, "\n")
	maxW := 0
	for _, line := range lines {
		if vw := lipgloss.Width(line); vw > maxW {
			maxW = vw
		}
	}
	if maxW >= width {
		return block
	}
	pad := strings.Repeat(" ", (width-maxW)/2)
	for i, line := range lines {
		lines[i] = pad + line
	}
	return strings.Join(lines, "\n")
}

// centerLine pads a single line with leading spaces to center it within width.
func centerLine(line string, width int) string {
	visible := lipgloss.Width(line)
	if visible >= width {
		return line
	}
	pad := (width - visible) / 2
	return strings.Repeat(" ", pad) + line
}

// currentHarness returns the currently selected harness option.
func (m chooserModel) currentHarness() HarnessOption {
	if len(m.cfg.Harnesses) == 0 {
		return HarnessOption{}
	}
	if m.harnessIdx < 0 {
		return m.cfg.Harnesses[0]
	}
	if m.harnessIdx >= len(m.cfg.Harnesses) {
		return m.cfg.Harnesses[len(m.cfg.Harnesses)-1]
	}
	return m.cfg.Harnesses[m.harnessIdx]
}

// currentModel returns the currently selected model name from the visible
// (possibly filtered) model list. Returns empty string if no model is selected.
func (m chooserModel) currentModel() string {
	visible := m.visibleModels()
	if len(visible) == 0 {
		return ""
	}
	if m.modelIdx < 0 || m.modelIdx >= len(visible) {
		return ""
	}
	return strings.TrimSpace(visible[m.modelIdx])
}

// visibleModels returns the model list to display. If a filter is active,
// returns only the matching subset; otherwise returns all models.
func (m chooserModel) visibleModels() []string {
	if m.filtered != nil {
		out := make([]string, 0, len(m.filtered))
		for _, idx := range m.filtered {
			if idx < 0 || idx >= len(m.models) {
				continue
			}
			out = append(out, m.models[idx])
		}
		return out
	}
	return m.models
}

// scrollWindow calculates the visible range [start, end) for a scrollable list,
// keeping the cursor centered within the window when possible.
func scrollWindow(cursor, total, maxVisible int) (start, end int) {
	if total <= maxVisible {
		return 0, total
	}
	half := maxVisible / 2
	start = cursor - half
	if start < 0 {
		start = 0
	}
	end = start + maxVisible
	if end > total {
		end = total
		start = end - maxVisible
	}
	return start, end
}

// wrapFooter splits a footer string into multiple centered lines so it fits
// within the given width. It splits at double-space boundaries between items.
func wrapFooter(footer string, width int) string {
	// Split on double-space which separates footer items
	parts := strings.Split(footer, "  ")
	var lines []string
	current := ""
	for _, p := range parts {
		candidate := current
		if candidate != "" {
			candidate += "  "
		}
		candidate += p
		if lipgloss.Width(candidate) > width && current != "" {
			lines = append(lines, centerLine(strings.TrimSpace(current), width))
			current = p
		} else {
			current = candidate
		}
	}
	if strings.TrimSpace(current) != "" {
		lines = append(lines, centerLine(strings.TrimSpace(current), width))
	}
	return strings.Join(lines, "\n")
}

// openBrowser opens the given URL in the user's default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// fetchModelsCmd returns a tea.Cmd that asynchronously fetches available models
// from the Bifrost API and sends the result as a modelsMsg.
func (m chooserModel) fetchModelsCmd() tea.Cmd {
	baseURL := strings.TrimSpace(m.baseInput.Value())
	vk := strings.TrimSpace(m.vkInput.Value())
	fetch := m.cfg.FetchModels
	return func() tea.Msg {
		if fetch == nil {
			return modelsMsg{err: fmt.Errorf("model fetcher is not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		models, err := fetch(ctx, baseURL, vk)
		return modelsMsg{models: models, err: err}
	}
}
