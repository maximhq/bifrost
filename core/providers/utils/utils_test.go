package utils

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestSetExtraHeaders_AppliesProviderOverrideAndSkipsCaseInsensitive(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
		NetworkConfig: &schemas.ProviderNetworkConfigOverride{
			ExtraHeaders: map[string]string{
				"Authorization": "Bearer override",
				"X-Tenant":      "override",
			},
		},
	})

	req := &fasthttp.Request{}
	SetExtraHeaders(ctx, req, map[string]string{
		"authorization": "Bearer static",
		"X-Tenant":      "static",
		"X-Static":      "yes",
	}, []string{"authorization"})

	if got := string(req.Header.Peek("Authorization")); got != "" {
		t.Fatalf("Authorization header = %q, want skipped", got)
	}
	if got := string(req.Header.Peek("X-Tenant")); got != "override" {
		t.Fatalf("X-Tenant header = %q, want provider override", got)
	}
	if got := string(req.Header.Peek("X-Static")); got != "yes" {
		t.Fatalf("X-Static header = %q, want static header", got)
	}
}

func TestSetExtraHeadersHTTP_AppliesProviderOverrideAndSkipsCaseInsensitive(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
		NetworkConfig: &schemas.ProviderNetworkConfigOverride{
			ExtraHeaders: map[string]string{
				"Authorization": "Bearer override",
				"X-Tenant":      "override",
			},
		},
	})

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	SetExtraHeadersHTTP(ctx, req, map[string]string{
		"authorization": "Bearer static",
		"X-Tenant":      "static",
		"X-Static":      "yes",
	}, []string{"authorization"})

	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization header = %q, want skipped", got)
	}
	if got := req.Header.Get("X-Tenant"); got != "override" {
		t.Fatalf("X-Tenant header = %q, want provider override", got)
	}
	if got := req.Header.Get("X-Static"); got != "yes" {
		t.Fatalf("X-Static header = %q, want static header", got)
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

func TestEnrichError_SetsLatency(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: "provider failed",
		},
	}

	enrichedErr := EnrichError(ctx, bifrostErr, nil, nil, false, false, 42*time.Millisecond)

	if enrichedErr == nil {
		t.Fatal("EnrichError() returned nil")
	}
	if enrichedErr.ExtraFields.Latency != 42 {
		t.Fatalf("latency = %d, want 42", enrichedErr.ExtraFields.Latency)
	}
}

func TestEnrichError_DoesNotSetLatencyWithoutExplicitValue(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: "provider failed",
		},
	}

	enrichedErr := EnrichError(ctx, bifrostErr, nil, nil, false, false)

	if enrichedErr == nil {
		t.Fatal("EnrichError() returned nil")
	}
	if enrichedErr.ExtraFields.Latency != 0 {
		t.Fatalf("latency = %d, want 0", bifrostErr.ExtraFields.Latency)
	}
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
		providerOverride     *schemas.ProviderOverride
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
		// ProviderOverride.BaseURL cases
		{
			name:             "BaseURL override combines with default path",
			providerOverride: &schemas.ProviderOverride{BaseURL: "https://eu.api.openai.com"},
			defaultPath:      "/v1/chat/completions",
			requestType:      schemas.ChatCompletionRequest,
			expectedPath:     "https://eu.api.openai.com/v1/chat/completions",
			expectedIsURL:    true,
		},
		{
			name:             "BaseURL override combines with RequestPathOverrides path",
			providerOverride: &schemas.ProviderOverride{BaseURL: "https://custom.host.com"},
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "/v2/chat",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "https://custom.host.com/v2/chat",
			expectedIsURL: true,
		},
		{
			name:             "BaseURL override with absolute RequestPathOverrides returns override path directly",
			providerOverride: &schemas.ProviderOverride{BaseURL: "https://custom.host.com"},
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "https://other.host.com/v1/completions",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "https://other.host.com/v1/completions",
			expectedIsURL: true,
		},
		{
			name:             "Context path takes precedence over RequestPathOverrides when BaseURL is set",
			contextPath:      schemas.Ptr("/context/path"),
			providerOverride: &schemas.ProviderOverride{BaseURL: "https://custom.host.com"},
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "/config/path",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "https://custom.host.com/context/path",
			expectedIsURL: true,
		},
		{
			name:             "BaseURL with path lacking leading slash gets slash added",
			providerOverride: &schemas.ProviderOverride{BaseURL: "https://custom.host.com"},
			defaultPath:      "v1/chat/completions",
			requestType:      schemas.ChatCompletionRequest,
			expectedPath:     "https://custom.host.com/v1/chat/completions",
			expectedIsURL:    true,
		},
		{
			name:             "Empty context path does not count as pathFromContext — RequestPathOverrides still applied",
			contextPath:      schemas.Ptr(""),
			providerOverride: &schemas.ProviderOverride{BaseURL: "https://custom.host.com"},
			customProviderConfig: &schemas.CustomProviderConfig{
				RequestPathOverrides: map[schemas.RequestType]string{
					schemas.ChatCompletionRequest: "/v2/chat",
				},
			},
			defaultPath:   "/v1/chat/completions",
			requestType:   schemas.ChatCompletionRequest,
			expectedPath:  "https://custom.host.com/v2/chat",
			expectedIsURL: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.contextPath != nil {
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyURLPath, *tt.contextPath)
			}
			if tt.providerOverride != nil {
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyProviderOverride, tt.providerOverride)
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

// TestCheckAndDecodeBody_PooledGzip verifies that CheckAndDecodeBody correctly
// decompresses gzip-encoded responses using pooled gzip readers.
func TestCheckAndDecodeBody_PooledGzip(t *testing.T) {
	tests := []struct {
		name            string
		body            []byte
		contentEncoding string
		wantBody        string
		wantErr         bool
	}{
		{
			name:            "gzip encoded body",
			body:            gzipCompress([]byte(`{"message":"hello world"}`)),
			contentEncoding: "gzip",
			wantBody:        `{"message":"hello world"}`,
			wantErr:         false,
		},
		{
			name:            "gzip with uppercase header",
			body:            gzipCompress([]byte(`test data`)),
			contentEncoding: "GZIP",
			wantBody:        `test data`,
			wantErr:         false,
		},
		{
			name:            "gzip with whitespace in header",
			body:            gzipCompress([]byte(`trimmed`)),
			contentEncoding: "  gzip  ",
			wantBody:        `trimmed`,
			wantErr:         false,
		},
		{
			name:            "no encoding - plain body",
			body:            []byte(`plain text`),
			contentEncoding: "",
			wantBody:        `plain text`,
			wantErr:         false,
		},
		{
			name:            "empty gzip body",
			body:            []byte{},
			contentEncoding: "gzip",
			wantBody:        "",
			wantErr:         false,
		},
		{
			name:            "invalid gzip data",
			body:            []byte{0xFF, 0xFE, 0xFD},
			contentEncoding: "gzip",
			wantErr:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)
			resp.SetBody(tt.body)
			if tt.contentEncoding != "" {
				resp.Header.Set("Content-Encoding", tt.contentEncoding)
			}

			got, err := CheckAndDecodeBody(resp)
			if tt.wantErr {
				if err == nil {
					t.Errorf("CheckAndDecodeBody() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("CheckAndDecodeBody() unexpected error: %v", err)
				return
			}
			if string(got) != tt.wantBody {
				t.Errorf("CheckAndDecodeBody() = %q, want %q", string(got), tt.wantBody)
			}
		})
	}
}

