package handlers

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestSendJSON_ValidData tests SendJSON with valid data
func TestSendJSON_ValidData(t *testing.T) {
	SetLogger(&mockLogger{})

	ctx := &fasthttp.RequestCtx{}
	data := map[string]string{"key": "value"}

	SendJSON(ctx, data)

	// Check content type
	if string(ctx.Response.Header.ContentType()) != "application/json" {
		t.Errorf("Expected content type 'application/json', got '%s'", string(ctx.Response.Header.ContentType()))
	}

	// Check response body
	var result map[string]string
	if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
	if result["key"] != "value" {
		t.Errorf("Expected key='value', got key='%s'", result["key"])
	}
}

// TestSendJSON_NilData tests SendJSON with nil data
func TestSendJSON_NilData(t *testing.T) {
	SetLogger(&mockLogger{})

	ctx := &fasthttp.RequestCtx{}
	SendJSON(ctx, nil)

	// Check content type
	if string(ctx.Response.Header.ContentType()) != "application/json" {
		t.Errorf("Expected content type 'application/json', got '%s'", string(ctx.Response.Header.ContentType()))
	}

	// Check response body is "null"
	body := string(ctx.Response.Body())
	if body != "null\n" {
		t.Errorf("Expected 'null\\n', got '%s'", body)
	}
}

// TestSendJSON_ComplexStruct tests SendJSON with a complex struct
func TestSendJSON_ComplexStruct(t *testing.T) {
	SetLogger(&mockLogger{})

	ctx := &fasthttp.RequestCtx{}
	data := struct {
		Name    string   `json:"name"`
		Count   int      `json:"count"`
		Tags    []string `json:"tags"`
		Enabled bool     `json:"enabled"`
	}{
		Name:    "test",
		Count:   42,
		Tags:    []string{"a", "b", "c"},
		Enabled: true,
	}

	SendJSON(ctx, data)

	// Check content type
	if string(ctx.Response.Header.ContentType()) != "application/json" {
		t.Errorf("Expected content type 'application/json', got '%s'", string(ctx.Response.Header.ContentType()))
	}

	// Verify JSON structure
	var result map[string]interface{}
	if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
	if result["name"] != "test" {
		t.Errorf("Expected name='test', got name='%v'", result["name"])
	}
	if result["count"].(float64) != 42 {
		t.Errorf("Expected count=42, got count=%v", result["count"])
	}
}

// TestSendJSONWithStatus_CustomStatusCode tests SendJSONWithStatus with various status codes
func TestSendJSONWithStatus_CustomStatusCode(t *testing.T) {
	SetLogger(&mockLogger{})

	testCases := []struct {
		name       string
		statusCode int
		data       interface{}
	}{
		{"OK", fasthttp.StatusOK, map[string]string{"status": "ok"}},
		{"Created", fasthttp.StatusCreated, map[string]string{"id": "123"}},
		{"Accepted", fasthttp.StatusAccepted, map[string]string{"message": "processing"}},
		{"NoContent", fasthttp.StatusNoContent, nil},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			SendJSONWithStatus(ctx, tc.data, tc.statusCode)

			if ctx.Response.StatusCode() != tc.statusCode {
				t.Errorf("Expected status code %d, got %d", tc.statusCode, ctx.Response.StatusCode())
			}
			if string(ctx.Response.Header.ContentType()) != "application/json" {
				t.Errorf("Expected content type 'application/json', got '%s'", string(ctx.Response.Header.ContentType()))
			}
		})
	}
}

// TestSendError_VariousStatusCodes tests SendError with various status codes
func TestSendError_VariousStatusCodes(t *testing.T) {
	SetLogger(&mockLogger{})

	testCases := []struct {
		name       string
		statusCode int
		message    string
	}{
		{"BadRequest", fasthttp.StatusBadRequest, "Invalid request"},
		{"Unauthorized", fasthttp.StatusUnauthorized, "Authentication required"},
		{"Forbidden", fasthttp.StatusForbidden, "Access denied"},
		{"NotFound", fasthttp.StatusNotFound, "Resource not found"},
		{"InternalServerError", fasthttp.StatusInternalServerError, "Internal error"},
		{"ServiceUnavailable", fasthttp.StatusServiceUnavailable, "Service unavailable"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			SendError(ctx, tc.statusCode, tc.message)

			if ctx.Response.StatusCode() != tc.statusCode {
				t.Errorf("Expected status code %d, got %d", tc.statusCode, ctx.Response.StatusCode())
			}

			var result schemas.BifrostError
			if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
				t.Fatalf("Failed to unmarshal response: %v", err)
			}
			if result.Error == nil || result.Error.Message != tc.message {
				t.Errorf("Expected error message '%s', got '%v'", tc.message, result.Error)
			}
		})
	}
}

