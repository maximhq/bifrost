package utils

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestDrainNonSSEStreamResponse_SSEDoesNotDrain(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	body := []byte("data: hello\n\n")
	resp.Header.SetContentType("text/event-stream")
	resp.SetBodyStream(bytes.NewReader(body), len(body))

	drained := DrainNonSSEStreamResponse(resp)
	if drained {
		t.Fatal("expected SSE response to remain readable")
	}

	remaining, err := io.ReadAll(resp.BodyStream())
	if err != nil {
		t.Fatalf("failed to read SSE body after guard: %v", err)
	}
	if string(remaining) != string(body) {
		t.Fatalf("expected SSE body to remain intact, got %q", string(remaining))
	}
}

func TestDrainNonSSEStreamResponse_NonSSEDrains(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	body := []byte(`{"error":"not stream"}`)
	resp.Header.SetContentType("application/json")
	resp.SetBodyStream(bytes.NewReader(body), len(body))

	drained := DrainNonSSEStreamResponse(resp)
	if !drained {
		t.Fatal("expected non-SSE response to be drained")
	}

	remaining, err := io.ReadAll(resp.BodyStream())
	if err != nil {
		t.Fatalf("failed to read body after drain: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected drained body to be empty, got %q", string(remaining))
	}
}

func TestDrainNonSSEStreamResponse_GzipSSEStillReadable(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	body := []byte("data: hello\n\ndata: [DONE]\n\n")
	compressed := gzipCompress(body)
	resp.Header.SetContentType("text/event-stream")
	resp.Header.Set("Content-Encoding", "gzip")
	resp.SetBodyStream(bytes.NewReader(compressed), len(compressed))

	drained := DrainNonSSEStreamResponse(resp)
	if drained {
		t.Fatal("expected gzip SSE response to remain readable")
	}

	reader, releaseGzip := DecompressStreamBody(resp)
	defer releaseGzip()

	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read decompressed SSE body: %v", err)
	}
	if string(remaining) != string(body) {
		t.Fatalf("expected decompressed SSE body %q, got %q", string(body), string(remaining))
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

// TestProcessAndSendResponse_StoreRawLoggingOnly_StripsRawDataFromResponseChunk verifies
// that when BifrostContextKeyRawRequestResponseForLogging is set, ProcessAndSendResponse
// strips RawRequest and RawResponse from the outgoing stream chunk, while leaving other
// ExtraFields intact. It also verifies that the original BifrostResponse is not mutated
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
				ctx.SetValue(schemas.BifrostContextKeyRawRequestResponseForLogging, true)
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
			ProcessAndSendResponse(ctx, passThrough, response, responseChan)

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
// that when BifrostContextKeyRawRequestResponseForLogging is set, raw data is stripped
// from BifrostError payloads embedded in stream chunks, without mutating the shared
// BifrostError object (shared object safety for PostLLMHook goroutines).
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
				ctx.SetValue(schemas.BifrostContextKeyRawRequestResponseForLogging, true)
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
			}, responseChan)

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
//   - BifrostContextKeySendBackRawRequest=true in context: always returns true
//   - Logging-only mode: requestWorker sets BifrostContextKeySendBackRawRequest=true,
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
			// requestWorker sets BifrostContextKeySendBackRawRequest=true in logging-only
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
				ctx.SetValue(schemas.BifrostContextKeySendBackRawRequest, true)
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
			// requestWorker sets BifrostContextKeySendBackRawResponse=true in logging-only
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
				ctx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
			}

			got := ShouldSendBackRawResponse(ctx, tt.providerDefault)
			if got != tt.want {
				t.Errorf("ShouldSendBackRawResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}
