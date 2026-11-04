package promptcompression

import (
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

func intp(v int) *int { return &v }

func TestResolveDefaults(t *testing.T) {
	s, err := (&Config{}).resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := settings{
		MinAgeTurns: 2, MinTokens: 500, HeadLines: 20, TailLines: 20,
		ProtectedLeading: 1, MinSavingsPercent: 5, Dedupe: true, Whitespace: true,
	}
	if s != want {
		t.Fatalf("got %+v, want %+v", s, want)
	}
}

func TestResolveExplicitZeroKept(t *testing.T) {
	s, err := (&Config{ProtectedLeadingMessages: intp(0), MinSavingsPercent: intp(0), ToolOutputMinTokens: intp(0)}).resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.ProtectedLeading != 0 || s.MinSavingsPercent != 0 || s.MinTokens != 0 {
		t.Fatalf("explicit zeros overridden: %+v", s)
	}
}

// A min age of 0 would make the newest output eligible, so the model could
// lose the result it is about to read; 1 is the smallest safe value.
func TestResolveRejectsZeroMinAge(t *testing.T) {
	_, err := (&Config{ToolOutputMinAgeTurns: intp(0)}).resolve()
	if err == nil || !strings.Contains(err.Error(), "tool_output_min_age_turns") {
		t.Fatalf("expected error naming tool_output_min_age_turns, got %v", err)
	}
}

func TestResolveRejectsInvalid(t *testing.T) {
	cases := map[string]Config{
		"min_age":     {ToolOutputMinAgeTurns: intp(-1)},
		"min_tokens":  {ToolOutputMinTokens: intp(-1)},
		"head":        {KeepHeadLines: intp(-1)},
		"tail":        {KeepTailLines: intp(-1)},
		"protected":   {ProtectedLeadingMessages: intp(-1)},
		"savings_neg": {MinSavingsPercent: intp(-1)},
		"savings_101": {MinSavingsPercent: intp(101)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := c.resolve(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestInitNilConfig(t *testing.T) {
	p, err := Init(nil, bifrost.NewDefaultLogger(schemas.LogLevelDebug))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pl := p.(*Plugin)
	if pl.s.MinTokens != 500 || !pl.s.Dedupe {
		t.Fatalf("defaults not applied: %+v", pl.s)
	}
}

func TestInitRejectsInvalid(t *testing.T) {
	if _, err := Init(&Config{MinSavingsPercent: intp(101)}, bifrost.NewDefaultLogger(schemas.LogLevelDebug)); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetName(t *testing.T) {
	p, _ := Init(nil, bifrost.NewDefaultLogger(schemas.LogLevelDebug))
	if p.GetName() != "prompt_compression" {
		t.Fatalf("got %q", p.GetName())
	}
}
