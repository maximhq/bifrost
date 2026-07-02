package schemas

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactionPayloadContextRoundTrip verifies typed payloads can be read and serialized from context.
func TestRedactionPayloadContextRoundTrip(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	payload := RedactionPayload{
		ReversibleMappings:  map[string]string{"EMAIL-1": "alex@example.com"},
		LiteralReplacements: map[string]string{"alex@example.com": "[EMAIL-1]"},
		InputHistory:        `[{"role":"user","content":"[EMAIL-1]"}]`,
	}

	require.True(t, SetRedactionPayloadOnContext(ctx, payload))

	got, ok := RedactionPayloadFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, payload, got)

	serialized, ok := RedactionPayloadStringFromContext(ctx)
	require.True(t, ok)
	assert.Contains(t, serialized, "alex@example.com")
}

// TestRedactionPayloadFromSerializedContext verifies existing serialized payload values still decode.
func TestRedactionPayloadFromSerializedContext(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.SetValue(BifrostContextKeyRedactionData, `{"reversible_mappings":{"EMAIL-1":"alex@example.com"}}`)

	payload, ok := RedactionPayloadFromContext(ctx)

	require.True(t, ok)
	assert.Equal(t, map[string]string{"EMAIL-1": "alex@example.com"}, payload.ReversibleMappings)
}

// TestApplyLiteralReplacementsLongestFirst verifies overlapping literals are redacted deterministically.
func TestApplyLiteralReplacementsLongestFirst(t *testing.T) {
	replacements := map[string]string{
		"alex@example.com": "[EMAIL-1]",
		"example.com":      "[DOMAIN]",
	}

	got := ApplyLiteralReplacements("email alex@example.com uses example.com", replacements)

	assert.Equal(t, "email [EMAIL-1] uses [DOMAIN]", got)
}

// TestIsContentAttribute verifies only prompt, response, and tool payload fields are treated as content.
func TestIsContentAttribute(t *testing.T) {
	assert.True(t, IsContentAttribute(AttrInputMessages))
	assert.True(t, IsContentAttribute(AttrOutputMessages))
	assert.True(t, IsContentAttribute(AttrToolCallArguments))
	assert.True(t, IsContentAttribute(AttrInputEmbedding))
	assert.True(t, IsContentAttribute(AttrPrompt))
	assert.True(t, IsContentAttribute(AttrInstructions))
	assert.True(t, IsContentAttribute(AttrRespReasoningText))

	assert.False(t, IsContentAttribute(AttrRequestModel))
	assert.False(t, IsContentAttribute(AttrProviderName))
	assert.False(t, IsContentAttribute(TraceAttrSessionID))
}

// TestTraceApplyRedactionReplacementsRedactsContentAttributes verifies trace redaction touches all spans.
func TestTraceApplyRedactionReplacementsRedactsContentAttributes(t *testing.T) {
	trace := &Trace{}
	root := &Span{}
	child := &Span{}
	trace.RootSpan = root
	trace.Spans = []*Span{root, child}

	root.SetAttribute(AttrInputMessages, `{"content":"email alex@example.com"}`)
	root.SetAttribute(AttrRequestModel, "alex@example.com")
	child.SetAttribute(AttrOutputMessages, []string{"reply to alex@example.com"})
	child.SetAttribute(AttrToolCallArguments, []any{"alex@example.com", 42})
	child.AddEvent(SpanEvent{
		Name: "llm.message",
		Attributes: map[string]any{
			AttrInputMessages: `{"content":"event alex@example.com"}`,
			AttrRequestModel:  "alex@example.com",
		},
	})

	trace.SetRedactionReplacements(map[string]string{"alex@example.com": "[EMAIL-1]"})
	trace.ApplyRedactionReplacements()

	assert.Equal(t, `{"content":"email [EMAIL-1]"}`, root.Attributes[AttrInputMessages])
	assert.Equal(t, "alex@example.com", root.Attributes[AttrRequestModel])
	assert.Equal(t, []string{"reply to [EMAIL-1]"}, child.Attributes[AttrOutputMessages])
	assert.Equal(t, []any{"[EMAIL-1]", 42}, child.Attributes[AttrToolCallArguments])
	require.Len(t, child.Events, 1)
	assert.Equal(t, `{"content":"event [EMAIL-1]"}`, child.Events[0].Attributes[AttrInputMessages])
	assert.Equal(t, "alex@example.com", child.Events[0].Attributes[AttrRequestModel])
	assert.Nil(t, trace.redactionReplacements)
}

// TestTraceSetRedactionReplacementsMergesCalls verifies input/output replacement windows accumulate.
func TestTraceSetRedactionReplacementsMergesCalls(t *testing.T) {
	trace := &Trace{}
	root := &Span{}
	child := &Span{}
	trace.RootSpan = root
	trace.Spans = []*Span{root, child}

	root.SetAttribute(AttrInputMessages, `{"content":"email input@example.com"}`)
	child.SetAttribute(AttrOutputMessages, `{"content":"reply output@example.com"}`)

	trace.SetRedactionReplacements(map[string]string{"input@example.com": "[EMAIL-1]"})
	trace.SetRedactionReplacements(map[string]string{"output@example.com": "[EMAIL-2]"})
	trace.ApplyRedactionReplacements()

	assert.Equal(t, `{"content":"email [EMAIL-1]"}`, root.Attributes[AttrInputMessages])
	assert.Equal(t, `{"content":"reply [EMAIL-2]"}`, child.Attributes[AttrOutputMessages])
	assert.Nil(t, trace.redactionReplacements)
}

// TestTraceRedactionReplacementsDoNotSerialize verifies connector-facing replacements stay internal.
func TestTraceRedactionReplacementsDoNotSerialize(t *testing.T) {
	trace := &Trace{TraceID: "trace-1"}
	trace.SetRedactionReplacements(map[string]string{"alex@example.com": "[EMAIL-1]"})

	serialized, err := sonic.MarshalString(trace)
	require.NoError(t, err)

	assert.NotContains(t, serialized, "alex@example.com")
	assert.NotContains(t, serialized, "[EMAIL-1]")
	assert.False(t, strings.Contains(serialized, "redactionReplacements"))
	assert.False(t, strings.Contains(serialized, "RedactionReplacements"))
}

// TestTraceResetClearsRedactionReplacements verifies pooled traces cannot retain request redaction data.
func TestTraceResetClearsRedactionReplacements(t *testing.T) {
	trace := &Trace{}
	trace.SetRedactionReplacements(map[string]string{"alex@example.com": "[EMAIL-1]"})

	trace.Reset()

	assert.Nil(t, trace.redactionReplacements)
}
