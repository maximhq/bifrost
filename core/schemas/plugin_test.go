package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCaseInsensitiveLookup(t *testing.T) {
	tests := []struct {
		name     string
		data     map[string]string
		key      string
		expected string
	}{
		{
			name:     "nil map returns empty string",
			data:     nil,
			key:      "Content-Type",
			expected: "",
		},
		{
			name:     "empty key returns empty string",
			data:     map[string]string{"Content-Type": "application/json"},
			key:      "",
			expected: "",
		},
		{
			name:     "key not found returns empty string",
			data:     map[string]string{"Content-Type": "application/json"},
			key:      "Authorization",
			expected: "",
		},
		{
			name:     "exact match",
			data:     map[string]string{"Content-Type": "application/json"},
			key:      "Content-Type",
			expected: "application/json",
		},
		{
			name:     "lowercase key match - map has lowercase key",
			data:     map[string]string{"content-type": "application/json"},
			key:      "Content-Type",
			expected: "application/json",
		},
		{
			name:     "lowercase key match - query is lowercase",
			data:     map[string]string{"content-type": "application/json"},
			key:      "content-type",
			expected: "application/json",
		},
		{
			name:     "case-insensitive iteration - map has mixed case",
			data:     map[string]string{"Content-Type": "application/json"},
			key:      "content-type",
			expected: "application/json",
		},
		{
			name:     "case-insensitive iteration - uppercase query",
			data:     map[string]string{"Content-Type": "application/json"},
			key:      "CONTENT-TYPE",
			expected: "application/json",
		},
		{
			name:     "multiple keys - finds correct one",
			data:     map[string]string{"Accept": "text/html", "Content-Type": "application/json"},
			key:      "content-type",
			expected: "application/json",
		},
		// x-bf-vk header variations
		{
			name:     "x-bf-vk exact match lowercase",
			data:     map[string]string{"x-bf-vk": "sk-bf-test123"},
			key:      "x-bf-vk",
			expected: "sk-bf-test123",
		},
		{
			name:     "x-bf-vk mixed case in map",
			data:     map[string]string{"X-Bf-Vk": "sk-bf-test123"},
			key:      "x-bf-vk",
			expected: "sk-bf-test123",
		},
		{
			name:     "x-bf-vk uppercase in map",
			data:     map[string]string{"X-BF-VK": "sk-bf-test123"},
			key:      "x-bf-vk",
			expected: "sk-bf-test123",
		},
		// authorization header variations
		{
			name:     "authorization exact match lowercase",
			data:     map[string]string{"authorization": "Bearer sk-bf-test123"},
			key:      "authorization",
			expected: "Bearer sk-bf-test123",
		},
		{
			name:     "authorization capitalized in map",
			data:     map[string]string{"Authorization": "Bearer sk-bf-test123"},
			key:      "authorization",
			expected: "Bearer sk-bf-test123",
		},
		{
			name:     "authorization uppercase in map",
			data:     map[string]string{"AUTHORIZATION": "Bearer sk-bf-test123"},
			key:      "authorization",
			expected: "Bearer sk-bf-test123",
		},
		// x-api-key header variations
		{
			name:     "x-api-key exact match lowercase",
			data:     map[string]string{"x-api-key": "sk-bf-apikey123"},
			key:      "x-api-key",
			expected: "sk-bf-apikey123",
		},
		{
			name:     "x-api-key mixed case in map",
			data:     map[string]string{"X-Api-Key": "sk-bf-apikey123"},
			key:      "x-api-key",
			expected: "sk-bf-apikey123",
		},
		{
			name:     "x-api-key uppercase in map",
			data:     map[string]string{"X-API-KEY": "sk-bf-apikey123"},
			key:      "x-api-key",
			expected: "sk-bf-apikey123",
		},
		// x-goog-api-key header variations
		{
			name:     "x-goog-api-key exact match lowercase",
			data:     map[string]string{"x-goog-api-key": "sk-bf-google123"},
			key:      "x-goog-api-key",
			expected: "sk-bf-google123",
		},
		{
			name:     "x-goog-api-key mixed case in map",
			data:     map[string]string{"X-Goog-Api-Key": "sk-bf-google123"},
			key:      "x-goog-api-key",
			expected: "sk-bf-google123",
		},
		{
			name:     "x-goog-api-key uppercase in map",
			data:     map[string]string{"X-GOOG-API-KEY": "sk-bf-google123"},
			key:      "x-goog-api-key",
			expected: "sk-bf-google123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := caseInsensitiveLookup(tt.data, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHTTPRequest_CaseInsensitiveHeaderLookup(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		key      string
		expected string
	}{
		{
			name:     "exact match",
			headers:  map[string]string{"Content-Type": "application/json"},
			key:      "Content-Type",
			expected: "application/json",
		},
		{
			name:     "case-insensitive match",
			headers:  map[string]string{"Content-Type": "application/json"},
			key:      "content-type",
			expected: "application/json",
		},
		{
			name:     "authorization header",
			headers:  map[string]string{"Authorization": "Bearer token123"},
			key:      "authorization",
			expected: "Bearer token123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &HTTPRequest{Headers: tt.headers}
			result := req.CaseInsensitiveHeaderLookup(tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHTTPRequest_CaseInsensitiveQueryLookup(t *testing.T) {
	tests := []struct {
		name     string
		query    map[string]string
		key      string
		expected string
	}{
		{
			name:     "exact match",
			query:    map[string]string{"apiKey": "test123"},
			key:      "apiKey",
			expected: "test123",
		},
		{
			name:     "case-insensitive match",
			query:    map[string]string{"ApiKey": "test123"},
			key:      "apikey",
			expected: "test123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &HTTPRequest{Query: tt.query}
			result := req.CaseInsensitiveQueryLookup(tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}
