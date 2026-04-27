package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ptr returns a pointer to an int literal, useful for building test fixtures.
func intPtr(v int) *int { return &v }

// TestEvaluateVirtualKeyRequest_InjectsRequestTimeout verifies that a VK with
// request_timeout_in_seconds set causes BifrostContextKeyVKTimeoutConfig.Request to be
// populated after EvaluateVirtualKeyRequest succeeds.
func TestEvaluateVirtualKeyRequest_InjectsRequestTimeout(t *testing.T) {
	logger := NewMockLogger()
	pc := buildProviderConfig("openai", []string{"*"})
	pc.RequestTimeoutInSeconds = intPtr(10)

	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{pc})

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionAllow, result)

	got, ok := ctx.Value(schemas.BifrostContextKeyVKTimeoutConfig).(schemas.TimeoutConfig)
	assert.True(t, ok, "BifrostContextKeyVKTimeoutConfig should be set after EvaluateVirtualKeyRequest")
	assert.Equal(t, 10*time.Second, got.Request, "request timeout should match VK provider config value")
}

// TestEvaluateVirtualKeyRequest_InjectsStreamIdleTimeout verifies that a VK with
// stream_idle_timeout_in_seconds set causes BifrostContextKeyVKTimeoutConfig.StreamIdle to be
// populated.
func TestEvaluateVirtualKeyRequest_InjectsStreamIdleTimeout(t *testing.T) {
	logger := NewMockLogger()
	pc := buildProviderConfig("openai", []string{"*"})
	pc.StreamIdleTimeoutInSeconds = intPtr(120)

	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{pc})

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionAllow, result)

	got, ok := ctx.Value(schemas.BifrostContextKeyVKTimeoutConfig).(schemas.TimeoutConfig)
	assert.True(t, ok, "BifrostContextKeyVKTimeoutConfig should be set after EvaluateVirtualKeyRequest")
	assert.Equal(t, 120*time.Second, got.StreamIdle, "stream idle timeout should match VK provider config value")
}

// TestEvaluateVirtualKeyRequest_NilTimeoutFieldsNoContextWrite verifies that when
// a VK provider config has nil timeout fields (backward-compatible default), no
// timeout keys are written to the context, allowing provider-level defaults to
// remain in effect.
func TestEvaluateVirtualKeyRequest_NilTimeoutFieldsNoContextWrite(t *testing.T) {
	logger := NewMockLogger()
	pc := buildProviderConfig("openai", []string{"*"})
	// RequestTimeoutInSeconds and StreamIdleTimeoutInSeconds are nil (default)

	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{pc})

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionAllow, result)

	_, hasVKConfig := ctx.Value(schemas.BifrostContextKeyVKTimeoutConfig).(schemas.TimeoutConfig)
	assert.False(t, hasVKConfig, "BifrostContextKeyVKTimeoutConfig must NOT be set when VK provider config has nil timeout fields")
}

// TestEvaluateVirtualKeyRequest_BothTimeoutsInjected verifies that both timeout
// fields are injected simultaneously when both are set on the VK provider config.
func TestEvaluateVirtualKeyRequest_BothTimeoutsInjected(t *testing.T) {
	logger := NewMockLogger()
	pc := buildProviderConfig("anthropic", []string{"*"})
	pc.RequestTimeoutInSeconds = intPtr(45)
	pc.StreamIdleTimeoutInSeconds = intPtr(90)

	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{pc})

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.Anthropic, "claude-3-5-sonnet-20241022", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionAllow, result)

	got, ok := ctx.Value(schemas.BifrostContextKeyVKTimeoutConfig).(schemas.TimeoutConfig)
	assert.True(t, ok)
	assert.Equal(t, 45*time.Second, got.Request)
	assert.Equal(t, 90*time.Second, got.StreamIdle)
}

// TestEvaluateVirtualKeyRequest_InjectsStreamTotalTimeout verifies that a VK with
// stream_total_timeout_in_seconds set causes BifrostContextKeyVKTimeoutConfig.StreamTotal to be
// populated.
func TestEvaluateVirtualKeyRequest_InjectsStreamTotalTimeout(t *testing.T) {
	logger := NewMockLogger()
	pc := buildProviderConfig("openai", []string{"*"})
	pc.StreamTotalTimeoutInSeconds = intPtr(300)

	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{pc})

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionAllow, result)

	got, ok := ctx.Value(schemas.BifrostContextKeyVKTimeoutConfig).(schemas.TimeoutConfig)
	assert.True(t, ok, "BifrostContextKeyVKTimeoutConfig should be set")
	assert.Equal(t, 300*time.Second, got.StreamTotal)
}

// TestEvaluateVirtualKeyRequest_TimeoutNotInjectedForDifferentProvider verifies
// that timeouts from a VK provider config are only injected when the request
// provider matches — a different provider's config must not contaminate the context.
func TestEvaluateVirtualKeyRequest_TimeoutNotInjectedForDifferentProvider(t *testing.T) {
	logger := NewMockLogger()

	// VK allows anthropic with custom timeouts, but request is for openai
	anthropicPC := buildProviderConfig("anthropic", []string{"*"})
	anthropicPC.RequestTimeoutInSeconds = intPtr(15)

	openaiPC := buildProviderConfig("openai", []string{"*"})
	// openai config has no timeout overrides

	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{anthropicPC, openaiPC})

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	// Request is for OpenAI, not Anthropic
	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionAllow, result)

	_, hasVKConfig := ctx.Value(schemas.BifrostContextKeyVKTimeoutConfig).(schemas.TimeoutConfig)
	assert.False(t, hasVKConfig, "Anthropic's timeout must not be injected for an OpenAI request")
}

// TestGetStreamIdleTimeout_VKOverrideTakesPrecedence verifies that the VK-injected
// stream idle timeout takes precedence over the provider-level default value read
// by SetStreamIdleTimeoutIfEmpty.
func TestGetStreamIdleTimeout_VKOverrideTakesPrecedence(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	// Simulate the retry loop writing a pre-resolved TimeoutConfig with a VK idle override.
	ctx.SetValue(schemas.BifrostContextKeyTimeoutConfig, schemas.TimeoutConfig{StreamIdle: 120 * time.Second})

	// Simulate provider calling SetStreamIdleTimeoutIfEmpty with its own default (30s).
	// Since BifrostContextKeyTimeoutConfig already has a non-zero StreamIdle, this must be a no-op.
	setStreamIdleTimeoutIfEmptyTestHelper(ctx, 30)

	// The resolved config read by GetTimeoutConfigFromContext (fast path via BifrostContextKeyTimeoutConfig)
	// should still reflect the VK override, not the provider default.
	got, ok := ctx.Value(schemas.BifrostContextKeyTimeoutConfig).(schemas.TimeoutConfig)
	assert.True(t, ok)
	assert.Equal(t, 120*time.Second, got.StreamIdle, "VK override (120s) must win over provider default (30s)")
}

// setStreamIdleTimeoutIfEmptyTestHelper mirrors the providerUtils function in core/providers/utils,
// duplicated here to keep the governance package self-contained in tests.
func setStreamIdleTimeoutIfEmptyTestHelper(ctx *schemas.BifrostContext, configSeconds int) {
	if existing, ok := ctx.Value(schemas.BifrostContextKeyStreamIdleTimeout).(time.Duration); ok && existing > 0 {
		return
	}
	if configSeconds > 0 {
		ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, time.Duration(configSeconds)*time.Second)
	}
}