// TestSendBifrostError_WithStatusCode tests SendBifrostError with explicit status code
func TestSendBifrostError_WithStatusCode(t *testing.T) {
	SetLogger(&mockLogger{})

	ctx := &fasthttp.RequestCtx{}
	statusCode := fasthttp.StatusBadRequest
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: "Test error",
		},
	}

	SendBifrostError(ctx, bifrostErr)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	}
}

// TestSendBifrostError_NilStatusCode_NotBifrostError tests SendBifrostError with nil status and IsBifrostError=false
func TestSendBifrostError_NilStatusCode_NotBifrostError(t *testing.T) {
	SetLogger(&mockLogger{})

	ctx := &fasthttp.RequestCtx{}
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     nil,
		Error: &schemas.ErrorField{
			Message: "Test error",
		},
	}

	SendBifrostError(ctx, bifrostErr)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Errorf("Expected status code %d (BadRequest), got %d", fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	}
}

// TestSendBifrostError_NilStatusCode_IsBifrostError tests SendBifrostError with nil status and IsBifrostError=true
func TestSendBifrostError_NilStatusCode_IsBifrostError(t *testing.T) {
	SetLogger(&mockLogger{})

	ctx := &fasthttp.RequestCtx{}
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     nil,
		Error: &schemas.ErrorField{
			Message: "Test error",
		},
	}

	SendBifrostError(ctx, bifrostErr)

	if ctx.Response.StatusCode() != fasthttp.StatusInternalServerError {
		t.Errorf("Expected status code %d (InternalServerError), got %d", fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
	}
}

// TestSendSSEError_ValidError tests SendSSEError with valid error
func TestSendSSEError_ValidError(t *testing.T) {
	SetLogger(&mockLogger{})

	ctx := &fasthttp.RequestCtx{}
	statusCode := fasthttp.StatusBadRequest
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: "SSE error test",
		},
	}

	SendSSEError(ctx, bifrostErr)

	body := string(ctx.Response.Body())
	if !contains(body, "data:") {
		t.Error("Expected SSE format with 'data:' prefix")
	}
	if !contains(body, "SSE error test") {
		t.Error("Expected error message in body")
	}
}

// TestIsOriginAllowed_LocalhostOrigins tests IsOriginAllowed with localhost origins
func TestIsOriginAllowed_LocalhostOrigins(t *testing.T) {
	localhostOrigins := []string{
		"http://localhost:3000",
		"https://localhost:3000",
		"http://127.0.0.1:8080",
		"http://0.0.0.0:5000",
		"https://127.0.0.1:3000",
		"http://localhost:80",
	}

	for _, origin := range localhostOrigins {
		t.Run(origin, func(t *testing.T) {
			if !IsOriginAllowed(origin, []string{}) {
				t.Errorf("Expected localhost origin '%s' to be allowed", origin)
			}
		})
	}
}

// TestIsOriginAllowed_ExactMatch tests IsOriginAllowed with exact match
func TestIsOriginAllowed_ExactMatch(t *testing.T) {
	allowedOrigins := []string{"https://example.com", "https://api.example.com"}

	testCases := []struct {
		origin  string
		allowed bool
	}{
		{"https://example.com", true},
		{"https://api.example.com", true},
		{"https://other.com", false},
		{"http://example.com", false}, // different protocol
	}

	for _, tc := range testCases {
		t.Run(tc.origin, func(t *testing.T) {
			result := IsOriginAllowed(tc.origin, allowedOrigins)
			if result != tc.allowed {
				t.Errorf("Expected IsOriginAllowed('%s') = %v, got %v", tc.origin, tc.allowed, result)
			}
		})
	}
}

// TestIsOriginAllowed_WildcardAll tests IsOriginAllowed with "*" wildcard
func TestIsOriginAllowed_WildcardAll(t *testing.T) {
	allowedOrigins := []string{"*"}

	testOrigins := []string{
		"https://example.com",
		"https://any-site.org",
		"http://random.net:8080",
	}

	for _, origin := range testOrigins {
		t.Run(origin, func(t *testing.T) {
			if !IsOriginAllowed(origin, allowedOrigins) {
				t.Errorf("Expected origin '%s' to be allowed with '*' wildcard", origin)
			}
		})
	}
}

