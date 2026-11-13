package semanticcache

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

type cacheIdentity struct {
	params  string
	request string
}

func cacheIdentityForTest(t *testing.T, plugin *Plugin, req *schemas.BifrostRequest) cacheIdentity {
	t.Helper()
	metadata, err := plugin.buildRequestMetadataForCaching(plugin.createCacheState("req"), req)
	if err != nil {
		t.Fatalf("buildRequestMetadataForCaching failed: %v", err)
	}
	paramsHash, err := hashMap(metadata)
	if err != nil {
		t.Fatalf("hashMap failed: %v", err)
	}
	requestHash, err := plugin.generateRequestHash(req, metadata)
	if err != nil {
		t.Fatalf("generateRequestHash failed: %v", err)
	}
	return cacheIdentity{params: paramsHash, request: requestHash}
}

func TestChatParamsHashIncludesOutputShapingParams(t *testing.T) {
	plugin := &Plugin{config: getDefaultTestConfig()}
	prompt := "Write a one-line tagline for a coffee shop"
	hashFor := func(params *schemas.ChatParameters) cacheIdentity {
		t.Helper()
		return cacheIdentityForTest(t, plugin, &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: &prompt},
				}},
				Params: params,
			},
		})
	}

	audio := func(voice, format string) *schemas.ChatParameters {
		return &schemas.ChatParameters{
			Modalities: []string{"text", "audio"},
			Audio:      &schemas.ChatAudioParameters{Voice: voice, Format: format},
		}
	}
	pairs := map[string][2]*schemas.ChatParameters{
		"n":                    {{N: schemas.Ptr(1)}, {N: schemas.Ptr(3)}},
		"audio":                {audio("alloy", "wav"), audio("verse", "mp3")},
		"top_k":                {{TopK: schemas.Ptr(1)}, {TopK: schemas.Ptr(200)}},
		"reasoning.max_tokens": {{Reasoning: &schemas.ChatReasoning{MaxTokens: schemas.Ptr(1024)}}, {Reasoning: &schemas.ChatReasoning{MaxTokens: schemas.Ptr(16000)}}},
		"reasoning.summary":    {{Reasoning: &schemas.ChatReasoning{Summary: schemas.Ptr("concise")}}, {Reasoning: &schemas.ChatReasoning{Summary: schemas.Ptr("detailed")}}},
	}
	for name, pair := range pairs {
		if hashFor(pair[0]) != hashFor(pair[0]) {
			t.Fatalf("%s: identical params must hash the same", name)
		}
		first, second := hashFor(pair[0]), hashFor(pair[1])
		if first.params == second.params {
			t.Errorf("%s change must change the params hash used by the semantic lookup", name)
		}
		if first.request == second.request {
			t.Errorf("%s change must change the request hash used by the exact lookup", name)
		}
	}
}

func TestResponsesParamsHashIncludesTextFormatSchema(t *testing.T) {
	plugin := &Plugin{config: getDefaultTestConfig()}
	prompt := "Extract the fields from this invoice: ACME Corp, #4411, $120.00, due 2026-12-01"
	hashFor := func(format *schemas.ResponsesTextConfigFormat) cacheIdentity {
		t.Helper()
		return cacheIdentityForTest(t, plugin, &schemas.BifrostRequest{
			RequestType: schemas.ResponsesRequest,
			ResponsesRequest: &schemas.BifrostResponsesRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: &prompt},
				}},
				Params: &schemas.ResponsesParameters{Text: &schemas.ResponsesTextConfig{Format: format}},
			},
		})
	}
	jsonSchema := func(name string, required ...string) *schemas.ResponsesTextConfigFormat {
		return &schemas.ResponsesTextConfigFormat{
			Type:       "json_schema",
			Name:       schemas.Ptr(name),
			Strict:     schemas.Ptr(true),
			JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{Type: schemas.Ptr("object"), Required: required},
		}
	}

	invoice := hashFor(jsonSchema("invoice", "vendor", "total"))
	if invoice != hashFor(jsonSchema("invoice", "vendor", "total")) {
		t.Fatal("identical text.format must hash the same")
	}
	if changed := hashFor(jsonSchema("invoice", "invoice_number", "due_date")); invoice.params == changed.params || invoice.request == changed.request {
		t.Error("text.format schema change must change the cache identity")
	}
	if changed := hashFor(jsonSchema("due_date", "vendor", "total")); invoice.params == changed.params || invoice.request == changed.request {
		t.Error("text.format name change must change the cache identity")
	}
	nonStrict := jsonSchema("invoice", "vendor", "total")
	nonStrict.Strict = schemas.Ptr(false)
	if changed := hashFor(nonStrict); invoice.params == changed.params || invoice.request == changed.request {
		t.Error("text.format strict change must change the cache identity")
	}
}
