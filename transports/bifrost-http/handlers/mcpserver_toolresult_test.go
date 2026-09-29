package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

type richToolResultManager struct {
	result *mcp.CallToolResult
}

func (m *richToolResultManager) GetAvailableMCPTools(context.Context) []schemas.ChatTool {
	return nil
}

func (m *richToolResultManager) ExecuteChatMCPTool(context.Context, *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	text := "Attachment report.pdf"
	return &schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{ContentStr: &text},
	}, nil
}

func (m *richToolResultManager) ExecuteResponsesMCPTool(context.Context, *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	return nil, nil
}

// ExecuteRawMCPTool returns the protocol-native result used by the gateway path.
func (m *richToolResultManager) ExecuteRawMCPTool(context.Context, *schemas.ChatAssistantMessageToolCall) (*mcp.CallToolResult, *schemas.BifrostError) {
	return m.result, nil
}

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

func TestMCPGatewayPreservesRawToolResult(t *testing.T) {
	SetLogger(&mockLogger{})

	want := &mcp.CallToolResult{
		Result: mcp.Result{Meta: &mcp.Meta{AdditionalFields: map[string]any{
			"fixture": "mcp-gateway-rich-result",
		}}},
		Content: []mcp.Content{
			mcp.TextContent{Type: mcp.ContentTypeText, Text: "Attachment report.pdf"},
			mcp.ImageContent{Type: mcp.ContentTypeImage, Data: "aW1hZ2U=", MIMEType: "image/png"},
			mcp.AudioContent{Type: mcp.ContentTypeAudio, Data: "YXVkaW8=", MIMEType: "audio/wav"},
			mcp.EmbeddedResource{
				Type: mcp.ContentTypeResource,
				Resource: mcp.BlobResourceContents{
					URI:      "gmail-attachment://message/report.pdf",
					MIMEType: "application/pdf",
					Blob:     "JVBERi0xLjQK",
				},
			},
			mcp.ResourceLink{
				Type:        mcp.ContentTypeLink,
				URI:         "ui://attachment/report.pdf",
				Name:        "report.pdf",
				Description: "Open the attachment",
				MIMEType:    "application/pdf",
			},
		},
		StructuredContent: map[string]any{
			"filename": "report.pdf",
			"size":     float64(606),
		},
		IsError: true,
	}
	manager := &richToolResultManager{result: want}
	handler := &MCPServerHandler{toolManager: manager}
	toolName := "gmail-download_attachment"
	server := handler.buildServer([]schemas.ChatTool{{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:       toolName,
			Parameters: &schemas.ToolFunctionParameters{Type: "object"},
		},
	}})

	response := server.HandleMessage(context.Background(), []byte(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"tools/call",
		"params":{"name":"gmail-download_attachment","arguments":{}}
	}`))
	require.NotNil(t, response)

	responseJSON, err := json.Marshal(response)
	require.NoError(t, err)
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(responseJSON, &envelope))
	require.NotEmpty(t, envelope.Result, "response: %s", responseJSON)

	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	require.JSONEq(t, string(wantJSON), string(envelope.Result))
}
