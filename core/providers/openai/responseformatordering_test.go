package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestResponseFormatSchemaKeyOrderSurvivesOpenAIChat walks the real inbound path:
// raw client body -> OpenAIChatRequest -> Bifrost -> OpenAI wire payload, and
// asserts the JSON Schema reaches OpenAI in the order the client declared it.
func TestResponseFormatSchemaKeyOrderSurvivesOpenAIChat(t *testing.T) {
	var inbound OpenAIChatRequest
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &inbound))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := inbound.ToBifrostChatRequest(ctx)
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = schemas.OpenAI

	outbound := ToOpenAIChatRequest(ctx, bifrostReq)
	require.NotNil(t, outbound)

	raw, err := providerUtils.MarshalSorted(outbound)
	require.NoError(t, err)
	wire := string(raw)

	schemaorder.AssertPropertyOrder(t, wire)
	schemaorder.AssertSchemaKeyOrder(t, wire, "schema")
	schemaorder.AssertKeyOrder(t, schemaorder.ObjectAfterKey(t, wire, "json_schema"), "name", "description", "schema", "strict")
}

// TestResponseFormatSchemaKeyOrderSurvivesOpenAIResponses covers the Responses
// API method, where the schema arrives under text.format instead of response_format.
func TestResponseFormatSchemaKeyOrderSurvivesOpenAIResponses(t *testing.T) {
	var inbound OpenAIResponsesRequest
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &inbound))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := inbound.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = schemas.OpenAI

	outbound := ToOpenAIResponsesRequest(ctx, bifrostReq)
	require.NotNil(t, outbound)

	raw, err := providerUtils.MarshalSorted(outbound)
	require.NoError(t, err)
	wire := string(raw)

	schemaorder.AssertPropertyOrder(t, wire)
	schemaorder.AssertSchemaKeyOrder(t, wire, "schema")
}

// TestResponseFormatChatToResponsesConversionKeepsOrder covers the cross-method
// path: a chat request routed to a Responses-API-only surface must not lose the
// declared property order while being rewritten.
func TestResponseFormatChatToResponsesConversionKeepsOrder(t *testing.T) {
	var inbound OpenAIChatRequest
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &inbound))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := inbound.ToBifrostChatRequest(ctx)
	require.NotNil(t, bifrostReq)

	outbound := ToOpenAIResponsesRequest(ctx, bifrostReq.ToResponsesRequest())
	require.NotNil(t, outbound)

	raw, err := providerUtils.MarshalSorted(outbound)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
}

// TestResponseFormatSchemaOrderIsDeterministic ensures the fix does not trade
// alphabetical sorting for random Go map ordering: repeated marshals of the same
// request must emit identical bytes.
func TestResponseFormatSchemaOrderIsDeterministic(t *testing.T) {
	var inbound OpenAIChatRequest
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &inbound))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	outbound := ToOpenAIChatRequest(ctx, inbound.ToBifrostChatRequest(ctx))
	require.NotNil(t, outbound)

	first, err := providerUtils.MarshalSorted(outbound)
	require.NoError(t, err)
	for i := 0; i < 50; i++ {
		next, err := providerUtils.MarshalSorted(outbound)
		require.NoError(t, err)
		require.Equal(t, string(first), string(next), "non-deterministic response_format marshal on iteration %d", i)
	}
}

// TestResponseFormatSchemaBytesReachOpenAIVerbatim pins the stronger invariant:
// OpenAI takes the schema in the shape Bifrost received it, so the gateway has
// no reason to re-encode it at all. Byte equality also covers numeric fidelity,
// which key order alone does not.
func TestResponseFormatSchemaBytesReachOpenAIVerbatim(t *testing.T) {
	var inbound OpenAIChatRequest
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &inbound))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := inbound.ToBifrostChatRequest(ctx)
	bifrostReq.Provider = schemas.OpenAI

	raw, err := providerUtils.MarshalSorted(ToOpenAIChatRequest(ctx, bifrostReq))
	require.NoError(t, err)
	schemaorder.AssertSchemaBytes(t, string(raw), "schema")
}

// TestResponseFormatSurvivesDelegatingProviders covers every provider that has no
// response_format handling of its own and rides the OpenAI chat conversion. They
// each get their own parameter filtering, so this pins that none of them strips
// or rewrites the schema on the way through.
func TestResponseFormatSurvivesDelegatingProviders(t *testing.T) {
	delegating := []schemas.ModelProvider{
		schemas.Azure, schemas.Cerebras, schemas.DeepSeek, schemas.Groq,
		schemas.OpenRouter, schemas.SGL, schemas.VLLM, schemas.XAI,
		schemas.Wafer, schemas.Ollama, schemas.Fireworks, schemas.Parasail,
	}

	for _, provider := range delegating {
		t.Run(string(provider), func(t *testing.T) {
			var inbound OpenAIChatRequest
			require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &inbound))

			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			bifrostReq := inbound.ToBifrostChatRequest(ctx)
			bifrostReq.Provider = provider

			outbound := ToOpenAIChatRequest(ctx, bifrostReq)
			require.NotNil(t, outbound)
			require.NotNil(t, outbound.ResponseFormat, "%s dropped response_format", provider)

			raw, err := providerUtils.MarshalSorted(outbound)
			require.NoError(t, err)
			schemaorder.AssertSchemaBytes(t, string(raw), "schema")
		})
	}
}
