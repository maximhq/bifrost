package utils

import (
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestIsHTMLResponse(t *testing.T) {
	tests := []struct {
		name           string
		contentType    string
		body           []byte
		expectedIsHTML bool
		description    string
	}{
		{
			name:           "HTML with Content-Type header",
			contentType:    "text/html; charset=utf-8",
			body:           []byte("<html><body>Error</body></html>"),
			expectedIsHTML: true,
			description:    "Should detect HTML from Content-Type header",
		},
		{
			name:           "HTML without Content-Type",
			contentType:    "application/octet-stream",
			body:           []byte("<!DOCTYPE html><html><head><title>Error 500</title></head></html>"),
			expectedIsHTML: true,
			description:    "Should detect HTML from DOCTYPE",
		},
		{
			name:           "HTML with h1 tag",
			contentType:    "application/octet-stream",
			body:           []byte("<h1>Service Unavailable</h1>"),
			expectedIsHTML: true,
			description:    "Should detect HTML from h1 tag",
		},
		{
			name:           "JSON response",
			contentType:    "application/json",
			body:           []byte(`{"error": "invalid request"}`),
			expectedIsHTML: false,
			description:    "Should not detect JSON as HTML",
		},
		{
			name:           "Plain text response",
			contentType:    "text/plain",
			body:           []byte("Invalid request"),
			expectedIsHTML: false,
			description:    "Should not detect plain text as HTML",
		},
		{
			name:           "Empty body",
			contentType:    "text/html",
			body:           []byte(""),
			expectedIsHTML: true,
			description:    "Should detect HTML from Content-Type even with empty body",
		},
		{
			name:           "Very short body",
			contentType:    "application/json",
			body:           []byte("abc"),
			expectedIsHTML: false,
			description:    "Should not detect very short body as HTML",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &fasthttp.Response{}
			resp.Header.Set("Content-Type", tt.contentType)

			result := IsHTMLResponse(resp, tt.body)
			if result != tt.expectedIsHTML {
				t.Errorf("isHTMLResponse() = %v, want %v. %s", result, tt.expectedIsHTML, tt.description)
			}
		})
	}
}

func TestExtractHTMLErrorMessage(t *testing.T) {
	tests := []struct {
		name        string
		htmlBody    []byte
		expectMsg   string
		description string
	}{
		{
			name: "Extract from title tag",
			htmlBody: []byte(`
				<!DOCTYPE html>
				<html>
				<head><title>404 Not Found</title></head>
				<body><p>The page was not found</p></body>
				</html>
			`),
			expectMsg:   "404 Not Found",
			description: "Should extract title from title tag",
		},
		{
			name: "Extract from h1 tag",
			htmlBody: []byte(`
				<!DOCTYPE html>
				<html>
				<body>
				<h1>Service Unavailable</h1>
				<p>The service is currently unavailable</p>
				</body>
				</html>
			`),
			expectMsg:   "Service Unavailable",
			description: "Should extract from h1 tag when title is missing",
		},
		{
			name: "Extract from h2 tag",
			htmlBody: []byte(`
				<!DOCTYPE html>
				<html>
				<body>
				<h2 class="error-header">Authentication Failed</h2>
				<p>Please check your credentials</p>
				</body>
				</html>
			`),
			expectMsg:   "Authentication Failed",
			description: "Should extract from h2 tag with attributes",
		},
		{
			name: "Extract visible text when no headers",
			htmlBody: []byte(`
				<html>
				<body>
				<div>There was an error processing your request. Please try again later.</div>
				</body>
				</html>
			`),
			expectMsg:   "There was an error processing your request. Please try again later.",
			description: "Should extract visible text from div when no headers found",
		},
		{
			name: "Ignore script and style tags",
			htmlBody: []byte(`
				<html>
				<head><title>Error</title></head>
				<body>
				<script>var x = 'ignore me';</script>
				<style>.error { color: red; }</style>
				<h1>Actual Error Message</h1>
				</body>
				</html>
			`),
			expectMsg:   "Actual Error Message",
			description: "Should ignore script and style content",
		},
		{
			name: "Extract from first valid h1",
			htmlBody: []byte(`
				<html>
				<body>
				<h1></h1>
				<h1>Second header with actual content</h1>
				</body>
				</html>
			`),
			expectMsg:   "Second header with actual content",
			description: "Should extract from first non-empty header",
		},
		{
			name: "Handle meta description",
			htmlBody: []byte(`
				<html>
				<head>
				<meta name="description" content="Rate limit exceeded. Please wait 60 seconds.">
				</head>
				<body></body>
				</html>
			`),
			expectMsg:   "Rate limit exceeded. Please wait 60 seconds.",
			description: "Should extract from meta description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractHTMLErrorMessage(tt.htmlBody)
			if result != tt.expectMsg {
				t.Errorf("extractHTMLErrorMessage() = %q, want %q. %s", result, tt.expectMsg, tt.description)
			}
		})
	}
}

