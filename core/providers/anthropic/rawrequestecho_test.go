package anthropic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The /anthropic/v1/messages route dropped extra_fields on the way out, so
// x-bf-send-back-raw-request produced nothing for callers on that surface even when the
// setting was enabled. The raw capture is populated on the Bifrost response
// (BifrostResponseExtraFields.RawRequest) but AnthropicMessageResponse had no field to
// carry it, unlike BifrostChatResponse which serializes extra_fields directly.
//
// The echo must appear ONLY when a raw capture is actually present, so a default
// response on this route stays byte-identical to Anthropic's documented shape.

func responsesRespWithRaw(rawReq interface{}) *schemas.BifrostResponsesResponse {
	return &schemas.BifrostResponsesResponse{
		ID:    schemas.Ptr("resp_1"),
		Model: "gemini-3.7-flash",
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:   schemas.Gemini,
			RawRequest: rawReq,
		},
	}
}

func TestToAnthropicResponsesResponse_EchoesRawRequestWhenCaptured(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	raw := map[string]interface{}{"contents": []interface{}{
		map[string]interface{}{"role": "user", "parts": []interface{}{map[string]interface{}{"text": "hi"}}},
	}}

	out := ToAnthropicResponsesResponse(ctx, responsesRespWithRaw(raw))
	require.NotNil(t, out)
	require.NotNil(t, out.ExtraFields, "extra_fields must be carried when a raw request was captured")
	assert.NotNil(t, out.ExtraFields.RawRequest, "raw_request must survive to the Anthropic wire shape")

	// It must actually serialize - a field that marshals away is no better than a dropped one.
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "\"extra_fields\"")
	assert.Contains(t, string(encoded), "\"raw_request\"")
}

// Without a capture, the response must stay byte-conformant to Anthropic's schema.
func TestToAnthropicResponsesResponse_NoExtraFieldsWithoutCapture(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	out := ToAnthropicResponsesResponse(ctx, responsesRespWithRaw(nil))
	require.NotNil(t, out)
	assert.Nil(t, out.ExtraFields, "extra_fields must be absent when nothing was captured")

	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "extra_fields",
		"a default response on this route must stay byte-identical to Anthropic's shape")
}
