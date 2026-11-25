package logging

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

func TestApplyProviderResponseID(t *testing.T) {
	id := "msg_011ChQDewfwJ3BMDLzXNoVjS"
	oversized := strings.Repeat("a", 256)
	cases := []struct {
		name   string
		result *schemas.BifrostResponse
		want   string
	}{
		{
			name:   "chat completion",
			result: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ID: id}},
			want:   id,
		},
		{
			name:   "responses",
			result: &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{ID: &id}},
			want:   id,
		},
		{
			name: "responses stream final chunk",
			result: &schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
				Response: &schemas.BifrostResponsesResponse{ID: &id},
			}},
			want: id,
		},
		{
			name:   "responses stream chunk without a response",
			result: &schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{}},
		},
		{
			name:   "text completion",
			result: &schemas.BifrostResponse{TextCompletionResponse: &schemas.BifrostTextCompletionResponse{ID: id}},
			want:   id,
		},
		{
			name:   "empty id",
			result: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}},
		},
		{
			name:   "oversized id",
			result: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ID: oversized}},
		},
		{name: "nil result"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := &logstore.Log{}
			applyProviderResponseID(entry, tc.result)
			got := ""
			if entry.ProviderResponseID != nil {
				got = *entry.ProviderResponseID
			}
			if got != tc.want {
				t.Fatalf("provider_response_id = %q, want %q", got, tc.want)
			}
		})
	}
	// A nil entry must not panic.
	applyProviderResponseID(nil, &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ID: id}})
}
