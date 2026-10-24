package schemas

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

// Core repopulates the extra fields after RunPostLLMHooks while plugins (governance billing,
// logging, telemetry) still read the response from goroutines their post-hooks started. With
// the values unchanged, that second pass must not write, or -race reports it.
func TestPopulateExtraFieldsLeavesUnchangedFieldsUnwritten(t *testing.T) {
	resp := &BifrostResponse{ChatResponse: &BifrostChatResponse{}}
	resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "gpt-6")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			ef := resp.ChatResponse.ExtraFields
			_ = ef.RequestType
			_ = ef.Provider
			_ = ef.OriginalModelRequested
			_ = ef.ResolvedModelUsed
		}
	}()
	for range 100 {
		resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "gpt-6")
	}
	wg.Wait()
}

func TestPopulateExtraFieldsOverwritesChangedFields(t *testing.T) {
	resp := &BifrostResponse{ChatResponse: &BifrostChatResponse{}}
	resp.ChatResponse.ExtraFields.RequestType = TextCompletionRequest
	resp.ChatResponse.ExtraFields.Provider = Anthropic
	resp.ChatResponse.ExtraFields.OriginalModelRequested = "stale"
	resp.ChatResponse.ExtraFields.ResolvedModelUsed = "stale-resolved"

	resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "")

	ef := resp.ChatResponse.ExtraFields
	if ef.RequestType != ChatCompletionRequest || ef.Provider != OpenAI || ef.OriginalModelRequested != "gpt-6" || ef.ResolvedModelUsed != "gpt-6" {
		t.Fatalf("changed fields not written: %+v", ef)
	}
}

// TestErrorFieldContentFilterDetailsRoundTrip pins that a provider's content-filter
// classification survives an encode and decode, since the log store and a gateway chained
// in front of Bifrost both read errors back through ErrorField.UnmarshalJSON, and that an
// error without one encodes exactly as before (#8799).
func TestErrorFieldContentFilterDetailsRoundTrip(t *testing.T) {
	field := &ErrorField{
		Code:    Ptr("content_filter"),
		Message: "The response was filtered",
		Param:   "prompt",
		ContentFilterDetails: &ContentFilterDetails{Categories: []ContentFilterCategory{
			{Category: "violence", Severity: Ptr("medium"), Filtered: true},
			{Category: "jailbreak", Filtered: true, Detected: Ptr(true)},
		}},
	}
	encoded, err := json.Marshal(field)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	const want = `{"code":"content_filter","message":"The response was filtered","param":"prompt","content_filter_details":{"categories":[{"category":"violence","severity":"medium","filtered":true},{"category":"jailbreak","filtered":true,"detected":true}]}}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s\nwant      %s", encoded, want)
	}

	var stdDecoded, sonicDecoded ErrorField
	if err := json.Unmarshal(encoded, &stdDecoded); err != nil {
		t.Fatalf("encoding/json decode: %v", err)
	}
	if err := Unmarshal(encoded, &sonicDecoded); err != nil {
		t.Fatalf("sonic decode: %v", err)
	}
	for name, decoded := range map[string]ErrorField{"encoding/json": stdDecoded, "sonic": sonicDecoded} {
		if !reflect.DeepEqual(decoded.ContentFilterDetails, field.ContentFilterDetails) {
			t.Errorf("%s decoded content_filter_details = %+v, want %+v", name, decoded.ContentFilterDetails, field.ContentFilterDetails)
		}
	}

	t.Run("an error without a classification encodes as before", func(t *testing.T) {
		encoded, err := json.Marshal(&ErrorField{Code: Ptr("rate_limit_exceeded"), Message: "Rate limit reached"})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if string(encoded) != `{"code":"rate_limit_exceeded","message":"Rate limit reached"}` {
			t.Fatalf("encoded = %s", encoded)
		}
	})
}

// BenchmarkPopulateExtraFieldsUnchanged measures the post-hook repopulation, where the values
// already match the pre-hook pass.
func BenchmarkPopulateExtraFieldsUnchanged(b *testing.B) {
	resp := &BifrostResponse{ChatResponse: &BifrostChatResponse{}}
	resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "gpt-6")
	b.ReportAllocs()
	for b.Loop() {
		resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "gpt-6")
	}
}
