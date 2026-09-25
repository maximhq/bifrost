package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPrefersPlainChooserLayoutAppleTerminal(t *testing.T) {
	old := os.Getenv("TERM_PROGRAM")
	t.Cleanup(func() {
		if old == "" {
			os.Unsetenv("TERM_PROGRAM")
			return
		}
		os.Setenv("TERM_PROGRAM", old)
	})

	os.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if !prefersPlainChooserLayout() {
		t.Fatal("expected Apple Terminal to use the plain chooser layout")
	}

	os.Setenv("TERM_PROGRAM", "iTerm.app")
	if prefersPlainChooserLayout() {
		t.Fatal("did not expect iTerm to use the plain chooser layout")
	}
}

func TestRenderPlainChooserView(t *testing.T) {
	out := renderPlainChooserView("Ready", "base url\nmodel", "enter launch")

	for _, want := range []string{"BIFROST CLI", "Ready", "base url", "model", "enter launch"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got %q", want, out)
		}
	}
}

func TestChooserViewShowsUpdatePrompt(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		Version:       "v1.0.0",
		Commit:        "abc123",
		ConfigSrc:     "test",
		UpdateVersion: "v1.2.3",
	})

	view := m.View()

	for _, want := range []string{"Update available:", "bifrost v1.2.3", "press y to update now"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected chooser view to contain %q, got %q", want, view)
		}
	}
}

func TestChooserUpdateShortcutRequestsUpdate(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		UpdateVersion: "v1.2.3",
	})
	// Move to a non-text-entry phase so 'y' isn't consumed by the input field.
	m.phase = phaseSummary

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	got := next.(chooserModel)

	if !got.updateRequested {
		t.Fatal("expected y to request update when update is available")
	}
}

func TestChooserSummaryArrowEnterOpensSelectedOption(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL: "http://localhost:8080",
		Harness: "codex",
		Model:   "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID:                    "codex",
			Label:                 "Codex CLI",
			Installed:             true,
			SupportsModelOverride: true,
		}},
	})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(chooserModel)
	if got.phase != phaseSummary || !got.summaryEditing {
		t.Fatalf("expected enter on first summary row to edit base URL inline, phase=%v summaryEditing=%v", got.phase, got.summaryEditing)
	}

	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-edited")})
	got = next.(chooserModel)
	if !strings.Contains(got.baseInput.Value(), "-edited") {
		t.Fatalf("expected base URL input to update inline, got %q", got.baseInput.Value())
	}

	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = next.(chooserModel)
	if got.summaryEditing || got.baseInput.Value() != "http://localhost:8080" {
		t.Fatalf("expected esc to cancel inline edit, editing=%v baseURL=%q", got.summaryEditing, got.baseInput.Value())
	}

	m = newChooserModel(ChooserConfig{
		BaseURL: "http://localhost:8080",
		Harness: "codex",
		Model:   "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID:                    "codex",
			Label:                 "Codex CLI",
			Installed:             true,
			SupportsModelOverride: true,
		}},
		FetchModels: func(ctx context.Context, baseURL, virtualKey string) ([]string, error) {
			return []string{"gpt-4o-mini"}, nil
		},
	})
	for i := 0; i < 2; i++ {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(chooserModel)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = next.(chooserModel)
	if got.phase != phaseModel || !got.returnToSummary || !got.loading || cmd == nil {
		t.Fatalf("expected enter on model row to fetch models, phase=%v returnToSummary=%v loading=%v cmdNil=%v", got.phase, got.returnToSummary, got.loading, cmd == nil)
	}
}