// TestCheckAndDecodeBody_Concurrent verifies no data races with concurrent access.
func TestCheckAndDecodeBody_Concurrent(t *testing.T) {
	testData := []byte(`{"concurrent":"test"}`)
	compressed := gzipCompress(testData)

	done := make(chan bool)
	for i := 0; i < 100; i++ {
		go func() {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)
			resp.SetBody(compressed)
			resp.Header.Set("Content-Encoding", "gzip")

			got, err := CheckAndDecodeBody(resp)
			if err != nil {
				t.Errorf("CheckAndDecodeBody() error: %v", err)
			}
			if string(got) != string(testData) {
				t.Errorf("CheckAndDecodeBody() = %q, want %q", string(got), string(testData))
			}
			done <- true
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}
}

func TestDrainNonSSEStreamReader_SSEWithoutContentTypeStillReadable(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	body := []byte("event: response.created\n\ndata: {\"type\":\"response.completed\"}\n\n")
	reader, drained := DrainNonSSEStreamReader(resp, bytes.NewReader(body))
	if drained {
		t.Fatal("expected SSE-looking response without content type to remain readable")
	}

	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read SSE body after guard: %v", err)
	}
	if string(remaining) != string(body) {
		t.Fatalf("expected SSE body to remain intact, got %q", string(remaining))
	}
}

func TestDrainNonSSEStreamReader_GzipSSEWithoutContentTypeStillReadable(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	body := []byte("data: {\"type\":\"response.completed\"}\n\n")
	compressed := gzipCompress(body)
	resp.Header.Set("Content-Encoding", "gzip")
	resp.SetBodyStream(bytes.NewReader(compressed), len(compressed))

	decompressed, releaseGzip := DecompressStreamBody(resp)
	defer releaseGzip()

	reader, drained := DrainNonSSEStreamReader(resp, decompressed)
	if drained {
		t.Fatal("expected decompressed SSE-looking response without content type to remain readable")
	}

	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read decompressed SSE body after guard: %v", err)
	}
	if string(remaining) != string(body) {
		t.Fatalf("expected decompressed SSE body %q, got %q", string(body), string(remaining))
	}
}

func TestDrainNonSSEStreamReader_JSONWithoutContentTypeDrains(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	body := []byte(`{"error":"not stream"}`)
	reader, drained := DrainNonSSEStreamReader(resp, bytes.NewReader(body))
	if !drained {
		t.Fatal("expected JSON response without content type to be drained")
	}
	if reader != nil {
		t.Fatal("expected drained response to return nil reader")
	}
}

func TestDrainNonSSEStreamReader_UppercaseSSEPrefixDrains(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	body := []byte("DATA: {\"type\":\"response.completed\"}\n\n")
	reader, drained := DrainNonSSEStreamReader(resp, bytes.NewReader(body))
	if !drained {
		t.Fatal("expected uppercase SSE-like prefix to be treated as non-SSE")
	}
	if reader != nil {
		t.Fatal("expected drained response to return nil reader")
	}
}

func TestDrainNonSSEStreamReader_ShortReadSSEPrefix(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	body := []byte("event: response.created\n\ndata: {}\n\n")
	reader, drained := DrainNonSSEStreamReader(resp, &shortReadReader{data: body, chunkSize: 3})
	if drained {
		t.Fatal("expected SSE stream with short-read prefix to remain readable")
	}

	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read after short-read guard: %v", err)
	}
	if string(remaining) != string(body) {
		t.Fatalf("expected body %q, got %q", body, remaining)
	}
}

func TestDrainNonSSEStreamReader_TinyOpenSSEPrefixReturnsPromptly(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
	}{
		{name: "comment", fragment: ":\n\n"},
		{name: "id field", fragment: "id:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)

			pr, pw := io.Pipe()
			defer pr.Close()

			writeErr := make(chan error, 1)
			go func() {
				_, err := pw.Write([]byte(tt.fragment))
				writeErr <- err
			}()

			result := make(chan struct {
				reader  io.Reader
				drained bool
			}, 1)
			go func() {
				reader, drained := DrainNonSSEStreamReader(resp, pr)
				result <- struct {
					reader  io.Reader
					drained bool
				}{reader: reader, drained: drained}
			}()

			var got struct {
				reader  io.Reader
				drained bool
			}
			select {
			case got = <-result:
			case <-time.After(200 * time.Millisecond):
				_ = pw.Close()
				t.Fatal("DrainNonSSEStreamReader blocked waiting for a larger prefix")
			}

			if got.drained {
				_ = pw.Close()
				t.Fatal("expected tiny SSE prefix to remain readable")
			}

			preserved := make([]byte, len(tt.fragment))
			if _, err := io.ReadFull(got.reader, preserved); err != nil {
				_ = pw.Close()
				t.Fatalf("failed to read preserved SSE prefix: %v", err)
			}
			if string(preserved) != tt.fragment {
				_ = pw.Close()
				t.Fatalf("expected preserved prefix %q, got %q", tt.fragment, string(preserved))
			}

			if err := pw.Close(); err != nil {
				t.Fatalf("failed to close pipe writer: %v", err)
			}
			if err := <-writeErr; err != nil {
				t.Fatalf("failed to write prefix: %v", err)
			}
		})
	}
}

func TestDrainNonSSEStreamReader_FragmentedFieldPrefixReturnsPromptly(t *testing.T) {
	tests := []struct {
		name   string
		first  string
		suffix string
	}{
		{name: "data field", first: "d", suffix: "ata: {}\n\n"},
		{name: "event field", first: "e", suffix: "vent: response.completed\n\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)

			pr, pw := io.Pipe()
			defer pr.Close()

			firstWriteErr := make(chan error, 1)
			go func() {
				_, err := pw.Write([]byte(tt.first))
				firstWriteErr <- err
			}()

			result := make(chan struct {
				reader  io.Reader
				drained bool
			}, 1)
			go func() {
				reader, drained := DrainNonSSEStreamReader(resp, pr)
				result <- struct {
					reader  io.Reader
					drained bool
				}{reader: reader, drained: drained}
			}()

			var got struct {
				reader  io.Reader
				drained bool
			}
			select {
			case got = <-result:
			case <-time.After(200 * time.Millisecond):
				_ = pw.Close()
				t.Fatal("DrainNonSSEStreamReader blocked waiting for a full SSE field name")
			}
			if got.drained {
				_ = pw.Close()
				t.Fatal("expected fragmented SSE field prefix to remain readable")
			}
			if err := <-firstWriteErr; err != nil {
				_ = pw.Close()
				t.Fatalf("failed to write first byte: %v", err)
			}

			suffixWriteErr := make(chan error, 1)
			go func() {
				_, err := pw.Write([]byte(tt.suffix))
				if err == nil {
					err = pw.Close()
				}
				suffixWriteErr <- err
			}()

			remaining, err := io.ReadAll(got.reader)
			if err != nil {
				t.Fatalf("failed to read preserved fragmented SSE stream: %v", err)
			}
			if string(remaining) != tt.first+tt.suffix {
				t.Fatalf("expected preserved stream %q, got %q", tt.first+tt.suffix, string(remaining))
			}
			if err := <-suffixWriteErr; err != nil {
				t.Fatalf("failed to write suffix: %v", err)
			}
		})
	}
}

// shortReadReader returns at most chunkSize bytes per Read call, simulating
// a network reader that delivers data in small segments (short reads).
type shortReadReader struct {
	data      []byte
	chunkSize int
	pos       int
}

func (r *shortReadReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	end := r.pos + r.chunkSize
	if end > len(r.data) {
		end = len(r.data)
	}
	n := copy(p, r.data[r.pos:end])
	r.pos += n
	return n, nil
}

