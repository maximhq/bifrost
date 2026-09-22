package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestValidateGlobalToolSyncIntervalMinutes pins the client-config
// mcp_tool_sync_interval bounds: 0 (built-in default) and ordinary positive
// minute counts up to the minutes->Duration overflow edge are accepted;
// negatives and anything past the edge are rejected.
func TestValidateGlobalToolSyncIntervalMinutes(t *testing.T) {
	for _, minutes := range []int{0, 1, 10, 1440, int(maxToolSyncIntervalMinutes)} {
		if err := validateGlobalToolSyncIntervalMinutes(minutes); err != nil {
			t.Errorf("mcp_tool_sync_interval=%d minutes should be accepted, got %v", minutes, err)
		}
	}
	for _, minutes := range []int{-1, -10, int(maxToolSyncIntervalMinutes) + 1} {
		if err := validateGlobalToolSyncIntervalMinutes(minutes); err == nil {
			t.Errorf("mcp_tool_sync_interval=%d minutes should be rejected", minutes)
		}
	}
}

func TestUpdateConfig_MCPToolSyncIntervalPresence(t *testing.T) {
	tests := []struct {
		name          string
		intervalField string
		wantInterval  int
	}{
		{
			name:         "omitted preserves stored interval",
			wantInterval: 15,
		},
		{
			name:          "explicit zero restores built-in default",
			intervalField: `,"mcp_tool_sync_interval":0`,
			wantInterval:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			handler, store := newConfigHandlerAuthTest(t)

			storedConfig, err := store.GetClientConfig(context.Background())
			require.NoError(t, err)
			require.NotNil(t, storedConfig)
			storedConfig.MCPToolSyncInterval = 15
			require.NoError(t, store.UpdateClientConfig(context.Background(), storedConfig))
			store.updateClientConfigCalls = 0
			handler.store.ClientConfig.MCPToolSyncInterval = 15

			ctx := putConfigCtx(`{
				"client_config":{
					"allowed_headers":["X-Existing"],
					"log_retention_days":30,
					"compat":{}` + tt.intervalField + `
				}
			}`)
			handler.updateConfig(ctx)

			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			persistedConfig, err := store.GetClientConfig(context.Background())
			require.NoError(t, err)
			require.NotNil(t, persistedConfig)
			assert.Equal(t, tt.wantInterval, persistedConfig.MCPToolSyncInterval)
			assert.Equal(t, tt.wantInterval, handler.store.ClientConfig.MCPToolSyncInterval)
		})
	}
}