func TestHandleProviderAPIErrorWithHTML(t *testing.T) {
	tests := []struct {
		name              string
		statusCode        int
		contentType       string
		body              []byte
		description       string
		expectedInMessage string
	}{
		{
			name:        "HTML 500 error - lazy detection",
			statusCode:  500,
			contentType: "text/html; charset=utf-8",
			body: []byte(`
				<!DOCTYPE html>
				<html>
				<head><title>Internal Server Error</title></head>
				<body><h1>Something went wrong</h1></body>
				</html>
			`),
			description:       "Should detect and handle HTML only after JSON parse fails",
			expectedInMessage: "HTML response received from provider",
		},
		{
			name:        "HTML 403 error - lazy detection",
			statusCode:  403,
			contentType: "text/html",
			body: []byte(`
				<html>
				<body>
				<h1>Forbidden</h1>
				<p>Access denied</p>
				</body>
				</html>
			`),
			description:       "Should detect HTML on parse failure",
			expectedInMessage: "HTML response received from provider",
		},
		{
			name:              "Invalid JSON with HTML fallback",
			statusCode:        400,
			contentType:       "application/json",
			body:              []byte(`not valid json`),
			description:       "Should fall back to raw string when not HTML",
			expectedInMessage: "provider API error",
		},
		{
			name:              "Valid JSON error response",
			statusCode:        400,
			contentType:       "application/json",
			body:              []byte(`{"error": {"message": "Invalid request"}, "code": "invalid_request"}`),
			description:       "Should handle valid JSON without HTML detection",
			expectedInMessage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &fasthttp.Response{}
			resp.SetStatusCode(tt.statusCode)
			resp.Header.Set("Content-Type", tt.contentType)
			resp.SetBody(tt.body)

			var errorResp map[string]interface{}
			bifrostErr := HandleProviderAPIError(resp, &errorResp)

			if bifrostErr == nil {
				t.Errorf("HandleProviderAPIError() returned nil error")
				return
			}

			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != tt.statusCode {
				t.Errorf("HandleProviderAPIError() status code = %v, want %v", bifrostErr.StatusCode, tt.statusCode)
			}

			if bifrostErr.Error == nil {
				t.Errorf("HandleProviderAPIError() error field is nil")
				return
			}

			// Check if expected message is in the response
			if tt.expectedInMessage != "" && !strings.Contains(bifrostErr.Error.Message, tt.expectedInMessage) {
				t.Errorf("Expected message to contain %q, got %q", tt.expectedInMessage, bifrostErr.Error.Message)
			}

			t.Logf("Handled %s: status=%d, message=%q", tt.name, *bifrostErr.StatusCode, bifrostErr.Error.Message)
		})
	}
}

func BenchmarkIsHTMLResponse(b *testing.B) {
	resp := &fasthttp.Response{}
	resp.Header.Set("Content-Type", "text/html; charset=utf-8")
	body := []byte(`<!DOCTYPE html><html><head><title>Error</title></head><body><h1>Test Error</h1></body></html>`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IsHTMLResponse(resp, body)
	}
}

func BenchmarkExtractHTMLErrorMessage(b *testing.B) {
	body := []byte(`<!DOCTYPE html><html><head><title>Internal Server Error</title></head><body><h1>Something went wrong</h1><p>This is a detailed error message</p></body></html>`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ExtractHTMLErrorMessage(body)
	}
}
