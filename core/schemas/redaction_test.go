package schemas

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactionDataContextRoundTrip verifies typed redaction data can be read from context.
func TestRedactionDataContextRoundTrip(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	data := RedactionData{
		LiteralReplacements: RedactionMapsByPhase{
			Input:  map[string]string{"alex@example.com": "[EMAIL-1]"},
			Output: map[string]string{"rivera@example.com": "[EMAIL-2]"},
		},
		ReversibleMappings: RedactionMapsByPhase{
			Input:  map[string]string{"EMAIL-1": "alex@example.com"},
			Output: map[string]string{"EMAIL-2": "rivera@example.com"},
		},
	}

	require.True(t, SetRedactionDataOnContext(ctx, data))

	got, ok := RedactionDataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, data, got)
}

// TestRedactionDataCloneCopiesMaps verifies clones do not share mutable map storage.
func TestRedactionDataCloneCopiesMaps(t *testing.T) {
	data := RedactionData{
		LiteralReplacements: RedactionMapsByPhase{
			Input:  map[string]string{"alex@example.com": "[EMAIL-1]"},
			Output: map[string]string{"rivera@example.com": "[EMAIL-2]"},
		},
		ReversibleMappings: RedactionMapsByPhase{
			Input:  map[string]string{"EMAIL-1": "alex@example.com"},
			Output: map[string]string{"EMAIL-2": "rivera@example.com"},
		},
	}

	clone := data.Clone()
	data.ReversibleMappings.Input["EMAIL-1"] = "mutated@example.com"
	data.ReversibleMappings.Output["EMAIL-2"] = "mutated@example.com"
	data.LiteralReplacements.Input["alex@example.com"] = "[MUTATED]"
	data.LiteralReplacements.Output["rivera@example.com"] = "[MUTATED]"

	assert.Equal(t, "alex@example.com", clone.ReversibleMappings.Input["EMAIL-1"])
	assert.Equal(t, "rivera@example.com", clone.ReversibleMappings.Output["EMAIL-2"])
	assert.Equal(t, "[EMAIL-1]", clone.LiteralReplacements.Input["alex@example.com"])
	assert.Equal(t, "[EMAIL-2]", clone.LiteralReplacements.Output["rivera@example.com"])
}