// TestIsOriginAllowed_WildcardSubdomain tests IsOriginAllowed with subdomain wildcard
func TestIsOriginAllowed_WildcardSubdomain(t *testing.T) {
	allowedOrigins := []string{"https://*.example.com"}

	testCases := []struct {
		origin  string
		allowed bool
	}{
		{"https://api.example.com", true},
		{"https://www.example.com", true},
		{"https://sub.example.com", true},
		{"https://example.com", false},              // no subdomain
		{"https://sub.sub.example.com", false},      // nested subdomain (doesn't match)
		{"https://api.other.com", false},            // different domain
		{"http://api.example.com", false},           // different protocol
	}

	for _, tc := range testCases {
		t.Run(tc.origin, func(t *testing.T) {
			result := IsOriginAllowed(tc.origin, allowedOrigins)
			if result != tc.allowed {
				t.Errorf("Expected IsOriginAllowed('%s') = %v, got %v", tc.origin, tc.allowed, result)
			}
		})
	}
}

// TestIsOriginAllowed_EmptyOrigin tests IsOriginAllowed with empty origin
func TestIsOriginAllowed_EmptyOrigin(t *testing.T) {
	if IsOriginAllowed("", []string{"https://example.com"}) {
		t.Error("Expected empty origin to not be allowed")
	}
}

// TestIsOriginAllowed_EmptyAllowedOrigins tests IsOriginAllowed with empty allowed list
func TestIsOriginAllowed_EmptyAllowedOrigins(t *testing.T) {
	// Non-localhost should not be allowed
	if IsOriginAllowed("https://example.com", []string{}) {
		t.Error("Expected non-localhost origin to not be allowed with empty allowedOrigins")
	}

	// Localhost should still be allowed
	if !IsOriginAllowed("http://localhost:3000", []string{}) {
		t.Error("Expected localhost to be allowed even with empty allowedOrigins")
	}
}

// TestIsLocalhostOrigin tests the isLocalhostOrigin helper function
func TestIsLocalhostOrigin(t *testing.T) {
	testCases := []struct {
		origin      string
		isLocalhost bool
	}{
		{"http://localhost:3000", true},
		{"https://localhost:3000", true},
		{"http://127.0.0.1:8080", true},
		{"http://0.0.0.0:5000", true},
		{"https://127.0.0.1:3000", true},
		{"https://example.com", false},
		{"http://192.168.1.1:3000", false},
		{"", false},
	}

	for _, tc := range testCases {
		t.Run(tc.origin, func(t *testing.T) {
			result := isLocalhostOrigin(tc.origin)
			if result != tc.isLocalhost {
				t.Errorf("Expected isLocalhostOrigin('%s') = %v, got %v", tc.origin, tc.isLocalhost, result)
			}
		})
	}
}

// TestMatchesWildcardPattern tests the matchesWildcardPattern helper function
func TestMatchesWildcardPattern(t *testing.T) {
	testCases := []struct {
		origin  string
		pattern string
		matches bool
	}{
		{"https://api.example.com", "https://*.example.com", true},
		{"https://www.example.com", "https://*.example.com", true},
		{"https://example.com", "https://*.example.com", false},
		{"https://api.other.com", "https://*.example.com", false},
		{"http://api.example.com", "http://*.example.com", true},
		{"https://sub.api.example.com", "https://*.example.com", false}, // nested subdomain
		{"https://api.example.com:8080", "https://*.example.com:8080", true},
	}

	for _, tc := range testCases {
		t.Run(tc.origin+"_"+tc.pattern, func(t *testing.T) {
			result := matchesWildcardPattern(tc.origin, tc.pattern)
			if result != tc.matches {
				t.Errorf("Expected matchesWildcardPattern('%s', '%s') = %v, got %v", tc.origin, tc.pattern, tc.matches, result)
			}
		})
	}
}

// TestParseModel_ValidFormats tests ParseModel with valid model formats
func TestParseModel_ValidFormats(t *testing.T) {
	testCases := []struct {
		input            string
		expectedProvider string
		expectedModel    string
	}{
		{"openai/gpt-4", "openai", "gpt-4"},
		{"anthropic/claude-3-opus", "anthropic", "claude-3-opus"},
		{"google/gemini-pro", "google", "gemini-pro"},
		{"azure/gpt-4/deployment", "azure", "gpt-4/deployment"},
		{"provider/nested/path/model", "provider", "nested/path/model"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			provider, model, err := ParseModel(tc.input)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if provider != tc.expectedProvider {
				t.Errorf("Expected provider='%s', got '%s'", tc.expectedProvider, provider)
			}
			if model != tc.expectedModel {
				t.Errorf("Expected model='%s', got '%s'", tc.expectedModel, model)
			}
		})
	}
}

