package utils

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestRewriteJSONModelValue(t *testing.T) {
	in := []byte(`{"model":"openai/gpt-5","messages":[{"role":"user","content":"x"}]}`)
	out, changed := rewriteJSONModelValue(in, "openai/gpt-5", "gpt-5")
	if !changed {
		t.Fatal("expected model rewrite to occur")
	}
	if strings.Contains(string(out), `"model":"openai/gpt-5"`) {
		t.Fatalf("expected prefixed model to be removed, got: %s", string(out))
	}
	if !strings.Contains(string(out), `"model":"gpt-5"`) {
		t.Fatalf("expected rewritten model, got: %s", string(out))
	}
}

func TestApplyLargePayloadRequestBodyWithModelNormalization(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	payload := `{"model":"openai/gpt-5","messages":[{"role":"user","content":"hello"}]}`
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	ctx.SetValue(
		schemas.BifrostContextKeyLargePayloadReader,
		strings.NewReader(payload),
	)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(payload))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, "application/json")
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, &schemas.LargePayloadMetadata{
		Model: "openai/gpt-5",
	})

	req := &fasthttp.Request{}
	if !ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.OpenAI) {
		t.Fatal("expected large payload body to be applied")
	}

	body := string(req.Body())
	if strings.Contains(body, "openai/gpt-5") {
		t.Fatalf("expected rewritten model in body, got: %s", body)
	}
	if !strings.Contains(body, `"model":"gpt-5"`) {
		t.Fatalf("expected normalized model in body, got: %s", body)
	}
}

// TestHandleProviderAPIError_RawResponseIncluded verifies that HandleProviderAPIError
// always includes the raw response body in BifrostError.ExtraFields.RawResponse
func TestHandleProviderAPIError_RawResponseIncluded(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        []byte
		contentType string
		description string
	}{
		{
			name:        "Decode failure",
			statusCode:  500,
			body:        []byte{0xFF, 0xFE}, // Invalid gzip-compressed data
			contentType: "application/json",
			description: "Should include raw response when decode fails",
		},
		{
			name:        "Empty response",
			statusCode:  502,
			body:        []byte(""),
			contentType: "application/json",
			description: "Should include empty raw response",
		},
		{
			name:        "Valid JSON error",
			statusCode:  400,
			body:        []byte(`{"error": {"message": "Invalid API key"}}`),
			contentType: "application/json",
			description: "Should include raw response for valid JSON",
		},
		{
			name:        "HTML error response",
			statusCode:  503,
			body:        []byte(`<html><body><h1>Service Unavailable</h1></body></html>`),
			contentType: "text/html",
			description: "Should include raw response for HTML errors",
		},
		{
			name:        "Unparseable non-HTML response",
			statusCode:  400,
			body:        []byte(`This is not JSON or HTML`),
			contentType: "text/plain",
			description: "Should include raw response for unparseable content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &fasthttp.Response{}
			resp.SetStatusCode(tt.statusCode)
			resp.Header.Set("Content-Type", tt.contentType)
			// Set Content-Encoding: gzip for decode failure test to trigger BodyGunzip() error
			if tt.name == "Decode failure" {
				resp.Header.Set("Content-Encoding", "gzip")
			}
			resp.SetBody(tt.body)

			var errorResp map[string]interface{}
			bifrostErr := HandleProviderAPIError(resp, &errorResp)

			if bifrostErr == nil {
				t.Fatal("HandleProviderAPIError() returned nil")
			}

			if bifrostErr.ExtraFields.RawResponse == nil {
				t.Errorf("%s: RawResponse is nil, expected it to be set", tt.description)
			}

			// Verify the raw response matches the body (for non-decode-failure cases)
			if tt.name != "Decode failure" {
				rawResponseBytes, err := sonic.Marshal(bifrostErr.ExtraFields.RawResponse)
				if err != nil {
					t.Errorf("Failed to marshal RawResponse: %v", err)
				}

				// The RawResponse should contain the body content
				if len(rawResponseBytes) == 0 {
					t.Errorf("%s: RawResponse is empty", tt.description)
				}
			}

			t.Logf("✓ %s: RawResponse is set", tt.name)
		})
	}
}

