package handlers

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveBackendModelAndRewrite(t *testing.T) {
	t.Parallel()

	start := &schemas.LiveSession{
		Model: "openai/gpt-live-1",
		Delegation: &schemas.LiveDelegationConfig{
			Type:      schemas.LiveDelegationResponses,
			Responses: &schemas.LiveResponsesDelegation{Model: "openai/luna"},
		},
	}
	model, err := liveBackendModel(start, schemas.OpenAI)
	require.NoError(t, err)
	assert.Equal(t, "luna", model)

	key := schemas.Key{Aliases: schemas.KeyAliases{"luna": {ModelID: "gpt-5.6-luna"}}}
	frame := []byte(`{"type":"session.start","session":{"model":"openai/gpt-live-1","delegation":{"type":"responses","responses":{"model":"openai/luna","tools":[{"type":"web_search"}]}}}}`)
	rewritten, err := rewriteLiveModels(frame, start, key, "gpt-live-1", model)
	require.NoError(t, err)
	assert.Equal(t, `{"type":"session.start","session":{"model":"gpt-live-1","delegation":{"type":"responses","responses":{"model":"gpt-5.6-luna","tools":[{"type":"web_search"}]}}}}`, string(rewritten))

	// A bare, unaliased frame is sent exactly as received.
	bare := []byte(`{"type":"session.start","session":{"model":"gpt-live-1"}}`)
	unchanged, err := rewriteLiveModels(bare, &schemas.LiveSession{Model: "gpt-live-1"}, schemas.Key{}, "gpt-live-1", "")
	require.NoError(t, err)
	assert.Equal(t, string(bare), string(unchanged))

	// Client delegation has no backend on the socket.
	model, err = liveBackendModel(&schemas.LiveSession{Delegation: &schemas.LiveDelegationConfig{Type: schemas.LiveDelegationClient}}, schemas.OpenAI)
	require.NoError(t, err)
	assert.Empty(t, model)

	_, err = liveBackendModel(&schemas.LiveSession{Delegation: &schemas.LiveDelegationConfig{Type: schemas.LiveDelegationResponses, Responses: &schemas.LiveResponsesDelegation{Model: "anthropic/claude-opus-5"}}}, schemas.OpenAI)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "must be served by openai"))
}
