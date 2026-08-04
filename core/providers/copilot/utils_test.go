package copilot

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestIsValidCopilotAPIBase(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		// Valid
		{"valid individual domain", "https://api.individual.githubcopilot.com", true},
		{"valid business domain", "https://api.business.githubcopilot.com", true},
		{"valid github.com subdomain", "https://api.github.com", true},
		{"valid deep github.com subdomain", "https://copilot.api.github.com", true},
		{"valid enterprise githubcopilot subdomain", "https://api.enterprise.githubcopilot.com", true},

		// Valid — explicit port (enterprise / proxied deployments may include one).
		// Verifies u.Hostname() strips the port before the suffix check.
		{"valid githubcopilot with explicit https port", "https://api.individual.githubcopilot.com:443", true},
		{"valid github.com with non-default port", "https://api.github.com:8443", true},

		// Invalid — wrong scheme
		{"http not https", "http://api.individual.githubcopilot.com", false},
		{"ftp scheme", "ftp://api.individual.githubcopilot.com", false},
		{"no scheme", "api.individual.githubcopilot.com", false},

		// Invalid — wrong domain
		{"unrelated domain", "https://evil.com", false},
		{"similar but not suffix", "https://notgithubcopilot.com", false},
		{"githubcopilot.com in path not host", "https://evil.com/githubcopilot.com", false},

		// SSRF vectors
		{"look-alike suffix attack", "https://notreally.githubcopilot.com.evil.co", false},
		{"github.com in subdomain of attacker", "https://github.com.attacker.io", false},
		{"empty string", "", false},
		{"invalid URL", "://bad-url", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isValidCopilotAPIBase(tc.input)
			if got != tc.want {
				t.Errorf("isValidCopilotAPIBase(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func userMessage(blocks ...schemas.ChatContentBlock) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{ContentBlocks: blocks},
	}
}

func TestChatRequestHeaders(t *testing.T) {
	text := "hi"
	imageBlock := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage}
	textBlock := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: &text}

	tests := []struct {
		name          string
		request       *schemas.BifrostChatRequest
		wantInitiator string
		wantVision    bool
	}{
		{
			name:          "nil request defaults to user",
			request:       nil,
			wantInitiator: copilotInitiatorUser,
		},
		{
			name:          "empty input defaults to user",
			request:       &schemas.BifrostChatRequest{},
			wantInitiator: copilotInitiatorUser,
		},
		{
			name: "last message from user is a user turn",
			request: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleSystem},
				userMessage(textBlock),
			}},
			wantInitiator: copilotInitiatorUser,
		},
		{
			name: "trailing tool result is an agent turn",
			request: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				userMessage(textBlock),
				{Role: schemas.ChatMessageRoleAssistant},
				{Role: schemas.ChatMessageRoleTool},
			}},
			wantInitiator: copilotInitiatorAgent,
		},
		{
			name: "trailing assistant prefill is an agent turn",
			request: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				userMessage(textBlock),
				{Role: schemas.ChatMessageRoleAssistant},
			}},
			wantInitiator: copilotInitiatorAgent,
		},
		{
			name: "image anywhere in history sets vision",
			request: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				userMessage(imageBlock),
				{Role: schemas.ChatMessageRoleAssistant},
				{Role: schemas.ChatMessageRoleTool},
			}},
			wantInitiator: copilotInitiatorAgent,
			wantVision:    true,
		},
		{
			name: "string content without blocks does not set vision",
			request: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &text}},
			}},
			wantInitiator: copilotInitiatorUser,
		},
		{
			name: "nil content is skipped",
			request: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser},
			}},
			wantInitiator: copilotInitiatorUser,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chatRequestHeaders(tc.request)
			if got[copilotInitiatorHeader] != tc.wantInitiator {
				t.Errorf("%s = %q, want %q", copilotInitiatorHeader, got[copilotInitiatorHeader], tc.wantInitiator)
			}
			_, hasVision := got[copilotVisionHeader]
			if hasVision != tc.wantVision {
				t.Errorf("%s present = %v, want %v", copilotVisionHeader, hasVision, tc.wantVision)
			}
			if got[copilotIntentHeader] != copilotIntent {
				t.Errorf("%s = %q, want %q", copilotIntentHeader, got[copilotIntentHeader], copilotIntent)
			}
		})
	}
}