func TestChooserModelManualEntryIsSelectable(t *testing.T) {
	m := newChooserModel(ChooserConfig{})
	m.phase = phaseModel
	m.models = []string{"claude-sonnet-4", "gpt-4o-mini"}
	m.filterInput.SetValue("custom-model")
	m.filterInput.Focus()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(chooserModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(chooserModel)

	if !m.modelManualSelected {
		t.Fatal("expected down navigation to select the manual model entry")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(chooserModel)
	if got.phase != phaseSummary || got.currentModel() != "custom-model" {
		t.Fatalf("expected manual model to be selected, phase=%v model=%q", got.phase, got.currentModel())
	}
}

func TestChooserModelPickerRendersSearchInputAboveResults(t *testing.T) {
	m := newChooserModel(ChooserConfig{})
	m.phase = phaseModel
	m.models = []string{"claude-sonnet-4", "gpt-4o-mini"}
	m.filterInput.SetValue("claude")

	view := m.View()
	searchIdx := strings.Index(view, "Search model")
	inputIdx := strings.Index(view, "> claude")
	resultsIdx := strings.Index(view, "Filtered results")
	modelIdx := strings.Index(view, "claude-sonnet-4")

	if searchIdx == -1 || inputIdx == -1 || resultsIdx == -1 || modelIdx == -1 {
		t.Fatalf("expected search input and filtered results in view, got %q", view)
	}
	if !(searchIdx < inputIdx && inputIdx < resultsIdx && resultsIdx < modelIdx) {
		t.Fatalf("expected search input above filtered results, indexes search=%d input=%d results=%d model=%d", searchIdx, inputIdx, resultsIdx, modelIdx)
	}
	if strings.Contains(view, "\nenter\n") {
		t.Fatalf("did not expect separate enter section in model picker, got %q", view)
	}
}

func TestChooserModelLoadingRendersInsidePopupWhenOpenedFromSummary(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL: "http://localhost:8080",
		Harness: "codex",
		Model:   "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID:                    "codex",
			Label:                 "Codex CLI",
			Installed:             true,
			SupportsModelOverride: true,
		}},
	})
	m.phase = phaseModel
	m.returnToSummary = true
	m.loading = true

	view := m.View()
	loadingIdx := strings.Index(view, "loading models from /v1/models...")
	popupIdx := strings.Index(view, "Model")

	if loadingIdx == -1 || popupIdx == -1 {
		t.Fatalf("expected loading message inside model popup, got %q", view)
	}
	if strings.Count(view, "┌") > 1 {
		t.Fatalf("expected a single popup border while loading, got %q", view)
	}
}

func TestChooserPopupUsesBifrostCLIHeading(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL: "http://localhost:8080",
		Harness: "codex",
		Model:   "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID:                    "codex",
			Label:                 "Codex CLI",
			Installed:             true,
			SupportsModelOverride: true,
		}},
	})
	m.phase = phaseModel
	m.returnToSummary = true
	m.models = []string{"gpt-4o-mini"}

	view := m.View()
	if !strings.Contains(view, "Bifrost CLI") {
		t.Fatalf("expected selector popup to use Bifrost CLI heading, got %q", view)
	}
	if strings.Contains(view, "Ready to launch") {
		t.Fatalf("did not expect selector popup to show Ready to launch heading, got %q", view)
	}
}

func TestChooserSummaryUsesActionLabels(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL: "http://localhost:8080",
		Harness: "codex",
		Model:   "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID:                    "codex",
			Label:                 "Codex CLI",
			Installed:             true,
			SupportsModelOverride: true,
		}},
	})

	view := m.View()
	if !strings.Contains(view, "Start") || !strings.Contains(view, "Codex CLI") {
		t.Fatalf("expected summary to show Start <harness>, got %q", view)
	}
	if !strings.Contains(view, "Exit") || !strings.Contains(view, "Bifrost CLI") {
		t.Fatalf("expected summary to show Exit Bifrost CLI, got %q", view)
	}
	if strings.Contains(view, "start Codex CLI") || strings.Contains(view, "Quit") {
		t.Fatalf("did not expect old launch/quit labels, got %q", view)
	}
}

func TestChooserOffersEnterpriseSSOAction(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL:                "https://gateway.example",
		EnterpriseSSOAvailable: true,
		Harness:                "codex",
		Model:                  "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID: "codex", Label: "Codex CLI", Installed: true, SupportsModelOverride: true,
		}},
	})

	view := m.View()
	if !strings.Contains(view, "Enterprise SSO") || !strings.Contains(view, "sign in") {
		t.Fatalf("expected a discoverable Enterprise SSO sign-in action, got %q", view)
	}
}