// TestParseModel_InvalidFormats tests ParseModel with invalid model formats
func TestParseModel_InvalidFormats(t *testing.T) {
	testCases := []struct {
		input string
		error string
	}{
		{"", "model cannot be empty"},
		{"   ", "model cannot be empty"},
		{"noSlash", "model must be in the format 'provider/model'"},
		{"/model", "model must be in the format 'provider/model' with non-empty provider and model"},
		{"provider/", "model must be in the format 'provider/model' with non-empty provider and model"},
		{"/", "model must be in the format 'provider/model' with non-empty provider and model"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			_, _, err := ParseModel(tc.input)
			if err == nil {
				t.Fatal("Expected error, got nil")
			}
			if err.Error() != tc.error {
				t.Errorf("Expected error '%s', got '%s'", tc.error, err.Error())
			}
		})
	}
}

// TestParseModel_WhitespaceHandling tests ParseModel handles whitespace correctly
func TestParseModel_WhitespaceHandling(t *testing.T) {
	testCases := []struct {
		input            string
		expectedProvider string
		expectedModel    string
	}{
		{"  openai/gpt-4  ", "openai", "gpt-4"},
		{"openai / gpt-4", "openai", "gpt-4"},
		{"\topenai/gpt-4\n", "openai", "gpt-4"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			provider, model, err := ParseModel(tc.input)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if provider != tc.expectedProvider {
				t.Errorf("Expected provider='%s', got '%s'", tc.expectedProvider, provider)
			}
			if model != tc.expectedModel {
				t.Errorf("Expected model='%s', got '%s'", tc.expectedModel, model)
			}
		})
	}
}

// TestFuzzyMatch_BasicMatching tests fuzzyMatch with basic cases
func TestFuzzyMatch_BasicMatching(t *testing.T) {
	testCases := []struct {
		text    string
		query   string
		matches bool
	}{
		{"gpt-4", "gpt4", true},
		{"gpt-4-turbo", "gpt4", true},
		{"gpt-4-turbo", "gpt4turbo", true},
		{"claude-3-opus", "claude", true},
		{"claude-3-opus", "c3o", true},
		{"claude-3-opus", "opus", true},
		{"openai", "oi", true},
		{"anthropic", "xyz", false},
		{"gpt-4", "gpt5", false},
	}

	for _, tc := range testCases {
		t.Run(tc.text+"_"+tc.query, func(t *testing.T) {
			result := fuzzyMatch(tc.text, tc.query)
			if result != tc.matches {
				t.Errorf("Expected fuzzyMatch('%s', '%s') = %v, got %v", tc.text, tc.query, tc.matches, result)
			}
		})
	}
}

// TestFuzzyMatch_EmptyQuery tests fuzzyMatch with empty query
func TestFuzzyMatch_EmptyQuery(t *testing.T) {
	if !fuzzyMatch("any-text", "") {
		t.Error("Expected empty query to match any text")
	}
}

// TestFuzzyMatch_CaseInsensitive tests fuzzyMatch is case insensitive
func TestFuzzyMatch_CaseInsensitive(t *testing.T) {
	testCases := []struct {
		text    string
		query   string
		matches bool
	}{
		{"GPT-4", "gpt4", true},
		{"gpt-4", "GPT4", true},
		{"Claude-3-Opus", "CLAUDE", true},
		{"ANTHROPIC", "anthropic", true},
	}

	for _, tc := range testCases {
		t.Run(tc.text+"_"+tc.query, func(t *testing.T) {
			result := fuzzyMatch(tc.text, tc.query)
			if result != tc.matches {
				t.Errorf("Expected fuzzyMatch('%s', '%s') = %v, got %v", tc.text, tc.query, tc.matches, result)
			}
		})
	}
}

// TestFuzzyMatch_SpecialCharacters tests fuzzyMatch with special characters
func TestFuzzyMatch_SpecialCharacters(t *testing.T) {
	testCases := []struct {
		text    string
		query   string
		matches bool
	}{
		{"model-v1.0", "v1", true},
		{"model_v2_beta", "v2beta", true},
		{"model@test", "test", true},
	}

	for _, tc := range testCases {
		t.Run(tc.text+"_"+tc.query, func(t *testing.T) {
			result := fuzzyMatch(tc.text, tc.query)
			if result != tc.matches {
				t.Errorf("Expected fuzzyMatch('%s', '%s') = %v, got %v", tc.text, tc.query, tc.matches, result)
			}
		})
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
