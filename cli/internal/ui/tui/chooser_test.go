package tui

import (
	"os"
	"strings"
	"testing"
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