// TestDrainNonSSEStreamReader_CodexNoContentType reproduces the original Codex hang:
// a real HTTP server streams valid SSE events but omits Content-Type: text/event-stream.
// Before the fix, DrainNonSSEStreamResponse would drain the body to /dev/null and the
// client would hang waiting for events that never arrived.
func TestDrainNonSSEStreamReader_CodexNoContentType(t *testing.T) {
	const sseBody = "event: response.created\n\ndata: {\"type\":\"response.created\"}\n\nevent: response.completed\n\ndata: {\"type\":\"response.completed\"}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Codex backend: valid SSE, but Content-Type is application/json — not text/event-stream.
		// Setting it explicitly prevents Go's httptest from auto-detecting text/plain.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		fmt.Fprint(w, sseBody)
		if ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(srv.URL)
	req.Header.SetMethod(http.MethodGet)
	resp.StreamBody = true

	if err := (&fasthttp.Client{}).Do(req, resp); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode())
	}

	// Mirror the streaming goroutine path in openai.go
	reader, releaseGzip := DecompressStreamBody(resp)
	defer releaseGzip()

	reader, drained := DrainNonSSEStreamReader(resp, reader)
	if drained {
		t.Fatal("SSE body without Content-Type was drained — reproduces the original Codex hang")
	}

	all, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading SSE body failed: %v", err)
	}
	if string(all) != sseBody {
		t.Fatalf("SSE body corrupted\nwant: %q\n got: %q", sseBody, string(all))
	}
}

// gzipCompress compresses data using gzip for testing.
func gzipCompress(data []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		panic(fmt.Errorf("gzip write: %w", err))
	}
	if err := gz.Close(); err != nil {
		panic(fmt.Errorf("gzip close: %w", err))
	}
	return buf.Bytes()
}

func TestMergeExtraParamsIntoJSON_PreservesKeyOrder(t *testing.T) {
	// JSON with a specific key order that must be preserved
	jsonBody := []byte(`{
  "model": "gpt-4",
  "messages": [],
  "tool_choice": {"type": "function", "function": {"name": "test"}},
  "tools": []
}`)

	extraParams := map[string]interface{}{
		"custom_field": "value",
	}

	result, err := MergeExtraParamsIntoJSON(jsonBody, extraParams)
	if err != nil {
		t.Fatalf("MergeExtraParamsIntoJSON() error: %v", err)
	}

	// Verify original key order is preserved and custom_field is appended
	resultStr := string(result)
	modelIdx := bytes.Index(result, []byte(`"model"`))
	messagesIdx := bytes.Index(result, []byte(`"messages"`))
	toolChoiceIdx := bytes.Index(result, []byte(`"tool_choice"`))
	toolsIdx := bytes.Index(result, []byte(`"tools"`))
	customIdx := bytes.Index(result, []byte(`"custom_field"`))

	if modelIdx >= messagesIdx || messagesIdx >= toolChoiceIdx || toolChoiceIdx >= toolsIdx || toolsIdx >= customIdx {
		t.Fatalf("Key order not preserved. Result:\n%s", resultStr)
	}
}

func TestMergeExtraParamsIntoJSON_OverwriteExistingKey(t *testing.T) {
	jsonBody := []byte(`{"z_first": "original", "a_second": "original"}`)

	extraParams := map[string]interface{}{
		"z_first": "overwritten",
	}

	result, err := MergeExtraParamsIntoJSON(jsonBody, extraParams)
	if err != nil {
		t.Fatalf("MergeExtraParamsIntoJSON() error: %v", err)
	}

	// z_first should still come before a_second (preserving original position)
	zIdx := bytes.Index(result, []byte(`"z_first"`))
	aIdx := bytes.Index(result, []byte(`"a_second"`))
	if zIdx >= aIdx {
		t.Fatalf("Overwritten key should preserve its position. Result: %s", string(result))
	}

	// z_first should have the new value
	if !bytes.Contains(result, []byte(`"overwritten"`)) {
		t.Fatalf("Value should be overwritten. Result: %s", string(result))
	}
}

func TestMergeExtraParamsIntoJSON_DeepMerge(t *testing.T) {
	jsonBody := []byte(`{"outer": {"a": 1, "b": 2}}`)

	extraParams := map[string]interface{}{
		"outer": map[string]interface{}{
			"c": 3,
		},
	}

	result, err := MergeExtraParamsIntoJSON(jsonBody, extraParams)
	if err != nil {
		t.Fatalf("MergeExtraParamsIntoJSON() error: %v", err)
	}

	// Verify the merge happened
	var parsed map[string]interface{}
	if err := sonic.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	outer, ok := parsed["outer"].(map[string]interface{})
	if !ok {
		t.Fatal("outer should be a map")
	}
	if len(outer) != 3 {
		t.Fatalf("outer should have 3 keys after merge, got %d: %v", len(outer), outer)
	}
}

func TestMergeExtraParamsIntoJSON_EmptyExtraParams(t *testing.T) {
	jsonBody := []byte(`{"a": 1, "b": 2}`)
	result, err := MergeExtraParamsIntoJSON(jsonBody, map[string]interface{}{})
	if err != nil {
		t.Fatalf("MergeExtraParamsIntoJSON() error: %v", err)
	}

	// Should be valid JSON with same content
	var parsed map[string]interface{}
	if err := sonic.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("Expected 2 keys, got %d", len(parsed))
	}
}

// TestParseAndSetRawRequest_CompactsJSON verifies that indented JSON input
// (with literal newlines from MarshalIndent) is compacted to a single line.
// This is critical for SSE streaming where newlines break data-line framing.
func TestParseAndSetRawRequest_CompactsJSON(t *testing.T) {
	indentedJSON := []byte(`{
  "model": "gpt-4",
  "messages": [
    {
      "role": "user",
      "content": "Hello"
    }
  ],
  "temperature": 0.7
}`)

	var extraFields schemas.BifrostResponseExtraFields
	ParseAndSetRawRequest(&extraFields, indentedJSON)

	if extraFields.RawRequest == nil {
		t.Fatal("RawRequest should be set")
	}

	raw, ok := extraFields.RawRequest.(json.RawMessage)
	if !ok {
		t.Fatalf("RawRequest should be json.RawMessage, got %T", extraFields.RawRequest)
	}

	// The compacted output must not contain any literal newlines
	if strings.Contains(string(raw), "\n") {
		t.Errorf("Compacted RawRequest should not contain newlines, got:\n%s", string(raw))
	}

	// Verify it's still valid JSON with the same content
	var parsed map[string]interface{}
	if err := sonic.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("Compacted RawRequest is not valid JSON: %v", err)
	}

	if parsed["model"] != "gpt-4" {
		t.Errorf("Expected model=gpt-4, got %v", parsed["model"])
	}
}

// TestParseAndSetRawRequest_PreservesKeyOrdering verifies that JSON key order
// is maintained after compaction. This is essential for LLM prompt caching
// where key ordering affects cache hit rates.
func TestParseAndSetRawRequest_PreservesKeyOrdering(t *testing.T) {
	// Keys are intentionally not alphabetically sorted
	jsonBody := []byte(`{"z_last":"z","a_first":"a","m_middle":"m"}`)

	var extraFields schemas.BifrostResponseExtraFields
	ParseAndSetRawRequest(&extraFields, jsonBody)

	raw := extraFields.RawRequest.(json.RawMessage)
	result := string(raw)

	zIdx := strings.Index(result, `"z_last"`)
	aIdx := strings.Index(result, `"a_first"`)
	mIdx := strings.Index(result, `"m_middle"`)

	if zIdx >= aIdx || aIdx >= mIdx {
		t.Errorf("Key ordering not preserved. Got: %s", result)
	}
}

// TestParseAndSetRawRequest_EmptyBody verifies that empty input is a no-op.
func TestParseAndSetRawRequest_EmptyBody(t *testing.T) {
	var extraFields schemas.BifrostResponseExtraFields
	ParseAndSetRawRequest(&extraFields, []byte{})

	if extraFields.RawRequest != nil {
		t.Error("RawRequest should be nil for empty body")
	}

	ParseAndSetRawRequest(&extraFields, nil)

	if extraFields.RawRequest != nil {
		t.Error("RawRequest should be nil for nil body")
	}
}

