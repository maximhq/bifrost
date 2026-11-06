package handlers

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// A tool result for an auth-required error points the caller at a page when there is one to open,
// and otherwise carries the resolver's own message. Exchange never has a page: the fix is to the
// request's credential, and prompting the caller to open an empty URL told them nothing.
func TestMCPAuthRequiredToolResult(t *testing.T) {
	cases := []struct {
		name       string
		authReq    *schemas.MCPAuthRequiredError
		want       string
		wantAbsent string
		wantPrefix bool
	}{
		{
			name: "per-user oauth opens the authorize page",
			authReq: &schemas.MCPAuthRequiredError{
				Kind: schemas.MCPAuthRequiredKindOAuth, MCPClientName: "github",
				AuthorizeURL: "https://idp.example/authorize?state=abc", Message: "Authentication required for github. Visit https://idp.example/authorize?state=abc to connect your account.",
			},
			want: "Authentication required for github. Open this URL to connect your account: https://idp.example/authorize?state=abc",
		},
		{
			name: "per-user headers opens the submit page",
			authReq: &schemas.MCPAuthRequiredError{
				Kind: schemas.MCPAuthRequiredKindHeaders, MCPClientName: "jira",
				SubmitURL: "https://bifrost.example/mcp/headers/jira", Message: "Authentication required for jira. Visit https://bifrost.example/mcp/headers/jira to submit the required headers.",
			},
			want: "Authentication required for jira. Open this URL to submit the required headers: https://bifrost.example/mcp/headers/jira",
		},
		{
			name: "exchange carries the resolver message as written",
			authReq: &schemas.MCPAuthRequiredError{
				Kind: schemas.MCPAuthRequiredKindExchange, MCPClientName: "entra_obo_server", SubjectTokenMissing: true,
				Message: "Authentication required for entra_obo_server: this server uses your identity token, so the request must carry one.",
			},
			want:       "Authentication required for entra_obo_server: this server uses your identity token, so the request must carry one.",
			wantAbsent: "Open this URL",
		},
		{
			name: "an interactive kind with no page falls back to its message",
			authReq: &schemas.MCPAuthRequiredError{
				Kind: schemas.MCPAuthRequiredKindOAuth, MCPClientName: "github",
				Message: "Authentication required for github. Ask an administrator to finish configuring this server.",
			},
			want:       "Authentication required for github. Ask an administrator to finish configuring this server.",
			wantAbsent: "Open this URL",
		},
		{
			name:    "nothing to say still names the server",
			authReq: &schemas.MCPAuthRequiredError{Kind: schemas.MCPAuthRequiredKindExchange, MCPClientName: "entra_obo_server"},
			want:    "Authentication required for entra_obo_server.",
		},
		{
			name: "a temp-token fragment keeps its reminder",
			authReq: &schemas.MCPAuthRequiredError{
				Kind: schemas.MCPAuthRequiredKindHeaders, MCPClientName: "jira",
				SubmitURL: "https://bifrost.example/mcp/headers/jira#t=one-time",
			},
			want:       "Authentication required for jira. Open this URL to submit the required headers: https://bifrost.example/mcp/headers/jira#t=one-time",
			wantPrefix: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpAuthRequiredToolResult(tc.authReq)
			if tc.wantPrefix {
				if !strings.HasPrefix(got, tc.want) || !strings.Contains(got, schemas.MCPAuthTempTokenReminder) {
					t.Fatalf("got %q, want prefix %q plus the temp-token reminder", got, tc.want)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if tc.wantAbsent != "" && strings.Contains(got, tc.wantAbsent) {
				t.Fatalf("got %q, must not contain %q", got, tc.wantAbsent)
			}
		})
	}
}