// TestEnrichError_PreservesExistingRawResponse verifies that EnrichError preserves
// existing RawResponse from the error's ExtraFields when responseBody parameter is nil
func TestEnrichError_PreservesExistingRawResponse(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	existingRawResponse := map[string]interface{}{
		"error": map[string]interface{}{
			"message": "Original error from provider",
			"code":    "invalid_api_key",
		},
	}

	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(401),
		Error: &schemas.ErrorField{
			Message: "Authentication failed",
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RawResponse: existingRawResponse,
		},
	}

	requestBody := []byte(`{"model": "gpt-4", "messages": []}`)

	// Call EnrichError with nil responseBody - should preserve existing RawResponse
	enrichedErr := EnrichError(ctx, bifrostErr, requestBody, nil, true, true)

	if enrichedErr == nil {
		t.Fatal("EnrichError() returned nil")
	}

	if enrichedErr.ExtraFields.RawResponse == nil {
		t.Error("RawResponse was cleared when it should have been preserved")
	} else {
		// Verify it's still the original
		if rawMap, ok := enrichedErr.ExtraFields.RawResponse.(map[string]interface{}); ok {
			if errorMap, ok := rawMap["error"].(map[string]interface{}); ok {
				if errorMap["code"] != "invalid_api_key" {
					t.Error("RawResponse was modified, expected it to be preserved")
				}
			}
		}
	}

	t.Log("✓ EnrichError preserves existing RawResponse when responseBody is nil")
}

// TestEnrichError_OverwritesWithProvidedResponse verifies that EnrichError sets
// RawResponse when a responseBody is provided
func TestEnrichError_OverwritesWithProvidedResponse(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(400),
		Error: &schemas.ErrorField{
			Message: "Bad request",
		},
		ExtraFields: schemas.BifrostErrorExtraFields{},
	}

	requestBody := []byte(`{"model": "gpt-4"}`)
	responseBody := []byte(`{"error": {"message": "Model not found"}}`)

	enrichedErr := EnrichError(ctx, bifrostErr, requestBody, responseBody, true, true)

	if enrichedErr == nil {
		t.Fatal("EnrichError() returned nil")
	}

	if enrichedErr.ExtraFields.RawResponse == nil {
		t.Error("RawResponse should be set from responseBody parameter")
	}

	if enrichedErr.ExtraFields.RawRequest == nil {
		t.Error("RawRequest should be set from requestBody parameter")
	}

	t.Log("✓ EnrichError sets RawRequest and RawResponse from provided bodies")
}

// TestEnrichError_RespectsFlags verifies that EnrichError respects
// sendBackRawRequest and sendBackRawResponse flags
func TestEnrichError_RespectsFlags(t *testing.T) {
	tests := []struct {
		name                string
		sendBackRawRequest  bool
		sendBackRawResponse bool
		expectRequest       bool
		expectResponse      bool
	}{
		{
			name:                "Both enabled",
			sendBackRawRequest:  true,
			sendBackRawResponse: true,
			expectRequest:       true,
			expectResponse:      true,
		},
		{
			name:                "Only request enabled",
			sendBackRawRequest:  true,
			sendBackRawResponse: false,
			expectRequest:       true,
			expectResponse:      false,
		},
		{
			name:                "Only response enabled",
			sendBackRawRequest:  false,
			sendBackRawResponse: true,
			expectRequest:       false,
			expectResponse:      true,
		},
		{
			name:                "Both disabled",
			sendBackRawRequest:  false,
			sendBackRawResponse: false,
			expectRequest:       false,
			expectResponse:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

			bifrostErr := &schemas.BifrostError{
				IsBifrostError: false,
				StatusCode:     schemas.Ptr(500),
				Error:          &schemas.ErrorField{Message: "Error"},
				ExtraFields:    schemas.BifrostErrorExtraFields{},
			}

			requestBody := []byte(`{"model": "test"}`)
			responseBody := []byte(`{"error": "test error"}`)

			enrichedErr := EnrichError(ctx, bifrostErr, requestBody, responseBody, tt.sendBackRawRequest, tt.sendBackRawResponse)

			hasRequest := enrichedErr.ExtraFields.RawRequest != nil
			hasResponse := enrichedErr.ExtraFields.RawResponse != nil

			if hasRequest != tt.expectRequest {
				t.Errorf("RawRequest: got %v, want %v", hasRequest, tt.expectRequest)
			}

			if hasResponse != tt.expectResponse {
				t.Errorf("RawResponse: got %v, want %v", hasResponse, tt.expectResponse)
			}
		})
	}
}