// TestParseAndSetRawRequest_SSEStreamingChunks simulates the actual SSE streaming
// flow end-to-end: a response chunk with raw_request containing indented JSON is
// marshaled, framed as SSE "data: <json>\n\n", and then each SSE data line is
// parsed back. This is the exact scenario that caused issue #1905 — pretty-printed
// JSON in raw_request introduced literal newlines that broke SSE data-line framing.
func TestParseAndSetRawRequest_SSEStreamingChunks(t *testing.T) {
	// Simulate indented request body (as produced by MarshalSortedIndent)
	indentedRequest := []byte(`{
  "model": "gpt-4",
  "messages": [
    {
      "role": "user",
      "content": "Hello"
    }
  ],
  "stream": true,
  "temperature": 0.7
}`)

	// Build a response chunk with raw_request set via ParseAndSetRawRequest.
	// Uses BifrostChatResponse which is the actual type marshaled in the streaming path.
	chunk := schemas.BifrostChatResponse{
		ID:     "chatcmpl-test",
		Model:  "gpt-4",
		Object: "chat.completion.chunk",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						Content: schemas.Ptr("Hello"),
					},
				},
			},
		},
	}
	ParseAndSetRawRequest(&chunk.ExtraFields, indentedRequest)

	// Marshal the chunk (exactly like the transport layer does: sonic.Marshal)
	chunkJSON, err := sonic.Marshal(chunk)
	if err != nil {
		t.Fatalf("Failed to marshal chunk: %v", err)
	}

	// Frame as SSE: "data: <json>\n\n" (exactly as in inference.go:1591)
	sseFrame := fmt.Sprintf("data: %s\n\n", chunkJSON)

	// Parse the SSE frame line-by-line as a real SSE client would.
	// Split on \n and check that there is exactly one "data:" line.
	lines := strings.Split(strings.TrimRight(sseFrame, "\n"), "\n")

	var dataLines []string
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, line)
		} else if line != "" {
			// Any non-empty, non-data line means SSE framing is broken —
			// this is exactly what happened in #1905
			t.Errorf("Unexpected non-data line in SSE frame (broken framing): %q", line)
		}
	}

	if len(dataLines) != 1 {
		t.Fatalf("Expected exactly 1 SSE data line, got %d:\n%s", len(dataLines), sseFrame)
	}

	// Parse the JSON payload from the single data line
	jsonPayload := strings.TrimPrefix(dataLines[0], "data: ")
	var parsed schemas.BifrostChatResponse
	if err := sonic.Unmarshal([]byte(jsonPayload), &parsed); err != nil {
		t.Fatalf("Failed to parse SSE data line as JSON (this is the #1905 bug): %v\nPayload: %s", err, jsonPayload)
	}

	// Verify the parsed response has the correct content
	if parsed.ID != "chatcmpl-test" {
		t.Errorf("Expected ID=chatcmpl-test, got %s", parsed.ID)
	}
	if parsed.ExtraFields.RawRequest == nil {
		t.Error("RawRequest should be present in parsed chunk")
	}

	// Verify raw_request round-trips correctly — the client should be able
	// to parse it back into the original request structure
	rawBytes, err := sonic.Marshal(parsed.ExtraFields.RawRequest)
	if err != nil {
		t.Fatalf("Failed to marshal raw_request: %v", err)
	}
	var rawParsed map[string]interface{}
	if err := sonic.Unmarshal(rawBytes, &rawParsed); err != nil {
		t.Fatalf("raw_request is not valid JSON after round-trip: %v", err)
	}
	if rawParsed["model"] != "gpt-4" {
		t.Errorf("Expected raw_request.model=gpt-4, got %v", rawParsed["model"])
	}
}

// TestBuildClientStreamChunk_ImageGenerationStripping verifies that
// BuildClientStreamChunk correctly handles BifrostImageGenerationStreamResponse:
// strips raw fields when in logging-only mode and never mutates the original.
func TestBuildClientStreamChunk_ImageGenerationStripping(t *testing.T) {
	rawReq := json.RawMessage(`{"model":"dall-e-3"}`)
	rawResp := json.RawMessage(`{"data":[{"url":"https://example.com/img.png"}]}`)

	imgResp := &schemas.BifrostImageGenerationStreamResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{
			RawRequest:  rawReq,
			RawResponse: rawResp,
		},
	}

	response := &schemas.BifrostResponse{ImageGenerationStreamResponse: imgResp}

	t.Run("logging-only: raw fields stripped from image gen chunk, original preserved", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyDropRawRequestFromClient, true)
		ctx.SetValue(schemas.BifrostContextKeyDropRawResponseFromClient, true)

		chunk := BuildClientStreamChunk(ctx, response, nil)
		if chunk.BifrostImageGenerationStreamResponse == nil {
			t.Fatal("expected BifrostImageGenerationStreamResponse in chunk")
		}
		if chunk.BifrostImageGenerationStreamResponse.ExtraFields.RawRequest != nil {
			t.Error("expected RawRequest stripped from chunk, but it was present")
		}
		if chunk.BifrostImageGenerationStreamResponse.ExtraFields.RawResponse != nil {
			t.Error("expected RawResponse stripped from chunk, but it was present")
		}
		// Original must not be mutated.
		if imgResp.ExtraFields.RawRequest == nil {
			t.Error("original BifrostImageGenerationStreamResponse.ExtraFields.RawRequest was mutated")
		}
		if imgResp.ExtraFields.RawResponse == nil {
			t.Error("original BifrostImageGenerationStreamResponse.ExtraFields.RawResponse was mutated")
		}
		if chunk.BifrostImageGenerationStreamResponse == imgResp {
			t.Error("chunk contains same pointer as original; it must be a copy")
		}
	})

	t.Run("no logging flag: raw fields preserved in image gen chunk", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		chunk := BuildClientStreamChunk(ctx, response, nil)
		if chunk.BifrostImageGenerationStreamResponse == nil {
			t.Fatal("expected BifrostImageGenerationStreamResponse in chunk")
		}
		if chunk.BifrostImageGenerationStreamResponse.ExtraFields.RawRequest == nil {
			t.Error("expected RawRequest present in chunk, but it was nil")
		}
		if chunk.BifrostImageGenerationStreamResponse.ExtraFields.RawResponse == nil {
			t.Error("expected RawResponse present in chunk, but it was nil")
		}
	})
}

