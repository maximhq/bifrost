package server

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/telemetry"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
)

// builtinLoggingStore keeps startup and the log writer offline.
type builtinLoggingStore struct {
	logstore.LogStore
	entries chan *logstore.Log
}

func (s *builtinLoggingStore) BatchCreateIfNotExists(_ context.Context, entries []*logstore.Log) error {
	for _, entry := range entries {
		s.entries <- entry
	}
	return nil
}

func builtinLoggingServer(t *testing.T, pluginConfig *schemas.PluginConfig) (*BifrostHTTPServer, *builtinLoggingStore) {
	t.Helper()
	previousLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = previousLogger })
	store := &builtinLoggingStore{entries: make(chan *logstore.Log, 2)}
	config := &lib.Config{
		ClientConfig:    &configstore.ClientConfig{DisableContentLogging: true},
		LogsStore:       store,
		LogsStoreConfig: &logstore.Config{Writer: &logstore.WriterConfig{BatchInterval: "1h", WriteQueueCapacity: 4}},
		PluginConfigs:   []*schemas.PluginConfig{{Name: telemetry.PluginName, Enabled: false}},
	}
	if pluginConfig != nil {
		config.PluginConfigs = append(config.PluginConfigs, pluginConfig)
	}
	t.Cleanup(func() {
		for _, plugin := range config.GetLoadedLLMPlugins() {
			require.NoError(t, plugin.Cleanup())
		}
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIsEnterprise, true)
	t.Cleanup(ctx.Cancel)
	return &BifrostHTTPServer{Ctx: ctx, Config: config}, store
}

func TestLoadPluginsRequestCostReceipt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plugin  *schemas.PluginConfig
		receipt bool
	}{
		{name: "default"},
		{name: "empty plugin config", plugin: &schemas.PluginConfig{Name: logging.PluginName, Enabled: true}},
		{name: "explicit false", plugin: &schemas.PluginConfig{Name: logging.PluginName, Enabled: true, Config: map[string]any{"include_request_costs": false}}},
		{name: "disabled plugin cannot disclose", plugin: &schemas.PluginConfig{Name: logging.PluginName, Enabled: false, Config: map[string]any{"include_request_costs": true}}},
		{name: "enabled", receipt: true, plugin: &schemas.PluginConfig{Name: logging.PluginName, Enabled: true, Config: map[string]any{
			"include_request_costs":   true,
			"disable_content_logging": false,
			"writer":                  map[string]any{"batch_interval": "invalid"},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, store := builtinLoggingServer(t, tc.plugin)
			// Exclude unrelated governance initialization while exercising the real
			// built-in registration and subsequent custom-plugin skip path.
			startupCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyIsEnterprise, true)
			require.NoError(t, server.LoadPlugins(startupCtx))
			plugin, err := lib.FindPluginAs[*logging.LoggerPlugin](server.Config, logging.PluginName)
			require.NoError(t, err)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			t.Cleanup(ctx.Cancel)
			ctx.SetValue(schemas.BifrostContextKeyRequestID, "startup-request")
			ctx.SetValue(schemas.BifrostContextKeyTraceID, "startup-trace")
			_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "test-model", Params: &schemas.ChatParameters{}},
			})
			require.NoError(t, err)
			response := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
				Model: "test-model",
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI,
					OriginalModelRequested: "test-model", ResolvedModelUsed: "test-model",
					RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "test-model"},
				},
			}}
			_, _, err = plugin.PostLLMHook(ctx, response, nil)
			require.NoError(t, err)
			costs := response.GetExtraFields().RequestCosts
			if tc.receipt {
				require.NotNil(t, costs, "the configured built-in logger must expose a receipt")
				require.Equal(t, "USD", costs.Currency)
				require.Len(t, costs.Requests, 1)
				require.Equal(t, "startup-request", costs.Requests[0].RequestID)
				require.Nil(t, costs.Requests[0].AmountUSD, "no catalog or provider call is needed to test disclosure")
				require.False(t, costs.IsComplete)
			} else {
				require.Nil(t, costs)
			}
			require.NoError(t, plugin.Inject(context.Background(), &schemas.Trace{InternalID: "startup-trace"}))
			require.NoError(t, plugin.Cleanup())
			select {
			case entry := <-store.entries:
				require.True(t, entry.ContentHidden, "the plugin override must preserve the client's content policy")
			default:
				t.Fatal("the built-in logger did not write the request")
			}
		})
	}
}

func TestLoadPluginsRejectsInvalidRequestCostConfig(t *testing.T) {
	server, _ := builtinLoggingServer(t, &schemas.PluginConfig{
		Name: logging.PluginName, Enabled: true, Config: map[string]any{"include_request_costs": "yes"},
	})
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyIsEnterprise, true)
	require.ErrorContains(t, server.LoadPlugins(ctx), "failed to marshal logging plugin config")
	require.False(t, server.Config.IsPluginLoaded(logging.PluginName))
}

func (s *builtinLoggingStore) ListUserAgentMappings(context.Context, bool) ([]logstore.UserAgentMapping, error) {
	return nil, nil
}
