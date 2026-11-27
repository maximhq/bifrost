package schemas

import (
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

// Every response variant that carries ExtraFields must come back from WithCost as
// a copy holding the cost, with the original left unpriced; a variant missing
// from the switch would silently drop the cost. Variants with no ExtraFields are
// returned unchanged and must be listed here.
func TestWithCostCoversEveryVariantWithExtraFields(t *testing.T) {
	knownWithoutExtraFields := map[string]bool{"LiveSession": true}
	extraFieldsType := reflect.TypeOf(BifrostResponseExtraFields{})
	cost := &BifrostCost{InputCost: 1, TotalCost: 1}

	rt := reflect.TypeOf(BifrostResponse{})
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.Type.Kind() != reflect.Pointer || field.Type.Elem().Kind() != reflect.Struct {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			original := &BifrostResponse{}
			variant := reflect.New(field.Type.Elem())
			reflect.ValueOf(original).Elem().Field(i).Set(variant)

			extraFields, hasExtraFields := field.Type.Elem().FieldByName("ExtraFields")
			if !hasExtraFields || extraFields.Type != extraFieldsType {
				if !knownWithoutExtraFields[field.Name] {
					t.Fatalf("%s has no ExtraFields; list it in knownWithoutExtraFields", field.Name)
				}
				if got := original.WithCost(cost); got != original {
					t.Fatalf("WithCost on %s = a new response, want the original back", field.Name)
				}
				return
			}
			if knownWithoutExtraFields[field.Name] {
				t.Fatalf("%s now has ExtraFields; remove it from knownWithoutExtraFields", field.Name)
			}

			got := original.WithCost(cost)
			if got == original {
				t.Fatalf("WithCost on %s returned the original, want a copy", field.Name)
			}
			if reflect.ValueOf(got).Elem().Field(i).Pointer() == variant.Pointer() {
				t.Fatalf("WithCost on %s shares the variant struct, whose ExtraFields it writes", field.Name)
			}
			if got.GetExtraFields().Cost != cost {
				t.Fatalf("WithCost on %s: copy carries cost %+v, want %+v", field.Name, got.GetExtraFields().Cost, cost)
			}
			if original.GetExtraFields().Cost != nil {
				t.Fatalf("WithCost on %s wrote the cost onto the original", field.Name)
			}
		})
	}

	var nilResponse *BifrostResponse
	if nilResponse.WithCost(cost) != nil {
		t.Fatal("WithCost on a nil response must return nil")
	}
}

// Post-hook plugins read the response from their own goroutines after core
// returns it, so pricing the response must never write to what they hold.
func TestWithCostDoesNotWriteTheOriginal(t *testing.T) {
	resp := &BifrostResponse{ChatResponse: &BifrostChatResponse{Model: "gpt-6"}}
	resp.ChatResponse.ExtraFields.Provider = OpenAI

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			ef := resp.ChatResponse.ExtraFields
			_ = ef.Cost
			_ = ef.Provider
		}
	}()
	for range 100 {
		priced := resp.WithCost(&BifrostCost{TotalCost: 1})
		if priced.ChatResponse.Model != "gpt-6" || priced.ChatResponse.ExtraFields.Provider != OpenAI {
			t.Errorf("copy lost the response's fields: %+v", priced.ChatResponse)
		}
	}
	wg.Wait()
}

func TestBifrostErrorWithCostReturnsCopy(t *testing.T) {
	status := 500
	original := &BifrostError{StatusCode: &status, ExtraFields: BifrostErrorExtraFields{Provider: OpenAI}}
	cost := &BifrostCost{TotalCost: 1}

	got := original.WithCost(cost)
	if got == original {
		t.Fatal("WithCost returned the original, want a copy")
	}
	if got.ExtraFields.Cost != cost || got.ExtraFields.Provider != OpenAI || got.StatusCode != original.StatusCode {
		t.Fatalf("copy = %+v, want the original's fields plus the cost", got)
	}
	if original.ExtraFields.Cost != nil {
		t.Fatal("WithCost wrote the cost onto the original")
	}

	var nilError *BifrostError
	if nilError.WithCost(cost) != nil {
		t.Fatal("WithCost on a nil error must return nil")
	}
}
