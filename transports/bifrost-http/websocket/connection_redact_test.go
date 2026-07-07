package websocket

import "testing"

// Regression test (found in Greptile PR review): dial-error messages must not
// leak query-string secrets. Gemini Live authenticates via a `?key=` query
// param on the dial URL (unlike OpenAI/Azure/ElevenLabs, which use headers),
// so a raw dial URL in an error message would expose the API key to clients
// and server logs.
func TestRedactURLForLog(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips API key query param",
			in:   "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key=AIzaSySECRET",
			want: "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent",
		},
		{
			name: "strips userinfo",
			in:   "wss://user:pass@example.com/realtime",
			want: "wss://example.com/realtime",
		},
		{
			name: "no query param is a no-op",
			in:   "wss://api.openai.com/v1/realtime",
			want: "wss://api.openai.com/v1/realtime",
		},
		{
			name: "invalid URL",
			in:   "://not a url",
			want: "<invalid-url>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactURLForLog(tc.in)
			if got != tc.want {
				t.Fatalf("redactURLForLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