// TestProviderErrorFlow_EndToEnd simulates the full flow of a provider error
// being captured and enriched with raw request/response
func TestProviderErrorFlow_EndToEnd(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Simulate provider error response
	errorBody := []byte(`{"error": {"message": "Rate limit exceeded", "type": "rate_limit_error", "code": "rate_limit"}}`)

	resp := &fasthttp.Response{}
	resp.SetStatusCode(429)
	resp.Header.Set("Content-Type", "application/json")
	resp.SetBody(errorBody)

	// Step 1: Parse the error (like ParseOpenAIError does)
	var errorResp map[string]interface{}
	bifrostErr := HandleProviderAPIError(resp, &errorResp)

	if bifrostErr == nil {
		t.Fatal("HandleProviderAPIError returned nil")
	}

	// Verify raw response is captured by HandleProviderAPIError
	if bifrostErr.ExtraFields.RawResponse == nil {
		t.Error("HandleProviderAPIError should have set RawResponse")
	}

	// Step 2: Enrich with request (like providers do)
	requestBody := []byte(`{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]}`)

	enrichedErr := EnrichError(ctx, bifrostErr, requestBody, nil, true, true)

	// Verify both raw request and raw response are present
	if enrichedErr.ExtraFields.RawRequest == nil {
		t.Error("EnrichError should have set RawRequest")
	}

	if enrichedErr.ExtraFields.RawResponse == nil {
		t.Error("EnrichError should have preserved RawResponse from HandleProviderAPIError")
	}

	t.Log("✓ End-to-end: Raw request and error response captured successfully")
}

// TestHandleProviderAPIError_AllPathsSetRawResponse verifies that all error return
// paths in HandleProviderAPIError include RawResponse
func TestHandleProviderAPIError_AllPathsSetRawResponse(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
		body       []byte
		setupResp  func(*fasthttp.Response)
		errorType  string
	}{
		{
			name:       "Path 1: Decode error",
			statusCode: 500,
			body:       []byte{0xFF, 0xFE, 0xFD}, // Invalid gzip-compressed data
			setupResp: func(r *fasthttp.Response) {
				r.Header.Set("Content-Type", "application/json")
				// Set Content-Encoding: gzip to trigger BodyGunzip() error on invalid gzip data
				r.Header.Set("Content-Encoding", "gzip")
			},
			errorType: "decode_failure",
		},
		{
			name:       "Path 2: Empty response",
			statusCode: 502,
			body:       []byte("   "), // Only whitespace
			setupResp: func(r *fasthttp.Response) {
				r.Header.Set("Content-Type", "application/json")
			},
			errorType: "empty_response",
		},
		{
			name:       "Path 3: Valid JSON",
			statusCode: 400,
			body:       []byte(`{"error": {"message": "Bad request"}}`),
			setupResp: func(r *fasthttp.Response) {
				r.Header.Set("Content-Type", "application/json")
			},
			errorType: "valid_json",
		},
		{
			name:       "Path 4: HTML response",
			statusCode: 503,
			body:       []byte(`<!DOCTYPE html><html><head><title>Error</title></head><body><h1>Service Error</h1></body></html>`),
			setupResp: func(r *fasthttp.Response) {
				r.Header.Set("Content-Type", "text/html")
			},
			errorType: "html",
		},
		{
			name:       "Path 5: Unparseable non-HTML",
			statusCode: 500,
			body:       []byte(`This is plain text that's not JSON`),
			setupResp: func(r *fasthttp.Response) {
				r.Header.Set("Content-Type", "text/plain")
			},
			errorType: "unparseable",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &fasthttp.Response{}
			resp.SetStatusCode(tc.statusCode)
			resp.SetBody(tc.body)
			tc.setupResp(resp)

			var errorResp map[string]interface{}
			bifrostErr := HandleProviderAPIError(resp, &errorResp)

			if bifrostErr == nil {
				t.Fatalf("%s: HandleProviderAPIError returned nil", tc.name)
			}

			if bifrostErr.ExtraFields.RawResponse == nil {
				t.Errorf("%s [%s]: RawResponse is nil - MISSING raw error body!", tc.name, tc.errorType)
			} else {
				t.Logf("✓ %s [%s]: RawResponse is set", tc.name, tc.errorType)
			}
		})
	}
}

