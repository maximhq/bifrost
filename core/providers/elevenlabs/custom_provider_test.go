package elevenlabs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLogger is a minimal logger implementation for testing.
type testLogger struct{}

func (l *testLogger) Debug(msg string, args ...any)                     {}
func (l *testLogger) Info(msg string, args ...any)                      {}
func (l *testLogger) Warn(msg string, args ...any)                      {}
func (l *testLogger) Error(msg string, args ...any)                     {}
func (l *testLogger) Fatal(msg string, args ...any)                     {}
func (l *testLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *testLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *testLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

const customElevenlabsProviderName = schemas.ModelProvider("custom-elevenlabs")

func TestElevenlabsProvider_CustomAliasListModelsReportsAliasMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`[{"model_id":"eleven_multilingual_v2","name":"Eleven Multilingual v2"}]`))
		require.NoError(t, err)
	}))
	defer server.Close()

	provider := NewElevenlabsProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: string(customElevenlabsProviderName),
			BaseProviderType:  schemas.Elevenlabs,
		},
	}, &testLogger{})

	assert.Equal(t, customElevenlabsProviderName, provider.GetProviderKey())

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostListModelsRequest{
		Provider:   customElevenlabsProviderName,
		Unfiltered: true,
	}

	response, bifrostErr := provider.ListModels(ctx, []schemas.Key{{}}, request)
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)
	require.Len(t, response.Data, 1)
	assert.Equal(t, "custom-elevenlabs/eleven_multilingual_v2", response.Data[0].ID)
}

func TestElevenlabsProvider_CustomAliasHonorsAllowedRequests(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("request should not reach the server when the operation is disallowed")
	}))
	defer server.Close()

	provider := NewElevenlabsProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: string(customElevenlabsProviderName),
			BaseProviderType:  schemas.Elevenlabs,
			AllowedRequests: &schemas.AllowedRequests{
				Speech: true,
			},
		},
	}, &testLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostListModelsRequest{
		Provider: customElevenlabsProviderName,
	}

	response, bifrostErr := provider.ListModels(ctx, []schemas.Key{{}}, request)
	require.Nil(t, response)
	require.NotNil(t, bifrostErr)
}
