package warp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Models don't reliably leave a root-relative link alone, despite prompt.go
// telling them not to invent one: one prepends a scheme and a bogus host,
// another drops the "workspace" segment it doesn't recognise. Either way the
// link goes nowhere, in every environment, so it is repaired before the
// answer is streamed to the client or saved to history - the query string a
// tool built is preserved exactly.
func TestWarpSanitizeAnswerLinksRepairsMangledPaths(t *testing.T) {
	cases := map[string]string{
		// A scheme and host prepended to the whole path.
		"See [this request](https://workspace/logs?selected_log=req-1) for details.": "See [this request](/workspace/logs?selected_log=req-1) for details.",
		"[logs](http://workspace/logs?providers=openai)":                             "[logs](/workspace/logs?providers=openai)",
		// The "workspace" segment dropped entirely.
		"[logs](/logs?start_time=1&end_time=2)": "[logs](/workspace/logs?start_time=1&end_time=2)",
		// No leading slash at all.
		"[logs](workspace/logs?start_time=1&end_time=2)": "[logs](/workspace/logs?start_time=1&end_time=2)",
		"[logs](logs?start_time=1&end_time=2)":           "[logs](/workspace/logs?start_time=1&end_time=2)",
		// No query string (logsViewLink with no filters).
		"[logs](/logs)": "[logs](/workspace/logs)",
		// A correct link, or unrelated text, passes through untouched.
		"See [this request](/workspace/logs?selected_log=req-1) for details.": "See [this request](/workspace/logs?selected_log=req-1) for details.",
		"no links here": "no links here",
		// A genuinely external link that happens to end in "/logs" is not a
		// mangled workspace path and must be left alone.
		"[external logs](https://example.com/logs)":         "[external logs](https://example.com/logs)",
		"[external logs](https://example.com/logs?foo=bar)": "[external logs](https://example.com/logs?foo=bar)",
		"[not us](https://workspace.attacker.example/logs)": "[not us](https://workspace.attacker.example/logs)",
	}
	for input, want := range cases {
		require.Equal(t, want, sanitizeAnswerLinks(input), "input: %s", input)
	}
}
