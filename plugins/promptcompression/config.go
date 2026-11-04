// Package promptcompression compresses old tool outputs in agent-loop requests.
package promptcompression

import "fmt"

// Config is the user-facing plugin configuration. Pointer fields distinguish
// "unset" (default applies) from an explicit zero.
type Config struct {
	ToolOutputMinAgeTurns    *int  `json:"tool_output_min_age_turns,omitempty"`
	ToolOutputMinTokens      *int  `json:"tool_output_min_tokens,omitempty"`
	KeepHeadLines            *int  `json:"keep_head_lines,omitempty"`
	KeepTailLines            *int  `json:"keep_tail_lines,omitempty"`
	DedupeToolOutputs        *bool `json:"dedupe_tool_outputs,omitempty"`
	NormalizeWhitespace      *bool `json:"normalize_whitespace,omitempty"`
	ProtectedLeadingMessages *int  `json:"protected_leading_messages,omitempty"`
	MinSavingsPercent        *int  `json:"min_savings_percent,omitempty"`
}

// settings holds the resolved configuration with defaults applied.
type settings struct {
	MinAgeTurns       int
	MinTokens         int
	HeadLines         int
	TailLines         int
	ProtectedLeading  int
	MinSavingsPercent int
	Dedupe            bool
	Whitespace        bool
}

func orInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func orBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// resolve applies defaults and validates the configuration.
func (c *Config) resolve() (settings, error) {
	if c == nil {
		c = &Config{}
	}
	s := settings{
		MinAgeTurns:       orInt(c.ToolOutputMinAgeTurns, 2),
		MinTokens:         orInt(c.ToolOutputMinTokens, 500),
		HeadLines:         orInt(c.KeepHeadLines, 20),
		TailLines:         orInt(c.KeepTailLines, 20),
		ProtectedLeading:  orInt(c.ProtectedLeadingMessages, 1),
		MinSavingsPercent: orInt(c.MinSavingsPercent, 5),
		Dedupe:            orBool(c.DedupeToolOutputs, true),
		Whitespace:        orBool(c.NormalizeWhitespace, true),
	}
	for name, v := range map[string]int{
		"tool_output_min_age_turns":  s.MinAgeTurns,
		"tool_output_min_tokens":     s.MinTokens,
		"keep_head_lines":            s.HeadLines,
		"keep_tail_lines":            s.TailLines,
		"protected_leading_messages": s.ProtectedLeading,
		"min_savings_percent":        s.MinSavingsPercent,
	} {
		if v < 0 {
			return settings{}, fmt.Errorf("promptcompression: %s must be >= 0, got %d", name, v)
		}
	}
	// Age 0 would make the newest tool output eligible, which the model has not
	// read yet; 1 is the smallest safe value.
	if s.MinAgeTurns < 1 {
		return settings{}, fmt.Errorf("promptcompression: tool_output_min_age_turns must be >= 1, got %d", s.MinAgeTurns)
	}
	if s.MinSavingsPercent > 100 {
		return settings{}, fmt.Errorf("promptcompression: min_savings_percent must be <= 100, got %d", s.MinSavingsPercent)
	}
	return s, nil
}
