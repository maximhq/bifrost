package handlers

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/tracing"
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

// splitKeysAccount exposes OpenAI with a key per model and one key for every model, so key
// selection has a choice governance can narrow.
type splitKeysAccount struct{}

func (splitKeysAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

// GetKeysForProvider honours the keys governance allowed on the context, as the transport's
// account does.
func (splitKeysAccount) GetKeysForProvider(ctx context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if p != schemas.OpenAI {
		return nil, nil
	}
	keys := []schemas.Key{
		{ID: "voice-only", Name: "voice-only", Value: *schemas.NewSecretVar("sk-voice"), Models: schemas.WhiteList{"gpt-live-1"}, Weight: 1.0},
		{ID: "backend-only", Name: "backend-only", Value: *schemas.NewSecretVar("sk-backend"), Models: schemas.WhiteList{"gpt-5.6-luna"}, Weight: 1.0},
		{ID: "every-model", Name: "every-model", Value: *schemas.NewSecretVar("sk-every"), Models: schemas.WhiteList{"*"}, Weight: 1.0},
	}
	allowed, restricted := ctx.Value(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys).([]string)
	if !restricted {
		return keys, nil
	}
	return slices.DeleteFunc(keys, func(key schemas.Key) bool { return !slices.Contains(allowed, key.ID) }), nil
}

func (splitKeysAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != schemas.OpenAI {
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// allowedKeysPlugin narrows a request to the keys its virtual key may use, as governance does,
// and records the key each unit was billed on.
type allowedKeysPlugin struct {
	ids    []string
	billed *[]string
}

func (allowedKeysPlugin) GetName() string { return "allowed-keys" }
func (allowedKeysPlugin) Cleanup() error  { return nil }
func (allowedKeysPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p allowedKeysPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	ctx.SetValue(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys, p.ids)
	return req, nil, nil
}
func (p allowedKeysPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	keyID, _ := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string)
	*p.billed = append(*p.billed, keyID)
	return resp, bifrostErr, nil
}

// A live session's key is chosen from the keys governance allows its virtual key, and only
// after governance has admitted the session's units, since that is when the allowed keys are known.
func TestLiveAdmitSelectsAmongGovernanceAllowedKeys(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelWarn))
	cases := []struct {
		name    string
		allowed []string
		backend string
		wantKey string
		wantErr string
	}{
		{name: "keys serving one model each refuse a two-model session", allowed: []string{"voice-only", "backend-only"}, backend: "gpt-5.6-luna", wantErr: "no keys found"},
		{name: "the allowed key serving both models is chosen", allowed: []string{"voice-only", "every-model"}, backend: "gpt-5.6-luna", wantKey: "every-model"},
		{name: "a voice-only session takes the allowed voice key", allowed: []string{"voice-only"}, wantKey: "voice-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := tracing.NewTraceStore(5*time.Minute, nil)
			defer store.Stop()
			tracer := tracing.NewTracer(store, nil, nil)
			defer tracer.Stop()
			var billed []string
			client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
				Account:    splitKeysAccount{},
				LLMPlugins: []schemas.LLMPlugin{allowedKeysPlugin{ids: tc.allowed, billed: &billed}},
				Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
				Tracer:     tracer,
			})
			require.NoError(t, err)
			defer client.Shutdown()

			provider, ok := client.GetProviderByKey(schemas.OpenAI).(schemas.LiveProvider)
			require.True(t, ok, "the OpenAI provider serves live sessions")
			g := &liveGateway{client: client}
			target := liveTarget{provider: provider, providerKey: schemas.OpenAI, voiceModel: "gpt-live-1", backendModel: tc.backend}
			preReqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			admission, bifrostErr := g.admit(nil, preReqCtx, nil, "/v1/live/sessions", target, "live-admit-test")
			if tc.wantErr != "" {
				require.NotNil(t, bifrostErr, "a session no allowed key can serve must be refused")
				assert.Contains(t, bifrostErr.Error.Message, tc.wantErr)
				return
			}
			require.Nil(t, bifrostErr, "admission refused: %v", bifrostErr)
			defer admission.cancel()
			assert.Equal(t, tc.wantKey, admission.key.ID)
			// The units opened during admission bill on the selected key.
			admission.meter.abort(newRealtimeWireBifrostError(499, "cancelled", "test over"))
			require.NotEmpty(t, billed, "the admitted units ran their post-hooks")
			for _, keyID := range billed {
				assert.Equal(t, tc.wantKey, keyID)
			}
		})
	}
}
