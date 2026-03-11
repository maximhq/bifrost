package sapaicore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestParseSAPAICoreError_OpenAIFormat(t *testing.T) {
	t.Parallel()

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(400)
	resp.SetBody([]byte(`{
		"error": {
			"message": "Invalid request",
			"type": "invalid_request_error",
			"code": "invalid_api_key"
		}
	}`))

	result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "gpt-4")

	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.Error == nil {
		t.Fatal("expected error field to be set")
	}
	if result.Error.Message != "Invalid request" {
		t.Errorf("expected message 'Invalid request', got %q", result.Error.Message)
	}
	if result.Error.Type == nil || *result.Error.Type != "invalid_request_error" {
		t.Errorf("expected type 'invalid_request_error', got %v", result.Error.Type)
	}
	if result.Error.Code == nil || *result.Error.Code != "invalid_api_key" {
		t.Errorf("expected code 'invalid_api_key', got %v", result.Error.Code)
	}
}

func TestParseSAPAICoreError_ExtraFieldsSet(t *testing.T) {
	t.Parallel()

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(401)
	resp.SetBody([]byte(`{"error": {"message": "Unauthorized"}}`))

	result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "gpt-4o")

	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.ExtraFields.Provider != schemas.SAPAICore {
		t.Errorf("expected provider SAPAICore, got %v", result.ExtraFields.Provider)
	}
	if result.ExtraFields.ModelRequested != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", result.ExtraFields.ModelRequested)
	}
	if result.ExtraFields.RequestType != schemas.ChatCompletionRequest {
		t.Errorf("expected request type ChatCompletionRequest, got %v", result.ExtraFields.RequestType)
	}
}

func TestParseSAPAICoreError_StatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
	}{
		{"Bad Request", 400},
		{"Unauthorized", 401},
		{"Forbidden", 403},
		{"Not Found", 404},
		{"Rate Limited", 429},
		{"Internal Server Error", 500},
		{"Service Unavailable", 503},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)

			resp.SetStatusCode(tt.statusCode)
			resp.SetBody([]byte(`{"error": {"message": "Error message"}}`))

			result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "gpt-4")

			if result == nil {
				t.Fatal("expected non-nil error")
			}
			if result.StatusCode == nil {
				t.Fatal("expected status code to be set")
			}
			if *result.StatusCode != tt.statusCode {
				t.Errorf("expected status code %d, got %d", tt.statusCode, *result.StatusCode)
			}
		})
	}
}

func TestParseSAPAICoreError_InvalidJSON(t *testing.T) {
	t.Parallel()

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(500)
	resp.SetBody([]byte(`not valid json`))

	result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "gpt-4")

	// Should still return an error, even with invalid JSON
	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.StatusCode == nil || *result.StatusCode != 500 {
		t.Error("expected status code 500")
	}
	// ExtraFields should still be set
	if result.ExtraFields.Provider != schemas.SAPAICore {
		t.Errorf("expected provider SAPAICore, got %v", result.ExtraFields.Provider)
	}
}

func TestParseSAPAICoreError_EmptyBody(t *testing.T) {
	t.Parallel()

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(500)
	resp.SetBody([]byte(``))

	result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "gpt-4")

	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.StatusCode == nil || *result.StatusCode != 500 {
		t.Error("expected status code 500")
	}
}

func TestParseSAPAICoreError_WithEventID(t *testing.T) {
	t.Parallel()

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(400)
	resp.SetBody([]byte(`{
		"event_id": "evt_123",
		"error": {
			"message": "Error with event",
			"event_id": "err_evt_456"
		}
	}`))

	result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "gpt-4")

	if result == nil {
		t.Fatal("expected non-nil error")
	}
	// Top-level event_id
	if result.EventID == nil || *result.EventID != "evt_123" {
		t.Errorf("expected event_id 'evt_123', got %v", result.EventID)
	}
	// Error-level event_id
	if result.Error == nil {
		t.Fatal("expected error field to be set")
	}
	if result.Error.EventID == nil {
		t.Fatal("expected error event_id to be set")
	}
	if *result.Error.EventID != "err_evt_456" {
		t.Errorf("expected error event_id 'err_evt_456', got %q", *result.Error.EventID)
	}
}