// TestProcessAndSendResponse_StoreRawLoggingOnly_StripsRawDataFromResponseChunk verifies
// that when drop-raw context flags are set, ProcessAndSendResponse strips RawRequest and
// RawResponse from the outgoing stream chunk, while leaving other ExtraFields intact.
// It also verifies that the original BifrostResponse is not mutated
// (shared object safety for PostLLMHook goroutines).
func TestProcessAndSendResponse_StoreRawLoggingOnly_StripsRawDataFromResponseChunk(t *testing.T) {
	rawReq := json.RawMessage(`{"model":"gpt-4","messages":[]}`)
	rawResp := json.RawMessage(`{"id":"chatcmpl-001"}`)

	tests := []struct {
		name           string
		loggingOnly    bool
		expectStripped bool
	}{
		{
			name:           "logging-only flag set: raw data stripped from chunk",
			loggingOnly:    true,
			expectStripped: true,
		},
		{
			name:           "logging-only flag not set: raw data preserved in chunk",
			loggingOnly:    false,
			expectStripped: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.loggingOnly {
				ctx.SetValue(schemas.BifrostContextKeyDropRawRequestFromClient, true)
				ctx.SetValue(schemas.BifrostContextKeyDropRawResponseFromClient, true)
			}

			response := &schemas.BifrostResponse{
				ChatResponse: &schemas.BifrostChatResponse{
					ID:    "chatcmpl-001",
					Model: "gpt-4",
					ExtraFields: schemas.BifrostResponseExtraFields{
						RawRequest:  rawReq,
						RawResponse: rawResp,
					},
				},
			}

			passThrough := func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return resp, err
			}

			responseChan := make(chan *schemas.BifrostStreamChunk, 1)
			ProcessAndSendResponse(ctx, passThrough, response, responseChan, nil)

			chunk := <-responseChan
			if chunk.BifrostChatResponse == nil {
				t.Fatal("expected non-nil BifrostChatResponse in stream chunk")
			}

			hasRawReq := chunk.BifrostChatResponse.ExtraFields.RawRequest != nil
			hasRawResp := chunk.BifrostChatResponse.ExtraFields.RawResponse != nil

			if tt.expectStripped {
				if hasRawReq {
					t.Error("expected RawRequest to be nil (stripped) in chunk, but it was present")
				}
				if hasRawResp {
					t.Error("expected RawResponse to be nil (stripped) in chunk, but it was present")
				}
				// Critical: the original shared object must NOT have been mutated.
				if response.ChatResponse.ExtraFields.RawRequest == nil {
					t.Error("original BifrostResponse.ChatResponse.ExtraFields.RawRequest was mutated (nil); shared object must be preserved")
				}
				if response.ChatResponse.ExtraFields.RawResponse == nil {
					t.Error("original BifrostResponse.ChatResponse.ExtraFields.RawResponse was mutated (nil); shared object must be preserved")
				}
				// The chunk must be a copy, not the same pointer as the original.
				if chunk.BifrostChatResponse == response.ChatResponse {
					t.Error("chunk.BifrostChatResponse is the same pointer as the original; it must be a copy to avoid data races")
				}
			} else {
				if !hasRawReq {
					t.Error("expected RawRequest to be present in chunk, but it was nil")
				}
				if !hasRawResp {
					t.Error("expected RawResponse to be present in chunk, but it was nil")
				}
			}
		})
	}
}

// TestProcessAndSendResponse_StoreRawLoggingOnly_StripsRawDataFromErrorChunk verifies
// that when drop-raw context flags are set, raw data is stripped from BifrostError
// payloads embedded in stream chunks, without mutating the shared BifrostError object
// (shared object safety for PostLLMHook goroutines).
func TestProcessAndSendResponse_StoreRawLoggingOnly_StripsRawDataFromErrorChunk(t *testing.T) {
	rawReq := json.RawMessage(`{"model":"gpt-4"}`)
	rawResp := json.RawMessage(`{"error":"rate limit exceeded"}`)

	tests := []struct {
		name           string
		loggingOnly    bool
		expectStripped bool
	}{
		{
			name:           "logging-only flag set: raw data stripped from error chunk",
			loggingOnly:    true,
			expectStripped: true,
		},
		{
			name:           "logging-only flag not set: raw data preserved in error chunk",
			loggingOnly:    false,
			expectStripped: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.loggingOnly {
				ctx.SetValue(schemas.BifrostContextKeyDropRawRequestFromClient, true)
				ctx.SetValue(schemas.BifrostContextKeyDropRawResponseFromClient, true)
			}

			// Use a postHookRunner that converts the response to a BifrostError with raw data
			bifrostErr := &schemas.BifrostError{
				IsBifrostError: false,
				StatusCode:     schemas.Ptr(429),
				Error:          &schemas.ErrorField{Message: "rate limit exceeded"},
				ExtraFields: schemas.BifrostErrorExtraFields{
					RawRequest:  rawReq,
					RawResponse: rawResp,
				},
			}

			errorRunner := func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return nil, bifrostErr
			}

			responseChan := make(chan *schemas.BifrostStreamChunk, 1)
			ProcessAndSendResponse(ctx, errorRunner, &schemas.BifrostResponse{
				ChatResponse: &schemas.BifrostChatResponse{ID: "chatcmpl-001"},
			}, responseChan, nil)

			chunk := <-responseChan
			if chunk.BifrostError == nil {
				t.Fatal("expected non-nil BifrostError in stream chunk")
			}

			hasRawReq := chunk.BifrostError.ExtraFields.RawRequest != nil
			hasRawResp := chunk.BifrostError.ExtraFields.RawResponse != nil

			if tt.expectStripped {
				if hasRawReq {
					t.Error("expected RawRequest to be nil (stripped) in error chunk, but it was present")
				}
				if hasRawResp {
					t.Error("expected RawResponse to be nil (stripped) in error chunk, but it was present")
				}
				// Critical: the original shared BifrostError must NOT have been mutated.
				if bifrostErr.ExtraFields.RawRequest == nil {
					t.Error("original BifrostError.ExtraFields.RawRequest was mutated (nil); shared object must be preserved")
				}
				if bifrostErr.ExtraFields.RawResponse == nil {
					t.Error("original BifrostError.ExtraFields.RawResponse was mutated (nil); shared object must be preserved")
				}
				// The chunk must hold a copy, not the same pointer as the original.
				if chunk.BifrostError == bifrostErr {
					t.Error("chunk.BifrostError is the same pointer as the original; it must be a copy to avoid data races")
				}
			} else {
				if !hasRawReq {
					t.Error("expected RawRequest to be present in error chunk, but it was nil")
				}
				if !hasRawResp {
					t.Error("expected RawResponse to be present in error chunk, but it was nil")
				}
			}
		})
	}
}

