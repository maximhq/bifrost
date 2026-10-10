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

// aliasLiveRunner grants the caller aliases, not their provider model identifiers.
type aliasLiveRunner struct {
	inner   *fakeLiveRunner
	allowed []string
}

func (r aliasLiveRunner) RunRealtimeTurnPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*bifrost.RealtimeTurnHooks, *schemas.BifrostError) {
	_, model, _ := req.GetRequestFields()
	if !slices.Contains(r.allowed, model) {
		return nil, newRealtimeWireBifrostError(403, "model_blocked", "model is not allowed")
	}
	return r.inner.RunRealtimeTurnPreHooks(ctx, req)
}

type aliasLiveIO struct{ messages [][]byte }

func (io *aliasLiveIO) sendUpstream(message []byte) error {
	io.messages = append(io.messages, message)
	return nil
}
func (io *aliasLiveIO) sendClient(message []byte) error {
	io.messages = append(io.messages, message)
	return nil
}
func (*aliasLiveIO) abandonUpstream() {}

func TestLiveSessionUpdatedRetainsAdmittedAlias(t *testing.T) {
	t.Parallel()
	for _, initial := range []string{"terra", "gpt-5.6-luna"} {
		t.Run(initial, func(t *testing.T) {
			runner := &fakeLiveRunner{}
			meter := newTestLiveMeter(runner)
			meter.runner = aliasLiveRunner{inner: runner, allowed: []string{"gpt-live-1", "gpt-5.6-luna", "terra", "terra-other"}}
			require.Nil(t, meter.admit("gpt-live-1", initial))
			key := schemas.Key{ID: "key-1", Aliases: schemas.KeyAliases{
				"terra": {ModelID: "gpt-5.6-terra"}, "terra-other": {ModelID: "gpt-5.6-terra"},
				"forbidden-alias": {ModelID: "gpt-5.6-sol"},
			}}
			// The key is selected after the initial units have been admitted.
			meter.setKey(key)
			wire := &aliasLiveIO{}
			controller := newTestLiveController(fakeLiveModels{allowed: true}, meter, wire)
			controller.key = key
			defer controller.markUpstreamDone()
			defer meter.finish(0)
			update := []byte(`{"type":"session.update","session":{"delegation":{"responses":{"model":"openai/terra"}}}}`)
			checked, accepted := controller.fromClient(update)
			require.True(t, accepted)
			assert.Contains(t, string(checked), `"model":"gpt-5.6-terra"`)
			opens, _, _ := runner.snapshot()
			ack := []byte(`{"type":"session.updated","session":{"delegation":{"responses":{"model":"gpt-5.6-terra"}}}}`)
			for i := 0; i < 2; i++ {
				assert.False(t, controller.fromUpstream(ack))
			}
			after, _, _ := runner.snapshot()
			require.Empty(t, wire.messages, "an acknowledged alias must not send an error or session.close")
			assert.Len(t, after, len(opens), "acknowledgments do not open another billing lane")
			assert.Equal(t, "terra", meter.activeBackend)

			// Two admitted aliases can resolve to the same provider model. The active alias wins.
			_, accepted = controller.fromClient([]byte(`{"type":"session.update","session":{"delegation":{"responses":{"model":"terra-other"}}}}`))
			require.True(t, accepted)
			controller.fromUpstream(ack)
			assert.Equal(t, "terra-other", meter.activeBackend)
			require.Empty(t, wire.messages)

			// A refused update must not acquire an identity that later bypasses authorization.
			_, accepted = controller.fromClient([]byte(`{"type":"session.update","session":{"delegation":{"responses":{"model":"forbidden-alias"}}}}`))
			require.False(t, accepted)
			require.Len(t, wire.messages, 1)
			wire.messages = nil
			controller.fromUpstream(ack)
			assert.Equal(t, "terra-other", meter.activeBackend)
			require.Empty(t, wire.messages)
			controller.fromUpstream([]byte(`{"type":"session.updated","session":{"delegation":{"responses":{"model":"gpt-5.6-sol"}}}}`))
			assert.Equal(t, "terra-other", meter.activeBackend)
			require.Len(t, wire.messages, 2, "an unknown sideband model still triggers authorization and refusal")
			assert.Contains(t, string(wire.messages[0]), "model is not allowed")
			assert.Equal(t, `{"type":"session.close"}`, string(wire.messages[1]))
		})
	}
}

