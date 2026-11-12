package traceingest

import (
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

// btParentV3 builds a span.export() value the way SDK span_identifier_v3 does: every id that is a
// UUID is a 17-byte binary entry, anything else goes into the trailing JSON.
func btParentV3(t *testing.T, objectType byte, uuids map[byte]string, json string) string {
	raw := []byte{3, objectType, byte(len(uuids))}
	for _, field := range []byte{1, 2, 3, 4} {
		if id, ok := uuids[field]; ok {
			raw = append(raw, field)
			raw = append(raw, mustHex(t, id)...)
		}
	}
	return base64.StdEncoding.EncodeToString(append(raw, json...))
}

// TestDecodeBraintrustParent pins the decoding of Braintrust's span.export() string, the value its
// SDKs send as x-bt-parent, in both encoding versions.
func TestDecodeBraintrustParent(t *testing.T) {
	t.Run("version 3 with uuid ids", func(t *testing.T) {
		value := btParentV3(t, 2, map[byte]string{
			1: "11111111222233334444555555555555", 2: "aaaaaaaabbbbccccddddeeeeeeeeeeee",
			3: "0123456789abcdef0123456789abcdef", 4: "fedcba9876543210fedcba9876543210",
		}, `{"propagated_event":{"metadata":{"env":"prod"}}}`)
		parent, err := DecodeBraintrustParent(value)
		require.NoError(t, err)
		assert.Equal(t, "project_logs", parent.ObjectType)
		assert.Equal(t, "11111111-2222-3333-4444-555555555555", parent.ObjectID)
		assert.Equal(t, "01234567-89ab-cdef-0123-456789abcdef", parent.SpanID)
		assert.Equal(t, "fedcba98-7654-3210-fedc-ba9876543210", parent.RootSpanID)
		assert.Equal(t, map[string]any{"metadata": map[string]any{"env": "prod"}}, parent.PropagatedEvent)
	})

	t.Run("version 3 with a non-uuid id in the json tail", func(t *testing.T) {
		value := btParentV3(t, 1, map[byte]string{4: "fedcba9876543210fedcba9876543210"}, `{"object_id":"exp-1","span_id":"custom-span"}`)
		parent, err := DecodeBraintrustParent(value)
		require.NoError(t, err)
		assert.Equal(t, "experiment", parent.ObjectType)
		assert.Equal(t, "exp-1", parent.ObjectID)
		assert.Equal(t, "custom-span", parent.SpanID)
	})

	t.Run("version 4 with otel-sized hex ids", func(t *testing.T) {
		raw := []byte{4, 2, 2, 3}
		raw = append(raw, mustHex(t, "b7ad6b7169203331")...)
		raw = append(raw, 4)
		raw = append(raw, mustHex(t, "0af7651916cd43dd8448eb211c80319c")...)
		raw = append(raw, `{"object_id":"proj-uuid","compute_object_metadata_args":{"project_name":"agent"}}`...)
		parent, err := DecodeBraintrustParent(base64.StdEncoding.EncodeToString(raw))
		require.NoError(t, err)
		assert.Equal(t, "b7ad6b7169203331", parent.SpanID, "version 4 span ids are 8 bytes, written as hex")
		assert.Equal(t, "0af7651916cd43dd8448eb211c80319c", parent.RootSpanID)
		assert.Equal(t, "proj-uuid", parent.ObjectID)
		assert.Equal(t, "agent", parent.ProjectName)
	})

	for name, value := range map[string]string{
		"not base64":      "%%%",
		"too short":       base64.StdEncoding.EncodeToString([]byte{3, 2}),
		"old version":     base64.StdEncoding.EncodeToString([]byte{2, 2, 0}),
		"truncated entry": base64.StdEncoding.EncodeToString([]byte{3, 2, 1, 3, 0xaa}),
		"unknown type":    base64.StdEncoding.EncodeToString([]byte{3, 9, 0}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeBraintrustParent(value)
			assert.Error(t, err)
		})
	}
}

