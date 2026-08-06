package vertex

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLogger is a minimal logger implementation for testing (mirrors the
// pattern used in providers/mistral/transcription_test.go).
type testLogger struct{}

func (l *testLogger) Debug(msg string, args ...any)                     {}
func (l *testLogger) Info(msg string, args ...any)                      {}
func (l *testLogger) Warn(msg string, args ...any)                      {}
func (l *testLogger) Error(msg string, args ...any)                     {}
func (l *testLogger) Fatal(msg string, args ...any)                     {}
func (l *testLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *testLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *testLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return nil
}

const customVertexProviderName = schemas.ModelProvider("custom-vertex")

// dummyChatRequest returns a minimal, well-formed BifrostChatRequest suitable
// for exercising the CheckOperationAllowed gate in ChatCompletion. Its
// contents are irrelevant because the gate check happens before the request
// body is ever inspected or any network/auth call is made.
func dummyChatRequest(provider schemas.ModelProvider) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    "gemini-1.5-flash",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentStr: schemas.Ptr("hello"),
			},
		}},
	}
}

// TestVertexCustomProviderKey verifies that a Vertex provider constructed with
// a CustomProviderConfig reports the custom provider key from GetProviderKey(),
// instead of always returning schemas.Vertex. This is a pure unit test: no
// HTTP server and no GCP auth is required since GetProviderKey never touches
// the network.
func TestVertexCustomProviderKey(t *testing.T) {
	t.Parallel()

	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: string(customVertexProviderName),
			BaseProviderType:  schemas.Vertex,
		},
	}, &testLogger{})
	require.NoError(t, err)
	require.NotNil(t, provider)

	assert.Equal(t, customVertexProviderName, provider.GetProviderKey())
	assert.NotEqual(t, schemas.Vertex, provider.GetProviderKey())
}

// TestVertexProviderKey_NoCustomConfig verifies the default (non-custom)
// behavior is preserved: with no CustomProviderConfig, GetProviderKey()
// returns schemas.Vertex.
func TestVertexProviderKey_NoCustomConfig(t *testing.T) {
	t.Parallel()

	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{},
	}, &testLogger{})
	require.NoError(t, err)
	require.NotNil(t, provider)

	assert.Equal(t, schemas.Vertex, provider.GetProviderKey())
}

// TestVertexCustomProvider_ChatCompletionGatedByAllowedRequests verifies that
// when a custom Vertex provider's AllowedRequests does NOT include
// ChatCompletion, calling ChatCompletion returns the exact BifrostError
// produced by providerUtils.CheckOperationAllowed / NewUnsupportedOperationError
// — and that this happens before any auth/network call, so no real GCP
// credentials are needed for this test to pass or fail deterministically.
func TestVertexCustomProvider_ChatCompletionGatedByAllowedRequests(t *testing.T) {
	t.Parallel()

	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: string(customVertexProviderName),
			BaseProviderType:  schemas.Vertex,
			AllowedRequests: &schemas.AllowedRequests{
				// ChatCompletion intentionally omitted (defaults to false).
				ListModels: true,
			},
		},
	}, &testLogger{})
	require.NoError(t, err)
	require.NotNil(t, provider)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	// A key with no VertexKeyConfig at all — if the gate did not fire first,
	// this would panic/error out deep in auth code rather than at the gate,
	// which would also fail this test (for the wrong reason).
	key := schemas.Key{}
	request := dummyChatRequest(customVertexProviderName)

	resp, bifrostErr := provider.ChatCompletion(ctx, key, request)
	require.Nil(t, resp)
	require.NotNil(t, bifrostErr)

	// Match the exact shape produced by providerUtils.NewUnsupportedOperationError
	// as constructed inside providerUtils.CheckOperationAllowed.
	require.NotNil(t, bifrostErr.Error)
	assert.False(t, bifrostErr.IsBifrostError)
	assert.Equal(t, schemas.Ptr("unsupported_operation"), bifrostErr.Error.Code)
	assert.Equal(t,
		fmt.Sprintf("%s is not supported by %s provider", schemas.ChatCompletionRequest, customVertexProviderName),
		bifrostErr.Error.Message,
	)
	assert.Equal(t, customVertexProviderName, bifrostErr.ExtraFields.Provider)
	assert.Equal(t, schemas.ChatCompletionRequest, bifrostErr.ExtraFields.RequestType)
}

// TestVertexCustomProvider_MultiInstanceIsolation is the concrete acceptance
// test for the whole feature: two independently configured custom Vertex
// providers ("vertex-prod" and "vertex-exp") must be constructible
// side-by-side, each reporting its own distinct GetProviderKey(), and each
// enforcing its own independently configured AllowedRequests gating without
// any shared state leaking between instances.
func TestVertexCustomProvider_MultiInstanceIsolation(t *testing.T) {
	t.Parallel()

	prodProvider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "vertex-prod",
			BaseProviderType:  schemas.Vertex,
			AllowedRequests: &schemas.AllowedRequests{
				ChatCompletion: true,
			},
		},
	}, &testLogger{})
	require.NoError(t, err)
	require.NotNil(t, prodProvider)

	expProvider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "vertex-exp",
			BaseProviderType:  schemas.Vertex,
			AllowedRequests: &schemas.AllowedRequests{
				// ChatCompletion intentionally omitted for the "exp" instance.
				ListModels: true,
			},
		},
	}, &testLogger{})
	require.NoError(t, err)
	require.NotNil(t, expProvider)

	// Distinct instances, distinct provider keys.
	assert.NotSame(t, prodProvider, expProvider)
	assert.Equal(t, schemas.ModelProvider("vertex-prod"), prodProvider.GetProviderKey())
	assert.Equal(t, schemas.ModelProvider("vertex-exp"), expProvider.GetProviderKey())
	assert.NotEqual(t, prodProvider.GetProviderKey(), expProvider.GetProviderKey())

	// Each instance holds its own customProviderConfig struct — not a shared pointer.
	require.NotNil(t, prodProvider.customProviderConfig)
	require.NotNil(t, expProvider.customProviderConfig)
	assert.NotSame(t, prodProvider.customProviderConfig, expProvider.customProviderConfig)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{}

	// exp's gate should fire immediately with the gating error (ChatCompletion
	// not allowed for this instance).
	expResp, expErr := expProvider.ChatCompletion(ctx, key, dummyChatRequest(expProvider.GetProviderKey()))
	require.Nil(t, expResp)
	require.NotNil(t, expErr)
	assert.Equal(t, schemas.Ptr("unsupported_operation"), expErr.Error.Code)
	assert.Equal(t,
		fmt.Sprintf("%s is not supported by %s provider", schemas.ChatCompletionRequest, expProvider.GetProviderKey()),
		expErr.Error.Message,
	)

	// prod's gate should NOT fire for ChatCompletion — it should proceed past
	// gating and fail later for a configuration/auth reason instead (no
	// project ID / region / credentials configured on the dummy key), proving
	// the two instances' gating state is independent.
	prodResp, prodErr := prodProvider.ChatCompletion(ctx, key, dummyChatRequest(prodProvider.GetProviderKey()))
	require.Nil(t, prodResp)
	require.NotNil(t, prodErr)
	require.NotNil(t, prodErr.Error)
	assert.NotEqual(t, schemas.Ptr("unsupported_operation"), prodErr.Error.Code)
	assert.NotEqual(t,
		fmt.Sprintf("%s is not supported by %s provider", schemas.ChatCompletionRequest, prodProvider.GetProviderKey()),
		prodErr.Error.Message,
	)
}