// TestRedactionDataFromContextRejectsSerializedValues verifies the handoff remains typed.
func TestRedactionDataFromContextRejectsSerializedValues(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.SetValue(BifrostContextKeyRedactionData, `{"reversible_mappings":{"EMAIL-1":"alex@example.com"}}`)

	_, ok := RedactionDataFromContext(ctx)

	assert.False(t, ok)
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

// TestTraceApplyRedactionReplacementsRedactsContentAttributes verifies trace redaction honors attribute phase.
func TestTraceApplyRedactionReplacementsRedactsContentAttributes(t *testing.T) {
	trace := &Trace{}
	root := &Span{}
	child := &Span{}
	trace.RootSpan = root
	trace.Spans = []*Span{root, child}

	root.SetAttribute(AttrInputMessages, `{"content":"email alex@example.com and bob@example.com"}`)
	root.SetAttribute(AttrRequestModel, "alex@example.com")
	child.SetAttribute(AttrOutputMessages, []string{"reply to alex@example.com and bob@example.com"})
	child.SetAttribute(AttrToolCallArguments, map[string]any{
		"customer": map[string]any{
			"email": "alex@example.com",
			"owner": "bob@example.com",
			"tags":  []any{"safe", "alex@example.com", "bob@example.com"},
		},
		"metadata": map[string]string{
			"owner": "bob@example.com",
		},
		"literal_key_alex@example.com": "key should redact too",
		"count":                        42,
	})
	child.AddEvent(SpanEvent{
		Name: "llm.message",
		Attributes: map[string]any{
			AttrInputMessages:  `{"content":"event alex@example.com and bob@example.com"}`,
			AttrOutputMessages: `{"content":"event alex@example.com and bob@example.com"}`,
			AttrRequestModel:   "alex@example.com",
		},
	})

	trace.SetRedactionReplacements(RedactionPhaseInput, map[string]string{"alex@example.com": "[EMAIL-1]"})
	trace.SetRedactionReplacements(RedactionPhaseOutput, map[string]string{"bob@example.com": "[EMAIL-2]"})
	trace.ApplyRedactionReplacements()

	assert.Equal(t, `{"content":"email [EMAIL-1] and bob@example.com"}`, root.Attributes[AttrInputMessages])
	assert.Equal(t, "alex@example.com", root.Attributes[AttrRequestModel])
	assert.Equal(t, []string{"reply to alex@example.com and [EMAIL-2]"}, child.Attributes[AttrOutputMessages])
	assert.Equal(t, map[string]any{
		"customer": map[string]any{
			"email": "[EMAIL-1]",
			"owner": "[EMAIL-2]",
			"tags":  []any{"safe", "[EMAIL-1]", "[EMAIL-2]"},
		},
		"metadata": map[string]string{
			"owner": "[EMAIL-2]",
		},
		"literal_key_[EMAIL-1]": "key should redact too",
		"count":                 42,
	}, child.Attributes[AttrToolCallArguments])
	require.Len(t, child.Events, 1)
	assert.Equal(t, `{"content":"event [EMAIL-1] and bob@example.com"}`, child.Events[0].Attributes[AttrInputMessages])
	assert.Equal(t, `{"content":"event alex@example.com and [EMAIL-2]"}`, child.Events[0].Attributes[AttrOutputMessages])
	assert.Equal(t, "alex@example.com", child.Events[0].Attributes[AttrRequestModel])
	assert.False(t, trace.redactionReplacements.HasReplacements())
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

	trace.SetRedactionReplacements(RedactionPhaseInput, map[string]string{"input@example.com": "[EMAIL-1]"})
	trace.SetRedactionReplacements(RedactionPhaseOutput, map[string]string{"output@example.com": "[EMAIL-2]"})
	trace.ApplyRedactionReplacements()

	assert.Equal(t, `{"content":"email [EMAIL-1]"}`, root.Attributes[AttrInputMessages])
	assert.Equal(t, `{"content":"reply [EMAIL-2]"}`, child.Attributes[AttrOutputMessages])
	assert.False(t, trace.redactionReplacements.HasReplacements())
}

// TestTraceRedactionReplacementsDoNotSerialize verifies connector-facing replacements stay internal.
func TestTraceRedactionReplacementsDoNotSerialize(t *testing.T) {
	trace := &Trace{TraceID: "trace-1"}
	trace.SetRedactionReplacements(RedactionPhaseInput, map[string]string{"alex@example.com": "[EMAIL-1]"})
	trace.SetRedactionReplacements(RedactionPhaseOutput, map[string]string{"rivera@example.com": "[EMAIL-2]"})

	serialized, err := sonic.MarshalString(trace)
	require.NoError(t, err)

	assert.NotContains(t, serialized, "alex@example.com")
	assert.NotContains(t, serialized, "[EMAIL-1]")
	assert.NotContains(t, serialized, "rivera@example.com")
	assert.NotContains(t, serialized, "[EMAIL-2]")
	assert.False(t, strings.Contains(serialized, "redactionReplacements"))
	assert.False(t, strings.Contains(serialized, "RedactionReplacements"))
}

// TestTraceResetClearsRedactionReplacements verifies pooled traces cannot retain request redaction data.
func TestTraceResetClearsRedactionReplacements(t *testing.T) {
	trace := &Trace{}
	trace.SetRedactionReplacements(RedactionPhaseInput, map[string]string{"alex@example.com": "[EMAIL-1]"})
	trace.SetRedactionReplacements(RedactionPhaseOutput, map[string]string{"rivera@example.com": "[EMAIL-2]"})

	trace.Reset()

	assert.False(t, trace.redactionReplacements.HasReplacements())
}