func TestResponsesRequestHeaders(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	imageBlock := schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesInputMessageContentBlockTypeImage}
	toolOutput := schemas.ResponsesMessageTypeFunctionCallOutput

	tests := []struct {
		name          string
		request       *schemas.BifrostResponsesRequest
		wantInitiator string
		wantVision    bool
	}{
		{
			name:          "nil request defaults to user",
			request:       nil,
			wantInitiator: copilotInitiatorUser,
		},
		{
			name: "last item from user is a user turn",
			request: &schemas.BifrostResponsesRequest{Input: []schemas.ResponsesMessage{
				{Role: &userRole},
			}},
			wantInitiator: copilotInitiatorUser,
		},
		{
			name: "roleless function_call_output is an agent turn",
			request: &schemas.BifrostResponsesRequest{Input: []schemas.ResponsesMessage{
				{Role: &userRole},
				{Type: &toolOutput},
			}},
			wantInitiator: copilotInitiatorAgent,
		},
		{
			name: "trailing assistant item is an agent turn",
			request: &schemas.BifrostResponsesRequest{Input: []schemas.ResponsesMessage{
				{Role: &userRole},
				{Role: &assistantRole},
			}},
			wantInitiator: copilotInitiatorAgent,
		},
		{
			name: "input_image sets vision",
			request: &schemas.BifrostResponsesRequest{Input: []schemas.ResponsesMessage{
				{Role: &userRole, Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{imageBlock},
				}},
			}},
			wantInitiator: copilotInitiatorUser,
			wantVision:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := responsesRequestHeaders(tc.request)
			if got[copilotInitiatorHeader] != tc.wantInitiator {
				t.Errorf("%s = %q, want %q", copilotInitiatorHeader, got[copilotInitiatorHeader], tc.wantInitiator)
			}
			_, hasVision := got[copilotVisionHeader]
			if hasVision != tc.wantVision {
				t.Errorf("%s present = %v, want %v", copilotVisionHeader, hasVision, tc.wantVision)
			}
		})
	}
}

// TestMergedExtraHeadersPrecedence pins the escape hatch: configured ExtraHeaders
// must be able to override the derived per-request headers, while the required
// Copilot headers still win over everything.
func TestMergedExtraHeadersPrecedence(t *testing.T) {
	provider := &CopilotProvider{
		networkConfig: schemas.NetworkConfig{
			ExtraHeaders: map[string]string{
				copilotIntentHeader: "operator-override",
				"x-custom":          "kept",
			},
		},
	}

	merged := provider.mergedExtraHeaders(map[string]string{
		copilotIntentHeader:    copilotIntent,
		copilotInitiatorHeader: copilotInitiatorAgent,
	})

	if merged[copilotIntentHeader] != "operator-override" {
		t.Errorf("ExtraHeaders should override per-request header, got %q", merged[copilotIntentHeader])
	}
	if merged[copilotInitiatorHeader] != copilotInitiatorAgent {
		t.Errorf("un-overridden per-request header should survive, got %q", merged[copilotInitiatorHeader])
	}
	if merged["x-custom"] != "kept" {
		t.Errorf("unrelated ExtraHeaders should survive, got %q", merged["x-custom"])
	}
	for k, v := range copilotRequiredHeaders {
		if merged[k] != v {
			t.Errorf("required header %s = %q, want %q", k, merged[k], v)
		}
	}
}

// TestMergedExtraHeadersNilPerRequest pins that ListModels (which passes nil)
// still gets the required headers and does not panic.
func TestMergedExtraHeadersNilPerRequest(t *testing.T) {
	provider := &CopilotProvider{}
	merged := provider.mergedExtraHeaders(nil)
	if len(merged) != len(copilotRequiredHeaders) {
		t.Errorf("expected only required headers, got %v", merged)
	}
	if _, ok := merged[copilotInitiatorHeader]; ok {
		t.Errorf("ListModels should not carry %s", copilotInitiatorHeader)
	}
}