// TestLinkFromRequestPrefersExplicitHeaders pins which trace a gateway request is attached to.
func TestLinkFromRequestPrefersExplicitHeaders(t *testing.T) {
	bt := btParentV3(t, 2, map[byte]string{3: "0123456789abcdef0123456789abcdef", 4: "fedcba9876543210fedcba9876543210"}, "")

	link := LinkFromRequest(map[string]string{"x-bf-trace-id": "t-1", "x-bf-parent-span-id": "s-1", "x-bt-parent": bt, "traceparent": "00-x-y-01"}, "otel-trace", "otel-span")
	assert.Equal(t, &GatewayLink{TraceID: "t-1", ParentSpanID: "s-1", Method: LinkMethodHeader}, link)

	link = LinkFromRequest(map[string]string{"x-bt-parent": bt}, "", "")
	assert.Equal(t, &GatewayLink{TraceID: "fedcba98-7654-3210-fedc-ba9876543210", ParentSpanID: "01234567-89ab-cdef-0123-456789abcdef", Method: LinkMethodHeader}, link)

	link = LinkFromRequest(map[string]string{"langfuse_trace_id": "lf-trace", "langfuse_parent_observation_id": "lf-obs"}, "", "")
	assert.Equal(t, &GatewayLink{TraceID: "lf-trace", ParentSpanID: "lf-obs", Method: LinkMethodHeader}, link)

	link = LinkFromRequest(map[string]string{"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}, "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331")
	assert.Equal(t, &GatewayLink{TraceID: "0af7651916cd43dd8448eb211c80319c", ParentSpanID: "b7ad6b7169203331", Method: LinkMethodTraceparent}, link)

	assert.Nil(t, LinkFromRequest(map[string]string{}, "minted-by-bifrost", ""), "a trace id Bifrost minted itself links nothing")
	assert.Nil(t, LinkFromRequest(map[string]string{"x-bt-parent": "project_name:agent"}, "", ""), "a parent that names no span links nothing")
}

// TestFingerprintMatchesAcrossLoggedShapes pins the fingerprint: the gateway's view of a request
// and every shape an SDK logs the same prompt in must hash alike.
func TestFingerprintMatchesAcrossLoggedShapes(t *testing.T) {
	prompt := "What is the  capital\nof France?"
	want := TextFingerprint("What is the capital of France?")
	require.NotEmpty(t, want)

	chat := []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: new("be brief")}},
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &prompt}},
		{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: new("Paris")}},
	}
	assert.Equal(t, want, ChatFingerprint(chat))
	blocks := []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{Type: "text", Text: &prompt}}}}}
	assert.Equal(t, want, ChatFingerprint(blocks))
	role := schemas.ResponsesInputMessageRoleUser
	assert.Equal(t, want, ResponsesFingerprint([]schemas.ResponsesMessage{{Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: &prompt}}}))

	for name, input := range map[string]any{
		"openai messages":     []any{map[string]any{"role": "system", "content": "be brief"}, map[string]any{"role": "user", "content": prompt}},
		"content parts":       []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": prompt}}}},
		"messages in object":  map[string]any{"messages": []any{map[string]any{"role": "user", "content": prompt}}, "model": "gpt-4o"},
		"openinference":       []any{map[string]any{"message": map[string]any{"role": "user", "content": prompt}}},
		"openinference parts": []any{map[string]any{"message": map[string]any{"role": "user", "contents": []any{map[string]any{"message_content": map[string]any{"type": "text", "text": prompt}}}}}},
		"gen_ai parts":        []any{map[string]any{"role": "user", "parts": []any{map[string]any{"type": "text", "content": prompt}}}},
		"bare prompt":         prompt,
		"responses api input": map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}}},
	} {
		assert.Equal(t, want, SpanInputFingerprint(input), name)
	}
	assert.Empty(t, SpanInputFingerprint([]any{map[string]any{"role": "assistant", "content": "hi"}}))
	assert.Empty(t, SpanInputFingerprint(nil))
}
