package main

import (
	"maps"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestHTTPTransportPreAuthHook(t *testing.T) {
	if err := Init(map[string]any{"virtual_key": "sk-bf-from-config"}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	tests := []struct {
		name        string
		headers     map[string]string
		wantHeaders map[string]string
	}{
		{
			name:        "adds configured virtual key when header is absent",
			headers:     map[string]string{"content-type": "application/json"},
			wantHeaders: map[string]string{"content-type": "application/json", "x-bf-vk": "sk-bf-from-config"},
		},
		{
			name:        "preserves existing virtual key",
			headers:     map[string]string{"x-bf-vk": "sk-bf-from-request"},
			wantHeaders: map[string]string{"x-bf-vk": "sk-bf-from-request"},
		},
		{
			name:        "detects existing header case insensitively",
			headers:     map[string]string{"X-Bf-Vk": "sk-bf-from-request"},
			wantHeaders: map[string]string{"X-Bf-Vk": "sk-bf-from-request"},
		},
		{
			name:        "does not replace an existing empty header",
			headers:     map[string]string{"x-bf-vk": ""},
			wantHeaders: map[string]string{"x-bf-vk": ""},
		},
		{
			name:        "initializes a nil header map",
			wantHeaders: map[string]string{"x-bf-vk": "sk-bf-from-config"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &schemas.HTTPRequest{Headers: cloneHeaders(tt.headers)}

			resp, err := HTTPTransportPreAuthHook(nil, req)
			if err != nil {
				t.Fatalf("HTTPTransportPreAuthHook() error = %v", err)
			}
			if resp != nil {
				t.Fatalf("HTTPTransportPreAuthHook() response = %#v, want nil", resp)
			}
			if !maps.Equal(req.Headers, tt.wantHeaders) {
				t.Fatalf("HTTPTransportPreAuthHook() headers = %#v, want %#v", req.Headers, tt.wantHeaders)
			}
		})
	}
}

func TestInitRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config any
	}{
		{name: "nil config", config: nil},
		{name: "non-object config", config: "sk-bf-test"},
		{name: "missing virtual key", config: map[string]any{}},
		{name: "non-string virtual key", config: map[string]any{"virtual_key": 123}},
		{name: "empty virtual key", config: map[string]any{"virtual_key": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Init(tt.config); err == nil {
				t.Fatal("Init() error = nil, want configuration error")
			}
		})
	}
}

func cloneHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	for key, value := range headers {
		cloned[key] = value
	}
	return cloned
}
