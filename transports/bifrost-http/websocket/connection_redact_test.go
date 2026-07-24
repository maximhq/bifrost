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

// Regression test (follow-up to the Greptile PR review finding): the Gemini
// API key must never become part of PoolKey.Endpoint — the Go map key held in
// memory for the pool's lifetime and stored on UpstreamConn for diagnostics —
// while non-credential query params other providers rely on for correct pool
// bucketing (OpenAI's `?model=`, Azure-style `?deployment=`) must be preserved
// untouched, or connections for different models would collapse into the same
// pool bucket.
func TestSanitizeEndpointForPoolKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips Gemini API key query param",
			in:   "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key=AIzaSySECRET",
			want: "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent",
		},
		{
			name: "preserves OpenAI's model query param",
			in:   "wss://api.openai.com/v1/realtime?model=gpt-4o-realtime-preview",
			want: "wss://api.openai.com/v1/realtime?model=gpt-4o-realtime-preview",
		},
		{
			name: "preserves Azure's deployment query param",
			in:   "wss://my-resource.openai.azure.com/openai/v1/realtime?deployment=gpt-4o-realtime",
			want: "wss://my-resource.openai.azure.com/openai/v1/realtime?deployment=gpt-4o-realtime",
		},
		{
			name: "ElevenLabs has no query-param secret, unchanged",
			in:   "wss://api.elevenlabs.io/v1/convai/conversation?agent_id=agent-123",
			want: "wss://api.elevenlabs.io/v1/convai/conversation?agent_id=agent-123",
		},
		{
			name: "no query params is a no-op",
			in:   "wss://api.openai.com/v1/realtime",
			want: "wss://api.openai.com/v1/realtime",
		},
		{
			name: "strips a token-named param alongside a preserved one",
			in:   "wss://example.com/realtime?model=foo&access_token=SECRET",
			want: "wss://example.com/realtime?model=foo",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeEndpointForPoolKey(tc.in)
			if got != tc.want {
				t.Fatalf("SanitizeEndpointForPoolKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