func TestChooserHidesEnterpriseSSOWhenGatewayDoesNotSupportIt(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL: "http://localhost:8080",
		Harness: "codex",
		Model:   "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID: "codex", Label: "Codex CLI", Installed: true, SupportsModelOverride: true,
		}},
	})

	if view := m.View(); strings.Contains(view, "Enterprise SSO") {
		t.Fatalf("did not expect an Enterprise SSO action for an unsupported gateway, got %q", view)
	}
	for _, action := range m.summaryActions() {
		if action == summaryActionEnterpriseSSO {
			t.Fatal("unsupported Enterprise SSO action remained keyboard-selectable")
		}
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if next.(chooserModel).loginRequested {
		t.Fatal("hidden Enterprise SSO shortcut requested login")
	}
	m.phase = phaseVirtualKey
	if view := m.View(); strings.Contains(view, "Enterprise SSO") {
		t.Fatalf("did not expect an Enterprise SSO hint during virtual-key entry, got %q", view)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if next.(chooserModel).loginRequested {
		t.Fatal("hidden Enterprise SSO F2 shortcut requested login")
	}
}

func TestChooserSummaryAlignsEnterpriseSSOValue(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL:                "https://gateway.example",
		EnterpriseSSOAvailable: true,
		Harness:                "codex",
		Model:                  "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID: "codex", Label: "Codex CLI", Installed: true, SupportsModelOverride: true,
		}},
	})
	m.summaryIdx = m.summaryIndexForAction(summaryActionEnterpriseSSO)
	m.plainLayout = true

	var baseURLColumn, ssoColumn = -1, -1
	for line := range strings.SplitSeq(m.View(), "\n") {
		switch {
		case strings.Contains(line, "Base URL"):
			baseURLColumn = strings.Index(line, "https://gateway.example")
		case strings.Contains(line, "Enterprise SSO"):
			ssoColumn = strings.Index(line, "sign in")
		}
	}

	if baseURLColumn < 0 || ssoColumn < 0 {
		t.Fatalf("expected Base URL and Enterprise SSO summary rows, got %q", m.View())
	}
	if ssoColumn != baseURLColumn {
		t.Fatalf("expected aligned summary values, Base URL column=%d Enterprise SSO column=%d", baseURLColumn, ssoColumn)
	}
}

