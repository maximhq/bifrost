package logo

import (
	"strings"
	"testing"
)

func TestRenderLarge(t *testing.T) {
	got := Render(120)
	if !strings.Contains(got, "██████╗") {
		t.Fatalf("expected large logo, got %q", got)
	}
}

func TestRenderCompact(t *testing.T) {
	got := Render(20)
	if got != "BIFROST CLI" {
		t.Fatalf("expected compact logo, got %q", got)
	}
}

func TestBootHeaderStartsWithLogo(t *testing.T) {
	header := BootHeader(120, "v1", "abc", "none", true)
	if !strings.Contains(header, Render(120)) {
		t.Fatalf("expected boot header to contain logo")
	}
	if !strings.Contains(header, "config=none") {
		t.Fatalf("expected boot header to contain config source")
	}
}