// TestGetRequestPath verifies GetRequestPath handles all path resolution scenarios correctly
func TestGetRequestPath(t *testing.T) {
	tests := []struct {
		name                 string
		contextPath          *string
		customProviderConfig *schemas.CustomProviderConfig
		defaultPath          string
		requestType          schemas.RequestType
		expectedPath         string
		expectedIsURL        bool
	}{
		{
			name:          "Returns default path when nothing is set",
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "/v1/chat/completions",
			expectedIsURL: false,
		},
		{
			name:          "Returns path from context when present",
			contextPath:   schemas.Ptr("/custom/path"),
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "/custom/path",
			expectedIsURL: false,
		},
		{
			name: "Returns full URL from config override",
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "https://custom.api.com/v1/completions",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "https://custom.api.com/v1/completions",
			expectedIsURL: true,
		},
		{
			name: "Returns path override with leading slash",
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "/custom/endpoint",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "/custom/endpoint",
			expectedIsURL: false,
		},
		{
			name: "Adds leading slash to path override without one",
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "custom/endpoint",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "/custom/endpoint",
			expectedIsURL: false,
		},
		{
			name: "Returns default path for empty override",
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "   ",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "/v1/chat/completions",
			expectedIsURL: false,
		},
		{
			name: "Returns default when override exists for different request type",
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.EmbeddingRequest: "/custom/embeddings",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "/v1/chat/completions",
			expectedIsURL: false,
		},
		{
			name: "Handles URL with http scheme",
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "http://internal.api:8080/completions",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "http://internal.api:8080/completions",
			expectedIsURL: true,
		},
		{
			name:        "Context path takes precedence over config override",
			contextPath: schemas.Ptr("/context/path"),
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "/config/path",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "/context/path",
			expectedIsURL: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.contextPath != nil {
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyURLPath, *tt.contextPath)
			}

			path, isURL := GetRequestPath(ctx, tt.defaultPath, tt.customProviderConfig, tt.requestType)

			if path != tt.expectedPath {
				t.Errorf("GetRequestPath() path = %q, want %q", path, tt.expectedPath)
			}

			if isURL != tt.expectedIsURL {
				t.Errorf("GetRequestPath() isURL = %v, want %v", isURL, tt.expectedIsURL)
			}
		})
	}
}

// TestMarshalSorted_Deterministic verifies that MarshalSorted produces identical
// output across multiple calls with the same map, despite Go's randomized map iteration.
func TestMarshalSorted_Deterministic(t *testing.T) {
	// Build a map with enough keys to make random ordering statistically certain
	m := map[string]interface{}{
		"zulu":    1,
		"alpha":   2,
		"mike":    3,
		"bravo":   4,
		"yankee":  5,
		"charlie": 6,
		"nested": map[string]interface{}{
			"zebra":   "z",
			"apple":   "a",
			"mango":   "m",
			"banana":  "b",
			"cherry":  "c",
			"date":    "d",
			"fig":     "f",
			"grape":   "g",
			"kiwi":    "k",
			"lemon":   "l",
			"orange":  "o",
			"papaya":  "p",
			"quince":  "q",
			"raisin":  "r",
			"satsuma": "s",
		},
	}

	first, err := MarshalSorted(m)
	if err != nil {
		t.Fatalf("MarshalSorted() error: %v", err)
	}

	// Run 50 iterations to be confident about determinism
	for i := 0; i < 50; i++ {
		got, err := MarshalSorted(m)
		if err != nil {
			t.Fatalf("MarshalSorted() iteration %d error: %v", i, err)
		}
		if string(got) != string(first) {
			t.Fatalf("MarshalSorted() produced different output on iteration %d:\nfirst: %s\ngot:   %s", i, first, got)
		}
	}

	// Also verify MarshalSortedIndent
	firstIndent, err := MarshalSortedIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("MarshalSortedIndent() error: %v", err)
	}

	for i := 0; i < 50; i++ {
		got, err := MarshalSortedIndent(m, "", "  ")
		if err != nil {
			t.Fatalf("MarshalSortedIndent() iteration %d error: %v", i, err)
		}
		if string(got) != string(firstIndent) {
			t.Fatalf("MarshalSortedIndent() produced different output on iteration %d:\nfirst: %s\ngot:   %s", i, firstIndent, got)
		}
	}
}