// TestShouldSendBackRawRequest verifies that ShouldSendBackRawRequest correctly resolves
// whether providers should capture the raw request body. It covers:
//   - Default (no context flags): returns the provider default
//   - BifrostContextKeyCaptureRawRequest=true in context: always returns true
//   - Logging-only mode: requestWorker sets BifrostContextKeyCaptureRawRequest=true,
//     so the function sees a single flag (no second check needed).
func TestShouldSendBackRawRequest(t *testing.T) {
	tests := []struct {
		name            string
		contextSendBack bool
		providerDefault bool
		want            bool
	}{
		{
			name: "provider default false, no context flag",
			want: false,
		},
		{
			name:            "provider default true, no context flag",
			providerDefault: true,
			want:            true,
		},
		{
			name:            "context SendBack=true overrides provider default false",
			contextSendBack: true,
			want:            true,
		},
		{
			name:            "context SendBack=true with provider default true",
			contextSendBack: true,
			providerDefault: true,
			want:            true,
		},
		{
			// requestWorker sets BifrostContextKeyCaptureRawRequest=true in logging-only
			// mode so a single flag covers both full send-back and logging-only cases.
			name:            "logging-only: context SendBack=true set by requestWorker",
			contextSendBack: true,
			providerDefault: false,
			want:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.contextSendBack {
				ctx.SetValue(schemas.BifrostContextKeyCaptureRawRequest, true)
			}

			got := ShouldSendBackRawRequest(ctx, tt.providerDefault)
			if got != tt.want {
				t.Errorf("ShouldSendBackRawRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestShouldSendBackRawResponse mirrors TestShouldSendBackRawRequest for the response side.
func TestShouldSendBackRawResponse(t *testing.T) {
	tests := []struct {
		name            string
		contextSendBack bool
		providerDefault bool
		want            bool
	}{
		{
			name: "provider default false, no context flag",
			want: false,
		},
		{
			name:            "provider default true, no context flag",
			providerDefault: true,
			want:            true,
		},
		{
			name:            "context SendBack=true overrides provider default false",
			contextSendBack: true,
			want:            true,
		},
		{
			name:            "context SendBack=true with provider default true",
			contextSendBack: true,
			providerDefault: true,
			want:            true,
		},
		{
			// requestWorker sets BifrostContextKeyCaptureRawResponse=true in logging-only
			// mode so a single flag covers both full send-back and logging-only cases.
			name:            "logging-only: context SendBack=true set by requestWorker",
			contextSendBack: true,
			providerDefault: false,
			want:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.contextSendBack {
				ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
			}

			got := ShouldSendBackRawResponse(ctx, tt.providerDefault)
			if got != tt.want {
				t.Errorf("ShouldSendBackRawResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetBudgetTokensFromReasoningEffort(t *testing.T) {
	const min = 1024
	const max = 16000

	tests := []struct {
		effort  string
		wantErr bool
		check   func(t *testing.T, budget int)
	}{
		{
			effort: "none",
			check:  func(t *testing.T, budget int) { assertEqual(t, 0, budget, "none effort") },
		},
		{
			effort: "minimal",
			check: func(t *testing.T, budget int) {
				assertRange(t, min, max-1, budget, "minimal")
			},
		},
		{
			effort: "low",
			check: func(t *testing.T, budget int) {
				assertRange(t, min, max-1, budget, "low")
			},
		},
		{
			effort: "medium",
			check: func(t *testing.T, budget int) {
				assertRange(t, min, max-1, budget, "medium")
			},
		},
		{
			effort: "high",
			check: func(t *testing.T, budget int) {
				assertRange(t, min, max-1, budget, "high")
			},
		},
		{
			effort: "xhigh",
			check: func(t *testing.T, budget int) {
				assertRange(t, min, max-1, budget, "xhigh")
			},
		},
		{
			// "max" with ratio=1.0 would produce budget==maxTokens without the cap.
			// Bedrock and Anthropic both require budget_tokens < max_tokens (strict).
			effort: "max",
			check: func(t *testing.T, budget int) {
				if budget >= max {
					t.Errorf("max effort: budget %d must be < maxTokens %d", budget, max)
				}
				assertEqual(t, max-1, budget, "max effort caps at maxTokens-1")
			},
		},
		{
			effort: "unknown",
			check: func(t *testing.T, budget int) {
				assertRange(t, min, max-1, budget, "unknown effort uses safe default")
			},
		},
		{
			// minBudgetTokens > maxTokens — always an error
			effort:  "high",
			wantErr: true,
		},
	}

	for i, tt := range tests {
		t.Run(fmt.Sprintf("%d_%s", i, tt.effort), func(t *testing.T) {
			maxTokens := max
			minTokens := min
			if tt.wantErr {
				minTokens = max + 1
			}
			budget, err := GetBudgetTokensFromReasoningEffort(tt.effort, minTokens, maxTokens)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error when minBudgetTokens > maxTokens, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, budget)
		})
	}
}

func assertEqual(t *testing.T, want, got int, label string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %d, want %d", label, got, want)
	}
}

func assertRange(t *testing.T, low, high, got int, label string) {
	t.Helper()
	if got < low || got > high {
		t.Errorf("%s: got %d, want in [%d, %d]", label, got, low, high)
	}
}

// TestGetStreamIdleTimeout_OverridePrecedenceWithoutPersisting pins the B-phase
// fix for the override leak across fallback attempts: a per-request
// ProviderOverride stream idle timeout is read at GetStreamIdleTimeout time and
// must never be written into BifrostContextKeyStreamIdleTimeout. The override
// ctx key is cleared and re-set per attempt, so each attempt observes only its
// own override; the shared timeout ctx key carries only transport/config values.
func TestGetStreamIdleTimeout_OverridePrecedenceWithoutPersisting(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	primarySeconds := 7
	ctx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
		NetworkConfig: &schemas.ProviderNetworkConfigOverride{StreamIdleTimeoutInSeconds: &primarySeconds},
	})

	// Provider config writes its own value into the shared ctx key; the override
	// must NOT be persisted there.
	SetStreamIdleTimeoutIfEmpty(ctx, 60)
	if got, _ := ctx.Value(schemas.BifrostContextKeyStreamIdleTimeout).(time.Duration); got != 60*time.Second {
		t.Fatalf("ctx stream idle timeout = %v, want 60s from provider config (override must not be written to ctx)", got)
	}

	// The override outranks the ctx value at read time.
	if got := GetStreamIdleTimeout(ctx); got != 7*time.Second {
		t.Fatalf("GetStreamIdleTimeout with override = %v, want 7s", got)
	}

	// Simulate a fallback attempt: the per-attempt override is cleared and
	// replaced. The fallback's own override must win; the primary's 7s must not
	// linger anywhere.
	fallbackSeconds := 9
	ctx.ClearValue(schemas.BifrostContextKeyProviderOverride)
	ctx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
		NetworkConfig: &schemas.ProviderNetworkConfigOverride{StreamIdleTimeoutInSeconds: &fallbackSeconds},
	})
	if got := GetStreamIdleTimeout(ctx); got != 9*time.Second {
		t.Fatalf("GetStreamIdleTimeout after fallback override swap = %v, want 9s (primary attempt's override leaked)", got)
	}

	// A fallback attempt with no override of its own falls back to the shared
	// ctx value, not the primary's override.
	ctx.ClearValue(schemas.BifrostContextKeyProviderOverride)
	if got := GetStreamIdleTimeout(ctx); got != 60*time.Second {
		t.Fatalf("GetStreamIdleTimeout with override cleared = %v, want 60s ctx value (primary attempt's override leaked)", got)
	}
}

// TestSetStreamIdleTimeoutIfEmpty_RespectsExistingValue pins that a timeout set
// upstream (transport/header) wins over the provider config fallback, and that
// GetStreamIdleTimeout returns DefaultStreamIdleTimeout when nothing is set.
func TestSetStreamIdleTimeoutIfEmpty_RespectsExistingValue(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 5*time.Second)

	SetStreamIdleTimeoutIfEmpty(ctx, 60)
	if got := GetStreamIdleTimeout(ctx); got != 5*time.Second {
		t.Fatalf("GetStreamIdleTimeout = %v, want 5s (upstream value must be respected)", got)
	}

	empty := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if got := GetStreamIdleTimeout(empty); got != DefaultStreamIdleTimeout {
		t.Fatalf("GetStreamIdleTimeout on empty ctx = %v, want default %v", got, DefaultStreamIdleTimeout)
	}
}

// TestExtractProviderResponseHeaders_StripsProviderSecrets verifies that provider auth headers
// Bifrost injects upstream (and some upstreams echo back, e.g. Google's file-download 302) are
// never forwarded to clients via the response header map, while normal headers pass through.
// Regression test for the /genai_passthrough x-goog-api-key leak.
func TestExtractProviderResponseHeaders_StripsProviderSecrets(t *testing.T) {
	resp := &fasthttp.Response{}
	// Provider secrets that must be stripped (case-insensitive).
	resp.Header.Set("x-goog-api-key", "AIzaSyEXAMPLE_SECRET")
	resp.Header.Set("X-Api-Key", "sk-ant-secret")
	resp.Header.Set("Api-Key", "azure-secret")
	resp.Header.Set("Authorization", "Bearer secret-token")
	// Benign headers that must be preserved.
	resp.Header.Set("x-request-id", "req-123")

	headers := ExtractProviderResponseHeaders(resp)

	// fasthttp canonicalizes header keys, so look these up case-insensitively.
	lookup := func(name string) (string, bool) {
		for k, v := range headers {
			if strings.EqualFold(k, name) {
				return v, true
			}
		}
		return "", false
	}

	for _, secret := range []string{"x-goog-api-key", "x-api-key", "api-key", "authorization"} {
		if _, ok := lookup(secret); ok {
			t.Fatalf("provider secret header %q leaked in response headers: %v", secret, headers)
		}
	}
	if v, ok := lookup("x-request-id"); !ok || v != "req-123" {
		t.Fatalf("benign header x-request-id was dropped: %v", headers)
	}
}

// TestExtractPassthroughProviderResponseHeaders verifies that the passthrough
// variant preserves content-type while still blocking transport headers and
// provider secrets. Regression guard for the providerResponseFilterHeaders[kLower] &&
// kLower != "content-type" carve-out.
func TestExtractPassthroughProviderResponseHeaders(t *testing.T) {
	resp := &fasthttp.Response{}
	// content-type must be forwarded.
	resp.Header.Set("Content-Type", "application/json")
	// transport headers that must still be stripped.
	resp.Header.Set("Content-Encoding", "gzip")
	resp.Header.Set("Transfer-Encoding", "chunked")
	resp.Header.Set("Content-Length", "42")
	// provider secrets that must still be stripped.
	resp.Header.Set("x-goog-api-key", "AIzaSyEXAMPLE_SECRET")
	resp.Header.Set("X-Api-Key", "sk-secret")
	resp.Header.Set("Authorization", "Bearer token")
	// benign headers that must be preserved.
	resp.Header.Set("x-request-id", "req-456")

	headers := ExtractPassthroughProviderResponseHeaders(resp)

	lookup := func(name string) (string, bool) {
		for k, v := range headers {
			if strings.EqualFold(k, name) {
				return v, true
			}
		}
		return "", false
	}

	// content-type must pass through.
	if v, ok := lookup("content-type"); !ok || v != "application/json" {
		t.Fatalf("content-type should be forwarded by passthrough extractor, got %q ok=%v", v, ok)
	}
	// transport headers must still be stripped.
	for _, stripped := range []string{"content-encoding", "transfer-encoding", "content-length"} {
		if _, ok := lookup(stripped); ok {
			t.Fatalf("transport header %q should be stripped by passthrough extractor", stripped)
		}
	}
	// provider secrets must still be stripped.
	for _, secret := range []string{"x-goog-api-key", "x-api-key", "authorization"} {
		if _, ok := lookup(secret); ok {
			t.Fatalf("provider secret %q should be stripped by passthrough extractor", secret)
		}
	}
	// benign header must pass through.
	if v, ok := lookup("x-request-id"); !ok || v != "req-456" {
		t.Fatalf("benign header x-request-id was dropped: %v", headers)
	}
}

func TestStripThoughtSignature(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no separator", "call_abc123", "call_abc123"},
		{"gemini embedded signature", "search_ts_QUJDREVG", "search"},
		{"base id is also a gemini id", "fc_123_ts_QUJD", "fc_123"},
		{"separator only", "_ts_QUJD", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripThoughtSignature(tc.in); got != tc.want {
				t.Errorf("StripThoughtSignature(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeAnthropicToolUseID(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"already valid", "call_abc123_XYZ-9"},
		{"kimi-style colon and dot", "functions.Bash:0"},
		{"gemini-style slash", "projects/foo/tool/1"},
		{"only unsafe chars", "::.."},
		{"long id with one unsafe char", "a-very-long-tool-call-identifier-that-goes-on-and-on:0"},
		{"long id with many unsafe chars", strings.Repeat("segment.with.dots/and:colons/", 5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeAnthropicToolUseID(tc.in)

			// Anthropic's pattern requires at least one character, so an already-valid,
			// non-empty id is the only case left unchanged; everything else (including
			// the empty string) must be rewritten to a non-empty, conforming id.
			if tc.in != "" && !anthropicUnsafeToolUseIDCharRegex.MatchString(tc.in) {
				if got != tc.in {
					t.Errorf("SanitizeAnthropicToolUseID(%q) = %q, want unchanged", tc.in, got)
				}
				return
			}
			if got == "" {
				t.Errorf("SanitizeAnthropicToolUseID(%q) = empty, want a non-empty conforming id", tc.in)
			}
			if anthropicUnsafeToolUseIDCharRegex.MatchString(got) {
				t.Errorf("SanitizeAnthropicToolUseID(%q) = %q, still contains unsafe characters", tc.in, got)
			}
			if len(got) > maxSanitizedAnthropicToolUseIDLen {
				t.Errorf("SanitizeAnthropicToolUseID(%q) = %q (len %d), exceeds %d-char cap", tc.in, got, len(got), maxSanitizedAnthropicToolUseIDLen)
			}
			if got2 := SanitizeAnthropicToolUseID(tc.in); got2 != got {
				t.Errorf("SanitizeAnthropicToolUseID(%q) is not deterministic: %q != %q", tc.in, got, got2)
			}
		})
	}

	// A tool_use id and its matching tool_result id must sanitize identically,
	// since Anthropic requires them to reference the same value.
	toolUseID := "functions.get_weather:0"
	if SanitizeAnthropicToolUseID(toolUseID) != SanitizeAnthropicToolUseID(toolUseID) {
		t.Error("matching tool_use/tool_result ids diverged after sanitization")
	}

	// Distinct ids that collapse to the same replaced-character skeleton must
	// still sanitize to distinct values (hash is computed on the original id).
	if SanitizeAnthropicToolUseID("functions.Bash:0") == SanitizeAnthropicToolUseID("functions.Bash:1") {
		t.Error("distinct tool ids sanitized to the same value")
	}
}

func TestSanitizeAnthropicToolUseIDPtr(t *testing.T) {
	if got := SanitizeAnthropicToolUseIDPtr(nil); got != nil {
		t.Errorf("SanitizeAnthropicToolUseIDPtr(nil) = %v, want nil", got)
	}

	id := "functions.Bash:0"
	got := SanitizeAnthropicToolUseIDPtr(&id)
	if got == nil {
		t.Fatal("SanitizeAnthropicToolUseIDPtr returned nil for non-nil input")
	}
	if *got != SanitizeAnthropicToolUseID(id) {
		t.Errorf("SanitizeAnthropicToolUseIDPtr(%q) = %q, want %q", id, *got, SanitizeAnthropicToolUseID(id))
	}
}

// finalizerTestTracer is a minimal schemas.Tracer that models only the
// deferred-span lifecycle: a span stays parked until ClearDeferredSpan runs.
// It records the status passed to EndSpan so tests can assert span outcomes.
type finalizerTestTracer struct {
	schemas.NoOpTracer
	parked    bool
	endStatus schemas.SpanStatus
}

func (t *finalizerTestTracer) GetDeferredSpanHandle(_ string) schemas.SpanHandle {
	if t.parked {
		return struct{}{} // any non-nil handle
	}
	return nil
}

func (t *finalizerTestTracer) ClearDeferredSpan(_ string) { t.parked = false }

func (t *finalizerTestTracer) EndSpan(_ schemas.SpanHandle, status schemas.SpanStatus, _ string) {
	t.endStatus = status
}

// A streaming goroutine that exits without reaching the final-chunk path (a
// failed final send with a live context, or a mid-stream death) must not leak
// its deferred span. EnsureStreamFinalizerCalled runs on every goroutine exit
// and clears it.
func TestEnsureStreamFinalizerCalled_ClearsOrphanedDeferredSpan(t *testing.T) {
	tracer := &finalizerTestTracer{parked: true}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")

	if tracer.GetDeferredSpanHandle("trace-1") == nil {
		t.Fatal("expected a parked deferred span before the finalizer runs")
	}

	EnsureStreamFinalizerCalled(ctx, func(context.Context) {})

	if tracer.GetDeferredSpanHandle("trace-1") != nil {
		t.Error("deferred span should be cleared when the streaming goroutine exits")
	}
}

// The clear must survive a nil finalizer (finalizer is optional; the span
// cleanup is not).
func TestEnsureStreamFinalizerCalled_ClearsDeferredSpanWithNilFinalizer(t *testing.T) {
	tracer := &finalizerTestTracer{parked: true}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")

	EnsureStreamFinalizerCalled(ctx, nil)

	if tracer.GetDeferredSpanHandle("trace-1") != nil {
		t.Error("deferred span should be cleared even when no finalizer is registered")
	}
}

// When the terminal path already cleared the span (the common case), the
// safety-net completion is a no-op (handle == nil early return) but the
// finalizer must still run exactly once.
func TestEnsureStreamFinalizerCalled_NoParkedSpan_StillRunsFinalizerOnce(t *testing.T) {
	tracer := &finalizerTestTracer{parked: false}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")

	calls := 0
	EnsureStreamFinalizerCalled(ctx, func(context.Context) { calls++ })

	if calls != 1 {
		t.Errorf("finalizer should run exactly once on the no-op path, ran %d times", calls)
	}
}

// A stream that exits with its span parked and no terminal chunk
// (StreamEndIndicator unset) died mid-flight and must be marked failed — not OK.
func TestEnsureStreamFinalizerCalled_IncompleteStreamMarkedError(t *testing.T) {
	tracer := &finalizerTestTracer{parked: true}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")
	// StreamEndIndicator deliberately unset: the stream never reached its end.

	EnsureStreamFinalizerCalled(ctx, func(context.Context) {})

	if tracer.endStatus != schemas.SpanStatusError {
		t.Errorf("incomplete stream should end as %q, got %q", schemas.SpanStatusError, tracer.endStatus)
	}
}

// A stream that reached its terminal chunk (StreamEndIndicator set) but was left
// parked — e.g. the final send failed — succeeded at the LLM level and must keep
// its OK status, never a fabricated error.
func TestEnsureStreamFinalizerCalled_CompletedStreamKeepsOkStatus(t *testing.T) {
	tracer := &finalizerTestTracer{parked: true}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	EnsureStreamFinalizerCalled(ctx, func(context.Context) {})

	if tracer.endStatus != schemas.SpanStatusOk {
		t.Errorf("completed-but-undelivered stream should end as %q, got %q", schemas.SpanStatusOk, tracer.endStatus)
	}
}

// Fix A: when the final chunk's send fails with the context still alive (a closed
// consumer channel), ProcessAndSendResponse must still complete the deferred span
// with its real (success) outcome rather than strand it.
func TestProcessAndSendResponse_CompletesSpanWhenFinalSendFails(t *testing.T) {
	tracer := &finalizerTestTracer{parked: true}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true) // final chunk

	// A closed channel makes GateSendChunk fail while the context is still alive.
	responseChan := make(chan *schemas.BifrostStreamChunk)
	close(responseChan)

	passthrough := func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return resp, err
	}

	ProcessAndSendResponse(ctx, passthrough, &schemas.BifrostResponse{}, responseChan, func(context.Context) {})

	if tracer.GetDeferredSpanHandle("trace-1") != nil {
		t.Error("deferred span must be completed even when the final chunk send fails")
	}
	if tracer.endStatus != schemas.SpanStatusOk {
		t.Errorf("successful stream whose delivery failed should end as %q, got %q", schemas.SpanStatusOk, tracer.endStatus)
	}
}

// TestMakeRequestWithContext_RequestTimeoutOverride pins the per-request
// timeout mechanism for dynamic providers: the override rides the request via
// fasthttp Request.SetTimeout inside MakeRequestWithContext — the shared
// fasthttp client's construction-time timeout is only a transport ceiling and
// is never touched. Because fasthttp enforces the deadline inside client.Do,
// the call (INCLUDING the wait() rendezvous) must return at the deadline —
// not when the slow upstream finally responds.
func TestMakeRequestWithContext_RequestTimeoutOverride(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer slow.Close()

	// Generous client timeout — stands in for the dynamic-provider transport
	// ceiling. The per-request override below must fire long before it.
	client := &fasthttp.Client{ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}

	newReq := func() (*fasthttp.Request, *fasthttp.Response) {
		req := fasthttp.AcquireRequest()
		req.SetRequestURI(slow.URL)
		req.Header.SetMethod(http.MethodGet)
		return req, fasthttp.AcquireResponse()
	}

	t.Run("OverrideShorterThanUpstreamLatencyYields504", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		timeoutSeconds := 1
		ctx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
			NetworkConfig: &schemas.ProviderNetworkConfigOverride{RequestTimeoutInSeconds: &timeoutSeconds},
		})
		req, resp := newReq()

		start := time.Now()
		_, bifrostErr, wait := MakeRequestWithContext(ctx, client, req, resp)
		// Elapsed includes wait() deliberately: SetTimeout makes client.Do
		// itself return at the deadline, so there is no abandoned background
		// goroutine for wait() to block on — the FULL caller-visible path
		// must be bounded by the override, not by the 2s upstream sleep.
		wait()
		elapsed := time.Since(start)
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		if bifrostErr == nil {
			t.Fatal("expected timeout error, got success")
		}
		if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 504 {
			t.Fatalf("status = %v, want 504 from per-request timeout override", bifrostErr.StatusCode)
		}
		if elapsed > 1900*time.Millisecond {
			t.Fatalf("request returned after %v; the 1s override deadline did not bound the call", elapsed)
		}
	})

	t.Run("OverrideLongerThanUpstreamLatencySucceeds", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		timeoutSeconds := 10
		ctx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
			NetworkConfig: &schemas.ProviderNetworkConfigOverride{RequestTimeoutInSeconds: &timeoutSeconds},
		})
		req, resp := newReq()

		_, bifrostErr, wait := MakeRequestWithContext(ctx, client, req, resp)
		wait()
		status := resp.StatusCode()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		if bifrostErr != nil {
			t.Fatalf("unexpected error with generous override: %+v", bifrostErr)
		}
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
	})

	t.Run("NoOverrideUsesClientTransportTimeout", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req, resp := newReq()

		_, bifrostErr, wait := MakeRequestWithContext(ctx, client, req, resp)
		wait()
		status := resp.StatusCode()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		if bifrostErr != nil {
			t.Fatalf("unexpected error without override: %+v", bifrostErr)
		}
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
	})
}

