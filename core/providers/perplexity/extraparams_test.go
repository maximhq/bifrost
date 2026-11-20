package perplexity

import (
	"reflect"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func newExtraParamsChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Model: "sonar",
		Input: []schemas.ChatMessage{
			{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				"search_mode":   "academic",
				"return_images": true,
				"custom":        "kept",
			},
		},
	}
}

func TestToPerplexityChatCompletionRequest_DoesNotMutateCallerExtraParams(t *testing.T) {
	t.Parallel()

	req := newExtraParamsChatRequest()

	out := ToPerplexityChatCompletionRequest(req)

	if out == nil {
		t.Fatal("expected non-nil result")
	}
	if !reflect.DeepEqual(req.Params.ExtraParams, newExtraParamsChatRequest().Params.ExtraParams) {
		t.Fatalf("caller's ExtraParams was modified: %v", req.Params.ExtraParams)
	}
	if out.SearchMode == nil || *out.SearchMode != "academic" {
		t.Errorf("expected search_mode to be mapped, got %v", out.SearchMode)
	}
	if _, ok := out.ExtraParams["search_mode"]; ok {
		t.Error("search_mode should not stay in the request's ExtraParams once it is mapped")
	}
	if out.ExtraParams["custom"] != "kept" {
		t.Errorf("expected unmapped custom param to be forwarded, got %v", out.ExtraParams)
	}
}

func TestToPerplexityChatCompletionRequest_ConvertingTwiceGivesTheSameRequest(t *testing.T) {
	t.Parallel()

	req := newExtraParamsChatRequest()

	first := ToPerplexityChatCompletionRequest(req)
	second := ToPerplexityChatCompletionRequest(req)

	if first.SearchMode == nil || second.SearchMode == nil || *second.SearchMode != *first.SearchMode {
		t.Errorf("second conversion lost search_mode: first=%v second=%v", first.SearchMode, second.SearchMode)
	}
	if first.ReturnImages == nil || second.ReturnImages == nil {
		t.Errorf("second conversion lost return_images: first=%v second=%v", first.ReturnImages, second.ReturnImages)
	}
}

// The conversion consumes keys from ExtraParams, so every iteration gets a fresh map; the cost
// of building it is the same before and after any change to the conversion.
func BenchmarkToPerplexityChatCompletionRequest_ExtraParams(b *testing.B) {
	req := newExtraParamsChatRequest()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req.Params.ExtraParams = map[string]interface{}{
			"search_mode":   "academic",
			"return_images": true,
			"custom":        "kept",
		}
		_ = ToPerplexityChatCompletionRequest(req)
	}
}
