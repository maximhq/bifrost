package tui

import (
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
