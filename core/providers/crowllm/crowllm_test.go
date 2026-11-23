package crowllm

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func newErrorResponse(status int, body string) *fasthttp.Response {
	resp := fasthttp.AcquireResponse()
	resp.SetStatusCode(status)
	resp.SetBodyString(body)

	return resp
}

func TestParseCrowLLMImageErrorOpenAIEnvelope(t *testing.T) {
	resp := newErrorResponse(fasthttp.StatusUnauthorized, `{"error":{"message":"invalid api key","type":"auth_error","code":"401"}}`)
	defer fasthttp.ReleaseResponse(resp)

	err := parseCrowLLMImageError(resp)
	if err == nil || err.Error == nil {
		t.Fatal("expected an error field")
	}
	if err.Error.Message != "invalid api key" {
		t.Fatalf("expected the OpenAI envelope message, got %q", err.Error.Message)
	}
}

func TestParseCrowLLMImageErrorDetailString(t *testing.T) {
	resp := newErrorResponse(fasthttp.StatusBadRequest, `{"detail":"prompt cannot be empty"}`)
	defer fasthttp.ReleaseResponse(resp)

	err := parseCrowLLMImageError(resp)
	if err == nil || err.Error == nil {
		t.Fatal("expected an error field")
	}
	if err.Error.Message != "prompt cannot be empty" {
		t.Fatalf("expected the detail string, got %q", err.Error.Message)
	}
}

func TestParseCrowLLMImageErrorValidationList(t *testing.T) {
	resp := newErrorResponse(fasthttp.StatusUnprocessableEntity, `{"detail":[{"loc":["body","width"],"msg":"must be between 64 and 2048","type":"value_error"}]}`)
	defer fasthttp.ReleaseResponse(resp)

	err := parseCrowLLMImageError(resp)
	if err == nil || err.Error == nil {
		t.Fatal("expected an error field")
	}
	want := "must be between 64 and 2048 [body.width]"
	if err.Error.Message != want {
		t.Fatalf("expected %q, got %q", want, err.Error.Message)
	}
}

func TestToCrowLLMImageGenerationRequestConvertsSizeAndFormat(t *testing.T) {
	size := "1024x768"
	format := "jpeg"
	req := &schemas.BifrostImageGenerationRequest{
		Model:  "black-forest-labs/flux-schnell",
		Input:  &schemas.ImageGenerationInput{Prompt: "a crow on a wire"},
		Params: &schemas.ImageGenerationParameters{Size: &size, OutputFormat: &format},
	}

	provider := &CrowLLMProvider{}
	out, err := provider.ToCrowLLMImageGenerationRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Width == nil || *out.Width != 1024 || out.Height == nil || *out.Height != 768 {
		t.Fatalf("expected 1024x768, got %v x %v", out.Width, out.Height)
	}
	if out.ResponseExtension == nil || *out.ResponseExtension != "jpg" {
		t.Fatalf("expected jpeg to map to jpg, got %v", out.ResponseExtension)
	}

	// The conversion must not mutate the input request.
	if *req.Params.Size != "1024x768" {
		t.Fatalf("input size mutated: %q", *req.Params.Size)
	}
	if *req.Params.OutputFormat != "jpeg" {
		t.Fatalf("input format mutated: %q", *req.Params.OutputFormat)
	}
	if req.Input.Prompt != "a crow on a wire" {
		t.Fatalf("input prompt mutated: %q", req.Input.Prompt)
	}
}

func TestToCrowLLMImageGenerationRequestRejectsBadSize(t *testing.T) {
	size := "huge"
	req := &schemas.BifrostImageGenerationRequest{
		Model:  "black-forest-labs/flux-schnell",
		Input:  &schemas.ImageGenerationInput{Prompt: "a crow"},
		Params: &schemas.ImageGenerationParameters{Size: &size},
	}

	provider := &CrowLLMProvider{}
	if _, err := provider.ToCrowLLMImageGenerationRequest(req); err == nil {
		t.Fatal("expected an error for a size without WIDTHxHEIGHT")
	}
}

func TestToCrowLLMImageGenerationRequestRejectsNilInput(t *testing.T) {
	provider := &CrowLLMProvider{}
	if _, err := provider.ToCrowLLMImageGenerationRequest(&schemas.BifrostImageGenerationRequest{Model: "x"}); err == nil {
		t.Fatal("expected an error for a nil input")
	}
}

func TestToCrowLLMImageGenerationRequestKeepsCallerExtraParams(t *testing.T) {
	extra := map[string]interface{}{
		"guidance_scale": 7,
		"loras": []interface{}{
			map[string]interface{}{"url": "https://example.com/lora.safetensors", "scale": 80},
		},
		"steps_hint": "keep-me",
	}
	req := &schemas.BifrostImageGenerationRequest{
		Model:  "black-forest-labs/flux-schnell",
		Input:  &schemas.ImageGenerationInput{Prompt: "a crow"},
		Params: &schemas.ImageGenerationParameters{ExtraParams: extra},
	}

	provider := &CrowLLMProvider{}
	out, err := provider.ToCrowLLMImageGenerationRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.GuidanceScale == nil || *out.GuidanceScale != 7 {
		t.Fatalf("expected guidance_scale 7, got %v", out.GuidanceScale)
	}
	if len(out.Loras) != 1 || out.Loras[0].URL != "https://example.com/lora.safetensors" || out.Loras[0].Scale != 80 {
		t.Fatalf("unexpected loras: %+v", out.Loras)
	}
	if _, ok := out.ExtraParams["steps_hint"]; !ok {
		t.Fatal("expected unrelated extra params to be forwarded")
	}

	// The converted keys stay in the caller's map, so a retry or fallback that
	// reuses the request still carries its LoRA and guidance settings.
	if _, ok := extra["guidance_scale"]; !ok {
		t.Fatal("caller map lost guidance_scale")
	}
	if _, ok := extra["loras"]; !ok {
		t.Fatal("caller map lost loras")
	}

	// A second conversion of the same request must produce the same result.
	again, err := provider.ToCrowLLMImageGenerationRequest(req)
	if err != nil {
		t.Fatalf("unexpected error on second conversion: %v", err)
	}
	if again.GuidanceScale == nil || *again.GuidanceScale != 7 || len(again.Loras) != 1 {
		t.Fatalf("second conversion lost settings: %+v/%+v", again.GuidanceScale, again.Loras)
	}
}

func TestToCrowLLMImageGenerationRequestNilParams(t *testing.T) {
	req := &schemas.BifrostImageGenerationRequest{
		Model: "black-forest-labs/flux-schnell",
		Input: &schemas.ImageGenerationInput{Prompt: "a crow"},
	}

	provider := &CrowLLMProvider{}
	out, err := provider.ToCrowLLMImageGenerationRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Model == nil || *out.Model != "black-forest-labs/flux-schnell" {
		t.Fatalf("unexpected model: %v", out.Model)
	}
	if out.Prompt == nil || *out.Prompt != "a crow" {
		t.Fatalf("unexpected prompt: %v", out.Prompt)
	}
	if out.ExtraParams != nil {
		t.Fatalf("expected no extra params, got %+v", out.ExtraParams)
	}
}

func TestNewCrowLLMProviderDefaults(t *testing.T) {
	provider, err := NewCrowLLMProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.networkConfig.BaseURL != "https://crowllm.com" {
		t.Fatalf("expected the default base URL, got %q", provider.networkConfig.BaseURL)
	}
	if provider.client == nil || provider.streamingClient == nil {
		t.Fatal("expected both HTTP clients to be configured")
	}
}