func TestChooserEnterpriseSSOActionRequestsLoginOrLogout(t *testing.T) {
	baseConfig := ChooserConfig{
		BaseURL:                "https://gateway.example",
		EnterpriseSSOAvailable: true,
		Harness:                "codex",
		Model:                  "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID: "codex", Label: "Codex CLI", Installed: true, SupportsModelOverride: true,
		}},
	}

	t.Run("login", func(t *testing.T) {
		m := newChooserModel(baseConfig)
		m.summaryIdx = m.summaryIndexForAction(summaryActionEnterpriseSSO)
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		got := next.(chooserModel)
		if !got.loginRequested || got.logoutRequested {
			t.Fatalf("login=%v logout=%v", got.loginRequested, got.logoutRequested)
		}
	})

	t.Run("logout", func(t *testing.T) {
		cfg := baseConfig
		cfg.AgentSignedIn = true
		cfg.AgentIdentity = "developer@example.com"
		cfg.UpdateVersion = "v99.0.0"
		m := newChooserModel(cfg)
		m.summaryIdx = m.summaryIndexForAction(summaryActionEnterpriseSSO)
		if view := m.View(); !strings.Contains(view, "signed in as developer@example.com") {
			t.Fatalf("expected signed-in identity, got %q", view)
		}
		if view := m.View(); !strings.Contains(view, "Press Enter to sign out") || strings.Contains(view, "select to sign out") {
			t.Fatalf("expected a compact contextual sign-out hint, got %q", view)
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		got := next.(chooserModel)
		if got.loginRequested || got.logoutRequested || !got.confirmingLogout {
			t.Fatalf("login=%v logout=%v confirming=%v", got.loginRequested, got.logoutRequested, got.confirmingLogout)
		}
		if view := got.View(); !strings.Contains(view, "Sign out of developer@example.com?") {
			t.Fatalf("expected in-place sign-out confirmation, got %q", view)
		}
		next, cmd := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
		got = next.(chooserModel)
		if !got.logoutRequested || got.confirmingLogout || got.updateRequested || cmd == nil {
			t.Fatalf("expected y to request logout instead of update, logout=%v update=%v confirming=%v", got.logoutRequested, got.updateRequested, got.confirmingLogout)
		}
	})

	t.Run("logout cancellation stays in chooser", func(t *testing.T) {
		cfg := baseConfig
		cfg.AgentSignedIn = true
		m := newChooserModel(cfg)
		m.summaryIdx = m.summaryIndexForAction(summaryActionEnterpriseSSO)
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		got := next.(chooserModel)
		if view := got.View(); !strings.Contains(view, "Sign out of Enterprise SSO?") {
			t.Fatalf("expected fallback sign-out identity, got %q", view)
		}
		next, cmd := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		got = next.(chooserModel)
		if got.logoutRequested || got.confirmingLogout || cmd != nil || got.phase != phaseSummary {
			t.Fatalf("expected n to cancel in place, logout=%v confirming=%v phase=%v", got.logoutRequested, got.confirmingLogout, got.phase)
		}
	})

	t.Run("logout supports arrows and enter", func(t *testing.T) {
		cfg := baseConfig
		cfg.AgentSignedIn = true
		m := newChooserModel(cfg)
		m.summaryIdx = m.summaryIndexForAction(summaryActionEnterpriseSSO)
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		got := next.(chooserModel)
		next, _ = got.Update(tea.KeyMsg{Type: tea.KeyLeft})
		got = next.(chooserModel)
		if got.logoutConfirmIdx != 0 {
			t.Fatalf("expected left arrow to select Yes, index=%d", got.logoutConfirmIdx)
		}
		next, cmd := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
		got = next.(chooserModel)
		if !got.logoutRequested || cmd == nil {
			t.Fatal("expected Enter on Yes to request logout and quit")
		}
	})

	t.Run("edited URL cannot reuse old gateway capability", func(t *testing.T) {
		cfg := baseConfig
		cfg.AgentSignedIn = true
		m := newChooserModel(cfg)
		m.baseInput.SetValue("https://other-gateway.example")
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		got := next.(chooserModel)
		if got.loginRequested || got.logoutRequested || got.enterpriseSSOVisible() {
			t.Fatalf("login=%v logout=%v", got.loginRequested, got.logoutRequested)
		}
	})
}

func TestChooserSignedInF2CancelReturnsToVirtualKeyEntry(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL:       "https://gateway.example",
		AgentSignedIn: true,
	})
	m.phase = phaseVirtualKey
	m.vkInput.Focus()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF2})
	got := next.(chooserModel)
	if !got.confirmingLogout || got.phase != phaseSummary {
		t.Fatalf("expected F2 to open sign-out confirmation, confirming=%v phase=%v", got.confirmingLogout, got.phase)
	}

	next, cmd := got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = next.(chooserModel)
	if got.confirmingLogout || got.phase != phaseVirtualKey || !got.vkInput.Focused() || cmd != nil {
		t.Fatalf("expected Escape to return to virtual-key entry, confirming=%v phase=%v focused=%v", got.confirmingLogout, got.phase, got.vkInput.Focused())
	}
}

func TestChooserVirtualKeyEntryKeepsLettersAndUsesF2ForSSO(t *testing.T) {
	m := newChooserModel(ChooserConfig{BaseURL: "https://gateway.example", EnterpriseSSOAvailable: true})
	m.phase = phaseVirtualKey
	m.vkInput.Focus()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	got := next.(chooserModel)
	if got.loginRequested || got.vkInput.Value() != "a" {
		t.Fatalf("typing a produced login=%v value=%q", got.loginRequested, got.vkInput.Value())
	}

	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyF2})
	got = next.(chooserModel)
	if !got.loginRequested {
		t.Fatal("expected F2 to request Enterprise SSO sign in")
	}
}

