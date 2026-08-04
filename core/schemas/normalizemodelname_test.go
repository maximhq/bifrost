package schemas

import "testing"

func TestNormalizeModelName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Bedrock cross-region inference profiles: strip region + vendor + version + date.
		{"us.anthropic.claude-opus-4-20250101-v1:0", "claude-opus-4"},
		{"eu.anthropic.claude-3-5-sonnet-20241022-v1:0", "claude-3-5-sonnet"},
		{"anthropic.claude-3-5-sonnet-20241022", "claude-3-5-sonnet"},
		{"anthropic.claude-opus-4-6-v1", "claude-opus-4-6"},
		// Plain Bedrock ids for other vendors.
		{"meta.llama3-70b-instruct-v1:0", "llama3-70b-instruct"},
		{"cohere.command-r-v1:0", "command-r"},
		// OpenAI dated resolved names collapse to the base.
		{"gpt-4o-mini-2024-07-18", "gpt-4o-mini"},
		{"gpt-4o-2024-08-06", "gpt-4o"},
		// Anthropic dated (non-Bedrock) collapses to the base.
		{"claude-sonnet-4-20250514", "claude-sonnet-4"},
		// Digit-dotted names without a vendor prefix must be preserved.
		{"gpt-3.5-turbo", "gpt-3.5-turbo"},
		{"gemini-1.5-pro", "gemini-1.5-pro"},
		// OpenAI fine-tune ids must be preserved (trailing segment is an id, not a version),
		// including a date-like custom suffix BaseModelName would otherwise strip.
		{"ft:gpt-4o-mini:acme:custom:AbCd1234", "ft:gpt-4o-mini:acme:custom:AbCd1234"},
		{"ft:gpt-4o-mini:acme:custom-20250514", "ft:gpt-4o-mini:acme:custom-20250514"},
		// Already-base names are unchanged.
		{"gpt-4o-mini", "gpt-4o-mini"},
		{"claude-opus-4", "claude-opus-4"},
		// Whitespace is trimmed; blank/whitespace-only collapses to "".
		{"  gpt-4o-mini  ", "gpt-4o-mini"},
		{"   ", ""},
		{"\t\n", ""},
		// Empty stays empty.
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeModelName(c.in); got != c.want {
			t.Errorf("NormalizeModelName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
