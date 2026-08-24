package handlers

import (
	"testing"

	"github.com/bytedance/sonic"
)

// TestProbeChatRequestStopParse pins that a chat body's stop and temperature survive
// unmarshalling into ChatRequest, and that extra-param extraction does not error on it.
//
// Known gap this probe surfaced, deliberately NOT asserted here: `stop` is absent from
// chatParamsKnownFields, so extractExtraParams also reports it as an extra param and the
// value ends up in both ChatParameters.Stop and ExtraParams. It is not unique -- 17 of
// ChatParameters' 39 modelled JSON fields are missing from that map, including the
// documented OpenAI parameters stop, top_p, n, seed, top_logprobs, audio, prediction and
// web_search_options (https://developers.openai.com/api/docs/api-reference/chat/create).
//
// The duplication is inert on most routes: MergeExtraParamsIntoJSON is gated on
// BifrostContextKeyPassthroughExtraParams, which only deepseek, sgl, vllm and wafer set.
// Where it IS set, a duplicated param normally merges back its own identical value, so the
// one case that bites is a param compat's dropUnsupportedParams removed from the typed
// side -- ExtraParams still carries it and the merge re-adds it after the drop.
//
// Asserting either shape here would freeze a decision that belongs with the known-fields
// map, so this test asserts only what is unambiguously correct today.
func TestProbeChatRequestStopParse(t *testing.T) {
	body := []byte(`{"model":"bedrock_mantle/anthropic.claude-opus-4-8","messages":[{"role":"user","content":"Count: one, two, three, four in lowercase"}],"stop":["three"],"temperature":0.5}`)
	var cr ChatRequest
	if err := sonic.Unmarshal(body, &cr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cr.ChatParameters == nil {
		t.Fatal("ChatParameters is nil")
	}

	if len(cr.ChatParameters.Stop) != 1 || cr.ChatParameters.Stop[0] != "three" {
		t.Errorf("stop did not survive unmarshalling: %#v", cr.ChatParameters.Stop)
	}
	if cr.ChatParameters.Temperature == nil {
		t.Error("temperature did not survive unmarshalling")
	} else if *cr.ChatParameters.Temperature != 0.5 {
		t.Errorf("temperature = %v, want 0.5", *cr.ChatParameters.Temperature)
	}

	if _, err := extractExtraParams(body, chatParamsKnownFields); err != nil {
		t.Fatalf("extract extra params: %v", err)
	}
}

// OpenRouter also calls its upstream-routing object `provider`. Bifrost's own
// adapter selector lives in the model prefix, so the HTTP parser must retain the
// top-level object as an extra parameter for the native OpenRouter converter.
func TestProbeChatRequestRetainsOpenRouterProviderRouting(t *testing.T) {
	body := []byte(`{"model":"openrouter/deepseek/deepseek-v4-flash-0731","messages":[{"role":"user","content":"route"}],"provider":{"order":["reka"],"allow_fallbacks":false}}`)

	extraParams, err := extractExtraParams(body, chatParamsKnownFields)
	if err != nil {
		t.Fatalf("extract extra params: %v", err)
	}
	routing, ok := extraParams["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider routing was not retained: %#v", extraParams["provider"])
	}
	if allowFallbacks, ok := routing["allow_fallbacks"].(bool); !ok || allowFallbacks {
		t.Fatalf("allow_fallbacks = %#v, want false", routing["allow_fallbacks"])
	}
}
