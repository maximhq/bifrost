package complexity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

func TestBuildComplexitySessionKeyRejectsBlankIdentity(t *testing.T) {
	for _, tt := range []struct {
		name      string
		scopeID   string
		sessionID string
	}{
		{name: "blank_scope", scopeID: " ", sessionID: "session"},
		{name: "blank_session", scopeID: "tenant", sessionID: "\t"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			key, ok := BuildSessionKey(tt.scopeID, tt.sessionID)
			assert.False(t, ok)
			assert.Empty(t, key)
		})
	}
}

func TestBuildComplexitySessionKeyScopeIsolation(t *testing.T) {
	const (
		scopeID   = "tenant-secret-value"
		sessionID = "session-private-value"
	)

	key, ok := BuildSessionKey(scopeID, sessionID)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(key, SessionKeyPrefix))
	assert.Len(t, strings.TrimPrefix(key, SessionKeyPrefix), 64)
	assert.NotContains(t, key, scopeID)
	assert.NotContains(t, key, sessionID)

	sameKey, ok := BuildSessionKey(scopeID, sessionID)
	require.True(t, ok)
	assert.Equal(t, key, sameKey)

	otherScopeKey, ok := BuildSessionKey("other-tenant", sessionID)
	require.True(t, ok)
	assert.NotEqual(t, key, otherScopeKey)

	otherSessionKey, ok := BuildSessionKey(scopeID, "other-session")
	require.True(t, ok)
	assert.NotEqual(t, key, otherSessionKey)
}

func TestCodexBackgroundKindSurvivesMalformedSessionID(t *testing.T) {
	ctx := complexityHarnessContext(schemas.CodexCLI.String(), map[string]string{
		codexTurnMetadataHeader: `{"request_kind":"compaction","session_id":123}`,
	})

	input, ok := BuildInput(ctx, complexitySessionChatRequest("Be concise", "Compact the conversation"))

	assert.False(t, ok)
	assert.Empty(t, input)
}

func TestNormalizeComplexitySessionID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "trims_valid_id", id: " session-abc ", want: "session-abc"},
		{name: "accepts_maximum_length", id: strings.Repeat("a", maxComplexitySessionIDBytes), want: strings.Repeat("a", maxComplexitySessionIDBytes)},
		{name: "rejects_blank_id", id: "\t"},
		{name: "rejects_oversized_id", id: strings.Repeat("a", maxComplexitySessionIDBytes+1)},
		{name: "rejects_nul", id: "session\x00abc"},
		{name: "rejects_invalid_utf8", id: string([]byte{0xff})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeComplexitySessionID(tt.id))
		})
	}
}

func TestResolveSessionIDBlankHeaderFallsThrough(t *testing.T) {
	ctx := complexityHarnessContext(schemas.ClaudeCLI.String(), map[string]string{
		claudeCodeSessionIDHeader: "native-session",
	})
	ctx.SetValue(schemas.BifrostContextKeySessionID, " ")

	id, source, ok := ResolveSessionID(ctx, []string{
		configstore.ComplexitySessionIdentityHeader,
		configstore.ComplexitySessionIdentityHarness,
	})

	require.True(t, ok)
	assert.Equal(t, "native-session", id)
	assert.Equal(t, configstore.ComplexitySessionIdentityHarness, source)
}

func TestResolveSessionIDHarnessSources(t *testing.T) {
	tests := []struct {
		name        string
		userAgent   string
		headers     map[string]string
		wantID      string
		wantPresent bool
	}{
		{
			name:      "claude_code_header",
			userAgent: schemas.ClaudeCLI.String(),
			headers: map[string]string{
				claudeCodeSessionIDHeader: " claude-session ",
			},
			wantID:      "claude-session",
			wantPresent: true,
		},
		{
			name:      "codex_cli_metadata",
			userAgent: schemas.CodexCLI.String(),
			headers: map[string]string{
				codexTurnMetadataHeader: `{"request_kind":"turn","session_id":" codex-session "}`,
			},
			wantID:      "codex-session",
			wantPresent: true,
		},
		{
			name:      "codex_desktop_metadata",
			userAgent: schemas.CodexDesktop.String(),
			headers: map[string]string{
				codexTurnMetadataHeader: `{"session_id":"desktop-session"}`,
			},
			wantID:      "desktop-session",
			wantPresent: true,
		},
		{
			name:      "generic_client_cannot_claim_claude_session",
			userAgent: "generic-client/1.0",
			headers: map[string]string{
				claudeCodeSessionIDHeader: "spoofed-session",
			},
		},
		{
			name:      "generic_client_cannot_claim_codex_session",
			userAgent: "generic-client/1.0",
			headers: map[string]string{
				codexTurnMetadataHeader: `{"session_id":"spoofed-session"}`,
			},
		},
		{
			name:      "malformed_codex_metadata",
			userAgent: schemas.CodexCLI.String(),
			headers: map[string]string{
				codexTurnMetadataHeader: "{",
			},
		},
		{
			name:      "non_string_codex_session",
			userAgent: schemas.CodexCLI.String(),
			headers: map[string]string{
				codexTurnMetadataHeader: `{"session_id":123}`,
			},
		},
		{
			name:      "blank_claude_session",
			userAgent: schemas.ClaudeCLI.String(),
			headers: map[string]string{
				claudeCodeSessionIDHeader: "   ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := complexityHarnessContext(tt.userAgent, tt.headers)
			gotID, gotSource, gotPresent := ResolveSessionID(ctx, []string{configstore.ComplexitySessionIdentityHarness})
			assert.Equal(t, tt.wantPresent, gotPresent)
			assert.Equal(t, tt.wantID, gotID)
			if tt.wantPresent {
				assert.Equal(t, configstore.ComplexitySessionIdentityHarness, gotSource)
			} else {
				assert.Empty(t, gotSource)
			}
		})
	}
}

