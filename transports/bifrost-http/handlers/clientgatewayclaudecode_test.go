package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestClaudeCodeGateway_ManagedSettings(t *testing.T) {
	h, _, _, cfg := newClaudeCodeGateway(t, false)

	t.Run("no policy configured", func(t *testing.T) {
		ctx := getCtx("/gateway/claude-code/managed/settings")
		claudeCodeProfile{}.managedSettingsHandler(h.store)(ctx)
		assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
	})

	cfg.ClientConfig.OAuth2ServerConfig.ClaudeCodeGateway.ManagedSettings = map[string]any{
		"permissions": map[string]any{"deny": []any{"Bash(curl <url>)"}},
		"env":         map[string]any{"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"},
	}
	// The checksum Claude Code computes: sha256 over key-sorted JSON with no
	// whitespace and no HTML escaping.
	canonical := `{"env":{"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY":"1"},"permissions":{"deny":["Bash(curl <url>)"]}}`
	sum := sha256.Sum256([]byte(canonical))
	wantChecksum := "sha256:" + hex.EncodeToString(sum[:])

	t.Run("policy is served with its checksum", func(t *testing.T) {
		ctx := getCtx("/gateway/claude-code/managed/settings")
		claudeCodeProfile{}.managedSettingsHandler(h.store)(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		body := jsonBody(t, ctx)
		assert.Equal(t, wantChecksum, body["checksum"])
		assert.Equal(t, wantChecksum, body["uuid"])
		assert.Equal(t, `"`+wantChecksum+`"`, string(ctx.Response.Header.Peek("ETag")))
		assert.Contains(t, body["settings"], "permissions")
	})

	t.Run("unchanged policy answers 304", func(t *testing.T) {
		ctx := getCtx("/gateway/claude-code/managed/settings")
		ctx.Request.Header.Set("If-None-Match", `"`+wantChecksum+`"`)
		claudeCodeProfile{}.managedSettingsHandler(h.store)(ctx)
		assert.Equal(t, fasthttp.StatusNotModified, ctx.Response.StatusCode())
	})

	t.Run("an empty policy is still a policy", func(t *testing.T) {
		cfg.ClientConfig.OAuth2ServerConfig.ClaudeCodeGateway.ManagedSettings = map[string]any{}
		ctx := getCtx("/gateway/claude-code/managed/settings")
		claudeCodeProfile{}.managedSettingsHandler(h.store)(ctx)
		assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	})
}