func TestChooserSummaryMasksVirtualKeyAfterSave(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL:    "http://localhost:8080",
		VirtualKey: "sk-abcdefxyz",
		Harness:    "codex",
		Model:      "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID:                    "codex",
			Label:                 "Codex CLI",
			Installed:             true,
			SupportsModelOverride: true,
		}},
	})

	view := m.View()
	if !strings.Contains(view, "sk*******xyz") {
		t.Fatalf("expected summary to show masked virtual key, got %q", view)
	}
	if strings.Contains(view, "sk-abcdefxyz") {
		t.Fatalf("expected summary not to show full virtual key after save, got %q", view)
	}
}

// TestChooserAuthSummaryReflectsEditedBaseURL verifies the "Auth" summary
// line stops showing "Enterprise SSO" once the Base URL is edited away from
// the trusted profile URL, since the actual launch (launchAgentToken in
// cli/internal/app) withholds the SSO token in that case — the display must
// not claim SSO will be used when it won't.
func TestChooserAuthSummaryReflectsEditedBaseURL(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL:                "https://gateway.example",
		EnterpriseSSOAvailable: true,
		AgentSignedIn:          true,
		Harness:                "codex",
		Model:                  "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID: "codex", Label: "Codex CLI", Installed: true, SupportsModelOverride: true,
		}},
	})

	view := m.View()
	if !strings.Contains(view, "Enterprise SSO") || !strings.Contains(view, "signed in") {
		t.Fatalf("expected initial summary (unedited, trusted URL) to show Enterprise SSO auth, got %q", view)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	got := next.(chooserModel)
	got.baseInput.SetValue("https://typo-gateway.example")
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = next.(chooserModel)

	view = got.View()
	if strings.Contains(view, "Enterprise SSO") || strings.Contains(view, "signed in") {
		t.Fatalf("expected summary to hide Enterprise SSO after editing away from the capability-checked gateway, got %q", view)
	}
}

// TestChooserReservesCtrlBForTabbedMode verifies native launcher sessions do
// not exit the chooser for a tab-manager shortcut that is unavailable there.
func TestChooserReservesCtrlBForTabbedMode(t *testing.T) {
	t.Run("native", func(t *testing.T) {
		model := newChooserModel(ChooserConfig{})
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
		if updated.(chooserModel).backToTabs {
			t.Fatal("native chooser treated ctrl+b as a tab-manager shortcut")
		}
	})

	t.Run("tabbed", func(t *testing.T) {
		model := newChooserModel(ChooserConfig{TabBarLine: func() string { return "tabs" }})
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
		if !updated.(chooserModel).backToTabs {
			t.Fatal("tabbed chooser did not retain ctrl+b shortcut")
		}
	})
}

func TestChooserSummaryVirtualKeyVisibleWhileEditing(t *testing.T) {
	m := newChooserModel(ChooserConfig{
		BaseURL:    "http://localhost:8080",
		VirtualKey: "sk-abcdefxyz",
		Harness:    "codex",
		Model:      "gpt-4o-mini",
		Harnesses: []HarnessOption{{
			ID:                    "codex",
			Label:                 "Codex CLI",
			Installed:             true,
			SupportsModelOverride: true,
		}},
	})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	got := next.(chooserModel)
	if !got.summaryEditing || got.summaryEditAction != summaryActionVirtualKey {
		t.Fatalf("expected v to edit virtual key, editing=%v action=%v", got.summaryEditing, got.summaryEditAction)
	}

	view := got.View()
	if !strings.Contains(view, "sk-abcdefxyz") {
		t.Fatalf("expected full virtual key to be visible while editing, got %q", view)
	}

	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = next.(chooserModel)
	view = got.View()
	if !strings.Contains(view, "sk*******xyz") || strings.Contains(view, "sk-abcdefxyz") {
		t.Fatalf("expected saved virtual key to be masked, got %q", view)
	}
}

func TestUpdateConfirmUsesHomeStylePopup(t *testing.T) {
	m := confirmModel{
		prompt:    "Bifrost v1.2.3 is available. Update now?",
		idx:       1,
		homeStyle: true,
		width:     100,
		height:    30,
	}

	view := m.View()
	for _, want := range []string{"Bifrost CLI", "Home", "Update available", "Bifrost v1.2.3 is available"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected update confirm view to contain %q, got %q", want, view)
		}
	}
	if !strings.Contains(view, "╭") {
		t.Fatalf("expected update confirm to render a popup border, got %q", view)
	}
}