func TestResolveSessionIDInvalidHeaderFallsThrough(t *testing.T) {
	ctx := complexityHarnessContext(schemas.ClaudeCLI.String(), map[string]string{
		claudeCodeSessionIDHeader: "native-session",
	})
	ctx.SetValue(schemas.BifrostContextKeySessionID, strings.Repeat("a", maxComplexitySessionIDBytes+1))

	id, source, ok := ResolveSessionID(ctx, []string{
		configstore.ComplexitySessionIdentityHeader,
		configstore.ComplexitySessionIdentityHarness,
	})

	require.True(t, ok)
	assert.Equal(t, "native-session", id)
	assert.Equal(t, configstore.ComplexitySessionIdentityHarness, source)
}

func TestResolveSessionIDMalformedHarnessReturnsNoIdentity(t *testing.T) {
	ctx := complexityHarnessContext(schemas.CodexCLI.String(), map[string]string{
		codexTurnMetadataHeader: "{",
	})

	id, source, ok := ResolveSessionID(ctx, []string{configstore.ComplexitySessionIdentityHarness})

	assert.False(t, ok)
	assert.Empty(t, id)
	assert.Empty(t, source)
}

func TestResolveSessionIDPrecedence(t *testing.T) {
	ctx := complexityHarnessContext(schemas.ClaudeCLI.String(), map[string]string{
		claudeCodeSessionIDHeader: " native-session ",
	})
	ctx.SetValue(schemas.BifrostContextKeySessionID, " explicit-session ")

	tests := []struct {
		name        string
		sources     []string
		wantID      string
		wantSource  string
		wantPresent bool
	}{
		{
			name: "header_wins_regardless_of_config_order",
			sources: []string{
				configstore.ComplexitySessionIdentityHarness,
				configstore.ComplexitySessionIdentityHeader,
			},
			wantID:      "explicit-session",
			wantSource:  configstore.ComplexitySessionIdentityHeader,
			wantPresent: true,
		},
		{
			name: "harness_wins_when_header_is_disabled",
			sources: []string{
				configstore.ComplexitySessionIdentityHarness,
			},
			wantID:      "native-session",
			wantSource:  configstore.ComplexitySessionIdentityHarness,
			wantPresent: true,
		},
		{
			name:    "no_enabled_sources",
			sources: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotSource, gotPresent := ResolveSessionID(ctx, tt.sources)
			assert.Equal(t, tt.wantPresent, gotPresent)
			assert.Equal(t, tt.wantSource, gotSource)
			if tt.wantPresent {
				assert.Equal(t, tt.wantID, gotID)
			} else {
				assert.Empty(t, gotID)
			}
		})
	}
}

func testComplexitySessionKey(t *testing.T, scopeID, sessionID string) string {
	t.Helper()
	key, ok := BuildSessionKey(scopeID, sessionID)
	require.True(t, ok)
	return key
}

func complexitySessionChatRequest(systemText string, userTexts ...string) *schemas.BifrostRequest {
	messages := make([]schemas.ChatMessage, 0, len(userTexts)+1)
	if systemText != "" {
		messages = append(messages, schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleSystem,
			Content: complexityChatString(systemText),
		})
	}
	for _, userText := range userTexts {
		messages = append(messages, schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleUser,
			Content: complexityChatString(userText),
		})
	}
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Input: messages},
	}
}