func TestEffectiveBetaHeaderOverridesFromContext(t *testing.T) {
	mkCtx := func(overrides map[string]bool) *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if overrides != nil {
			ctx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
				NetworkConfig: &schemas.ProviderNetworkConfigOverride{
					BetaHeaderOverrides: overrides,
				},
			})
		}
		return ctx
	}

	t.Run("no override returns base unchanged", func(t *testing.T) {
		base := map[string]bool{"tool-examples-2025-10-29": true}
		got := EffectiveBetaHeaderOverridesFromContext(mkCtx(nil), base)
		if len(got) != 1 || !got["tool-examples-2025-10-29"] {
			t.Fatalf("got %v, want base map unchanged", got)
		}
	})

	t.Run("nil base with override returns override keys", func(t *testing.T) {
		got := EffectiveBetaHeaderOverridesFromContext(mkCtx(map[string]bool{"redact-thinking-": true}), nil)
		if !got["redact-thinking-"] {
			t.Fatalf("got %v, want override key present", got)
		}
	})

	t.Run("override key wins over base", func(t *testing.T) {
		base := map[string]bool{"redact-thinking-": true}
		got := EffectiveBetaHeaderOverridesFromContext(mkCtx(map[string]bool{"redact-thinking-": false}), base)
		if got["redact-thinking-"] != false {
			t.Fatalf("override did not win: got true, want false")
		}
	})

	t.Run("base key absent from override is preserved", func(t *testing.T) {
		base := map[string]bool{"tool-examples-2025-10-29": true}
		got := EffectiveBetaHeaderOverridesFromContext(mkCtx(map[string]bool{"redact-thinking-": false}), base)
		if !got["tool-examples-2025-10-29"] {
			t.Fatalf("base-only key dropped: %v", got)
		}
		if got["redact-thinking-"] != false {
			t.Fatalf("override key missing: %v", got)
		}
	})

	t.Run("merge does not mutate base map", func(t *testing.T) {
		base := map[string]bool{"tool-examples-2025-10-29": true}
		EffectiveBetaHeaderOverridesFromContext(mkCtx(map[string]bool{"redact-thinking-": false}), base)
		if _, ok := base["redact-thinking-"]; ok {
			t.Fatal("EffectiveBetaHeaderOverridesFromContext mutated the caller's base map")
		}
	})
}