func TestParseSAPAICoreError_BedrockFormat(t *testing.T) {
	t.Parallel()

	// Bedrock errors may come through with a different format
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(400)
	resp.SetBody([]byte(`{
		"error": {
			"message": "Bedrock validation error",
			"type": "ValidationException"
		}
	}`))

	result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "anthropic--claude-3-sonnet")

	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.Error == nil {
		t.Fatal("expected error field to be set")
	}
	if result.Error.Type == nil || *result.Error.Type != "ValidationException" {
		t.Errorf("expected type 'ValidationException', got %v", result.Error.Type)
	}
	if result.ExtraFields.ModelRequested != "anthropic--claude-3-sonnet" {
		t.Errorf("expected model 'anthropic--claude-3-sonnet', got %q", result.ExtraFields.ModelRequested)
	}
}

func TestParseSAPAICoreError_PlatformFormat(t *testing.T) {
	t.Parallel()

	// SAP AI Core platform errors use top-level fields, no "error" wrapper
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(403)
	resp.SetBody([]byte(`{
		"message": "Access denied to resource group",
		"code": "403",
		"status": "FORBIDDEN"
	}`))

	result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "gpt-4")

	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.Error == nil {
		t.Fatal("expected error field to be set")
	}
	if result.Error.Message != "Access denied to resource group" {
		t.Errorf("expected message 'Access denied to resource group', got %q", result.Error.Message)
	}
	if result.Error.Code == nil || *result.Error.Code != "403" {
		t.Errorf("expected code '403', got %v", result.Error.Code)
	}
}

func TestParseSAPAICoreError_BedrockTopLevel(t *testing.T) {
	t.Parallel()

	// Bedrock errors with top-level message and __type (no "error" wrapper)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(400)
	resp.SetBody([]byte(`{
		"message": "A]ll model IDs must be provided",
		"__type": "ValidationException"
	}`))

	result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "anthropic--claude-3-sonnet")

	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.Error == nil {
		t.Fatal("expected error field to be set")
	}
	if result.Error.Message != "A]ll model IDs must be provided" {
		t.Errorf("expected message 'A]ll model IDs must be provided', got %q", result.Error.Message)
	}
	if result.Error.Type == nil || *result.Error.Type != "ValidationException" {
		t.Errorf("expected type 'ValidationException', got %v", result.Error.Type)
	}
}

func TestParseSAPAICoreError_PlatformStatusFallback(t *testing.T) {
	t.Parallel()

	// Platform error with "status" but no "code" — status should map to Code
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(404)
	resp.SetBody([]byte(`{
		"message": "Deployment not found",
		"status": "NOT_FOUND"
	}`))

	result := ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, schemas.SAPAICore, "gpt-4")

	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.Error == nil {
		t.Fatal("expected error field to be set")
	}
	if result.Error.Message != "Deployment not found" {
		t.Errorf("expected message 'Deployment not found', got %q", result.Error.Message)
	}
	if result.Error.Code == nil || *result.Error.Code != "NOT_FOUND" {
		t.Errorf("expected code 'NOT_FOUND', got %v", result.Error.Code)
	}
}

func TestParseSAPAICoreError_DifferentRequestTypes(t *testing.T) {
	t.Parallel()

	requestTypes := []schemas.RequestType{
		schemas.ChatCompletionRequest,
		schemas.ChatCompletionStreamRequest,
		schemas.EmbeddingRequest,
		schemas.ListModelsRequest,
	}

	for _, reqType := range requestTypes {
		t.Run(string(reqType), func(t *testing.T) {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)

			resp.SetStatusCode(400)
			resp.SetBody([]byte(`{"error": {"message": "Error"}}`))

			result := ParseSAPAICoreError(resp, reqType, schemas.SAPAICore, "gpt-4")

			if result == nil {
				t.Fatal("expected non-nil error")
			}
			if result.ExtraFields.RequestType != reqType {
				t.Errorf("expected request type %v, got %v", reqType, result.ExtraFields.RequestType)
			}
		})
	}
}
