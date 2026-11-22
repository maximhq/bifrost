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