func TestLiveMeterAliasResponsesKeepTheirBillingLane(t *testing.T) {
	t.Parallel()
	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	meter.runner = aliasLiveRunner{inner: runner, allowed: []string{"gpt-live-1", "luna", "terra"}}
	require.Nil(t, meter.admit("gpt-live-1", "luna"))
	meter.setKey(schemas.Key{ID: "key-1", Aliases: schemas.KeyAliases{
		"luna": {ModelID: "gpt-5.6-luna"}, "terra": {ModelID: "gpt-5.6-terra"},
	}})
	require.Nil(t, meter.switchBackend("terra"))
	// An older response can finish after a backend switch, including a dated provider name.
	require.Nil(t, meter.onBackendResponse(&schemas.BifrostResponsesResponse{
		ID: new("resp_luna"), Model: "gpt-5.6-luna-2026-10-01",
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
	}, "item_luna", 0))
	_, posts, _ := runner.snapshot()
	require.Len(t, posts, 1)
	assert.Equal(t, "luna", posts[0].model, "the old response must not be billed to the new active alias")
	assert.Equal(t, "gpt-5.6-luna", posts[0].resp.Model)
	_, _, requested, resolved := bifrost.GetResponseFields(&schemas.BifrostResponse{ResponsesResponse: posts[0].resp}, nil)
	assert.Equal(t, "luna", requested)
	assert.Equal(t, "gpt-5.6-luna", resolved)
	assert.Equal(t, 10, posts[0].resp.Usage.TotalTokens)
	assert.Equal(t, "terra", meter.activeBackend)
	meter.finish(0)
	opens, _, cleanups := runner.snapshot()
	assert.Equal(t, len(opens), cleanups)
}

type liveAliasMetadataPlugin struct {
	allowedKeysPlugin
	seen [2]string
}

func (p *liveAliasMetadataPlugin) PostLLMHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	_, _, requested, resolved := bifrost.GetResponseFields(result, err)
	p.seen = [2]string{requested, resolved}
	return result, err, nil
}

func TestLiveAliasedPostHooksPreserveModelIdentity(t *testing.T) {
	plugin := &liveAliasMetadataPlugin{allowedKeysPlugin: allowedKeysPlugin{ids: []string{"every-model"}}}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: splitKeysAccount{}, LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger: bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()
	for _, requestType := range []schemas.RequestType{schemas.LiveRequest, schemas.RealtimeRequest} {
		t.Run(string(requestType), func(t *testing.T) {
			for _, failed := range []bool{false, true} {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "terra", Config: &schemas.AliasConfig{ModelID: "gpt-5.6-terra"}})
				hooks, bifrostErr := client.RunRealtimeTurnPreHooks(ctx, &schemas.BifrostRequest{
					RequestType: requestType, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "terra"},
				})
				require.Nil(t, bifrostErr)
				defer hooks.Cleanup()
				result := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Model: "gpt-5.6-terra"}}
				var unitErr *schemas.BifrostError
				if failed {
					result = nil
					unitErr = newRealtimeWireBifrostError(502, "server_error", "backend failed")
				}
				result, unitErr = hooks.PostHookRunner(ctx, result, unitErr)
				resolved := "terra"
				if requestType == schemas.LiveRequest {
					resolved = "gpt-5.6-terra"
				}
				assert.Equal(t, [2]string{"terra", resolved}, plugin.seen, "post-hooks receive both identities; Realtime keeps its existing behavior")
				_, _, requested, actual := bifrost.GetResponseFields(result, unitErr)
				assert.Equal(t, "terra", requested)
				assert.Equal(t, resolved, actual)
			}
		})
	}
}

func TestLiveMeterAliasResponsePrefersExactModelOverSnapshot(t *testing.T) {
	t.Parallel()
	meter := newTestLiveMeter(&fakeLiveRunner{})
	require.Nil(t, meter.admit("gpt-live-1", "mini"))
	meter.setKey(schemas.Key{Aliases: schemas.KeyAliases{
		"terra": {ModelID: "gpt-5.6-terra"}, "mini": {ModelID: "gpt-5.6-terra-mini"},
	}})
	require.Nil(t, meter.switchBackend("terra"))
	defer meter.finish(0)
	assert.Equal(t, "mini", meter.backendLaneForLocked("gpt-5.6-terra-mini").model,
		"an exact admitted model must win over another model's snapshot prefix")
}
