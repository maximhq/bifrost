package crowllm

import (
	"testing"

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
