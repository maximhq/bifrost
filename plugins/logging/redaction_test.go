package logging

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
)

// TestAttachLogRedactionDataCopiesContextValue verifies async log entries carry transient redaction data.
func TestAttachLogRedactionDataCopiesContextValue(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	schemas.SetRedactionPayloadOnContext(ctx, schemas.RedactionPayload{
		ReversibleMappings: map[string]string{"EMAIL-1": "alex_rivera@gmail.com"},
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, true)

	payload, ok := schemas.RedactionPayloadFromContext(ctx)
	assert.True(t, ok)
	assert.Contains(t, entry.RedactionData, "alex_rivera@gmail.com")
	assert.Equal(t, map[string]string{"EMAIL-1": "alex_rivera@gmail.com"}, payload.ReversibleMappings)
}

// TestAttachLogRedactionDataSkipsDisabledContentLogging verifies disabled content logging drops sensitive payloads.
func TestAttachLogRedactionDataSkipsDisabledContentLogging(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	schemas.SetRedactionPayloadOnContext(ctx, schemas.RedactionPayload{
		ReversibleMappings: map[string]string{"EMAIL-1": "alex_rivera@gmail.com"},
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, false)

	assert.Empty(t, entry.RedactionData)
}

// TestAttachLogRedactionDataIgnoresMissingContext verifies nil inputs are safe for processing callbacks.
func TestAttachLogRedactionDataIgnoresMissingContext(t *testing.T) {
	entry := &logstore.Log{}

	attachLogRedactionData(nil, entry, true)
	attachLogRedactionData(schemas.NewBifrostContext(context.Background(), time.Time{}), entry, true)

	assert.Empty(t, entry.RedactionData)
}
