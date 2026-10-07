package logging

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAttachLogRedactionDataCopiesContextValue verifies async log entries carry transient redaction data.
func TestAttachLogRedactionDataCopiesContextValue(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		LiteralReplacements: schemas.RedactionMapsByPhase{
			Input:  map[string]string{"alex_rivera@gmail.com": "[EMAIL-1]"},
			Output: map[string]string{"rivera@example.com": "[EMAIL-2]"},
		},
		ReversibleMappings: schemas.RedactionMapsByPhase{
			Input: map[string]string{"EMAIL-1": "alex_rivera@gmail.com"},
		},
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, true)

	require.NotNil(t, entry.RedactionData)
	assert.Equal(t, map[string]string{"EMAIL-1": "alex_rivera@gmail.com"}, entry.RedactionData.ReversibleMappings.Input)
	assert.Equal(t, map[string]string{"alex_rivera@gmail.com": "[EMAIL-1]"}, entry.RedactionData.LiteralReplacements.Input)
	assert.Equal(t, map[string]string{"rivera@example.com": "[EMAIL-2]"}, entry.RedactionData.LiteralReplacements.Output)
}

// TestAttachLogRedactionDataClonesContextMaps verifies async log entries own their redaction maps.
func TestAttachLogRedactionDataClonesContextMaps(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	reversibleMappings := map[string]string{"EMAIL-1": "alex_rivera@gmail.com"}
	inputLiteralReplacements := map[string]string{"alex_rivera@gmail.com": "[EMAIL-1]"}
	outputLiteralReplacements := map[string]string{"rivera@example.com": "[EMAIL-2]"}
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		LiteralReplacements: schemas.RedactionMapsByPhase{
			Input:  inputLiteralReplacements,
			Output: outputLiteralReplacements,
		},
		ReversibleMappings: schemas.RedactionMapsByPhase{
			Input: reversibleMappings,
		},
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, true)
	reversibleMappings["EMAIL-1"] = "mutated@example.com"
	inputLiteralReplacements["alex_rivera@gmail.com"] = "[MUTATED]"
	outputLiteralReplacements["rivera@example.com"] = "[MUTATED]"

	require.NotNil(t, entry.RedactionData)
	assert.Equal(t, "alex_rivera@gmail.com", entry.RedactionData.ReversibleMappings.Input["EMAIL-1"])
	assert.Equal(t, "[EMAIL-1]", entry.RedactionData.LiteralReplacements.Input["alex_rivera@gmail.com"])
	assert.Equal(t, "[EMAIL-2]", entry.RedactionData.LiteralReplacements.Output["rivera@example.com"])
}

// TestAttachLogRedactionDataSkipsDisabledContentLogging verifies disabled content logging drops sensitive data.
func TestAttachLogRedactionDataSkipsDisabledContentLogging(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		ReversibleMappings: schemas.RedactionMapsByPhase{
			Input: map[string]string{"EMAIL-1": "alex_rivera@gmail.com"},
		},
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, false)

	assert.Nil(t, entry.RedactionData)
}

// TestAttachLogRedactionDataIgnoresMissingContext verifies nil inputs are safe for processing callbacks.
func TestAttachLogRedactionDataIgnoresMissingContext(t *testing.T) {
	entry := &logstore.Log{}

	attachLogRedactionData(nil, entry, true)
	attachLogRedactionData(schemas.NewBifrostContext(context.Background(), time.Time{}), entry, true)

	assert.Nil(t, entry.RedactionData)
}

// TestAttachMCPLogRedactionDataCopiesContextValue verifies MCP entries receive an owned redaction snapshot.
func TestAttachMCPLogRedactionDataCopiesContextValue(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	reversibleMappings := map[string]string{"EMAIL-1": "alex_rivera@gmail.com"}
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		ReversibleMappings: schemas.RedactionMapsByPhase{Input: reversibleMappings},
	})
	entry := &logstore.MCPToolLog{}

	attachMCPLogRedactionData(ctx, entry, true)
	reversibleMappings["EMAIL-1"] = "mutated@example.com"

	require.NotNil(t, entry.RedactionData)
	assert.Equal(t, "alex_rivera@gmail.com", entry.RedactionData.ReversibleMappings.Input["EMAIL-1"])
}

// TestAttachMCPLogRedactionDataSkipsUnavailableContent verifies disabled logging and missing inputs never attach sensitive data.
func TestAttachMCPLogRedactionDataSkipsUnavailableContent(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		ReversibleMappings: schemas.RedactionMapsByPhase{Input: map[string]string{"EMAIL-1": "alex_rivera@gmail.com"}},
	})
	entry := &logstore.MCPToolLog{}

	attachMCPLogRedactionData(ctx, entry, false)
	attachMCPLogRedactionData(nil, entry, true)
	attachMCPLogRedactionData(ctx, nil, true)

	assert.Nil(t, entry.RedactionData)
}

// TestDecisionInputLogMessagesMatchChatLogging pins that a logged ordered
// decision input takes the shape chat requests are logged in: a text input is
// one user message, text and image parts become text and image_url blocks the
// logs UI renders, and an unfamiliar part is kept as its type name. The
// payload is synthetic.
func TestDecisionInputLogMessagesMatchChatLogging(t *testing.T) {
	assert.Empty(t, decisionInputLogMessages(nil))

	text := decisionInputLogMessages(&schemas.DecisionInput{Text: schemas.Ptr("I was charged twice.")})
	require.Len(t, text, 1)
	assert.Equal(t, schemas.ChatMessageRoleUser, text[0].Role)
	require.NotNil(t, text[0].Content.ContentStr)
	assert.Equal(t, "I was charged twice.", *text[0].Content.ContentStr)

	input := &schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		{Role: "user", Content: schemas.DecisionInputContent{Text: schemas.Ptr("Plain message.")}},
		{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeText, Text: schemas.Ptr("Inspect the product.")},
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,AAAA")},
			{Type: "input_audio"},
		}}},
	}}

	logged := decisionInputLogMessages(input)
	require.Len(t, logged, 2)
	require.NotNil(t, logged[0].Content.ContentStr)
	assert.Equal(t, "Plain message.", *logged[0].Content.ContentStr)

	blocks := logged[1].Content.ContentBlocks
	require.Len(t, blocks, 3)
	assert.Equal(t, schemas.ChatContentBlockTypeText, blocks[0].Type)
	assert.Equal(t, "Inspect the product.", *blocks[0].Text)
	assert.Equal(t, schemas.ChatContentBlockTypeImage, blocks[1].Type)
	require.NotNil(t, blocks[1].ImageURLStruct)
	assert.Equal(t, "data:image/png;base64,AAAA", blocks[1].ImageURLStruct.URL)
	assert.Equal(t, schemas.ChatContentBlockTypeText, blocks[2].Type)
	assert.Equal(t, "[input_audio]", *blocks[2].Text)
}
