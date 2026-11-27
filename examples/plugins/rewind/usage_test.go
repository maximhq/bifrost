package main

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestPriceFor(t *testing.T) {
	tok := Tokens{Prompt: 1_000_000, Completion: 1_000_000}
	cases := []struct {
		served, requested string
		want              float64
	}{
		{"gemini-2.5-flash-lite", "", 0.50},
		{"gemini-2.5-flash", "", 2.80},
		{"gemini/gemini-2.5-pro", "", 11.25},
		{"", "groq/llama-3.3-70b-versatile", 1.38},
		{"llama-3.3-70b-versatile", "gemini/gemini-2.5-pro", 1.38}, // served wins
		{"unknown-model", "", 0},
		{"", "", 0},
	}
	for _, c := range cases {
		if got := priceFor(c.served, c.requested, tok); !near(got, c.want) {
			t.Errorf("priceFor(%q,%q) = %v, want %v", c.served, c.requested, got, c.want)
		}
	}
}

func TestFillUsage(t *testing.T) {
	s := Step{Kind: "llm", Model: "gemini/gemini-2.5-flash"}
	fillUsage(&s, []byte(`{"model":"gemini-2.5-flash-lite","usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	if s.Tokens != (Tokens{1000, 500}) || s.ServedModel != "gemini-2.5-flash-lite" {
		t.Fatalf("step = %+v", s)
	}
	if !near(s.CostUSD, (1000*0.10+500*0.40)/1e6) {
		t.Fatalf("cost = %v", s.CostUSD)
	}

	// responses API style usage, no served model: falls back to requested
	s = Step{Kind: "llm", Model: "gemini/gemini-2.5-pro"}
	fillUsage(&s, []byte(`{"usage":{"input_tokens":2000,"output_tokens":100}}`))
	if s.Tokens != (Tokens{2000, 100}) || !near(s.CostUSD, (2000*1.25+100*10)/1e6) {
		t.Fatalf("responses usage = %+v", s)
	}

	s = Step{Kind: "llm", Model: "x"}
	fillUsage(&s, []byte(`not json`))
	if s.CostUSD != 0 || s.Tokens != (Tokens{}) {
		t.Fatalf("invalid body should leave step alone: %+v", s)
	}

	s = Step{Kind: "relay", Status: 200}
	fillUsage(&s, []byte(`{}`))
	if s.CostUSD != tavilySearchUSD {
		t.Fatalf("relay cost = %v", s.CostUSD)
	}
	s = Step{Kind: "relay", Status: 502}
	fillUsage(&s, []byte(`{}`))
	if s.CostUSD != 0 {
		t.Fatal("failed relay call should cost nothing")
	}
}
