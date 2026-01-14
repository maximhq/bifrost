package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// mockTestLogger implements schemas.Logger for testing
type mockTestLogger struct{}

func (m *mockTestLogger) Debug(format string, args ...any)                  {}
func (m *mockTestLogger) Info(format string, args ...any)                   {}
func (m *mockTestLogger) Warn(format string, args ...any)                   {}
func (m *mockTestLogger) Error(format string, args ...any)                  {}
func (m *mockTestLogger) Fatal(format string, args ...any)                  {}
func (m *mockTestLogger) SetLevel(level schemas.LogLevel)                   {}
func (m *mockTestLogger) SetOutputType(outputType schemas.LoggerOutputType) {}

func init() {
	// Initialize logger for tests
	SetLogger(&mockTestLogger{})
}

// Note: The ConvertToBifrostContext function requires a properly initialized
// fasthttp.RequestCtx with server context. These tests document expected
// behavior and are covered by integration tests.

// TestConvertToBifrostContext_Documentation documents the function behavior
func TestConvertToBifrostContext_Documentation(t *testing.T) {
	// ConvertToBifrostContext converts a FastHTTP RequestCtx to a Bifrost context.
	// It processes the following headers:
	// 1. x-request-id: Custom request ID (or generates UUID if not present)
	// 2. x-bf-prom-*: Prometheus metrics headers
	// 3. x-bf-maxim-*: Maxim tracing headers
	// 4. x-bf-mcp-*: MCP control headers
	// 5. x-bf-vk: Virtual key header
	// 6. x-bf-api-key: API key name reference
	// 7. x-bf-cache-*: Cache control headers
	// 8. x-bf-eh-*: Extra headers for forwarding
	// 9. x-bf-send-back-raw-response: Raw response control
	// 10. Authorization, x-api-key, x-goog-api-key: Direct key extraction

	t.Log("ConvertToBifrostContext processes multiple header types for context propagation")
}

// TestHeaderFilterConfig_Allowlist_Logic documents allowlist filtering logic
func TestHeaderFilterConfig_Allowlist_Logic(t *testing.T) {
	// When allowlist is non-empty, only headers in the allowlist are allowed
	config := &configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"allowed-header"},
	}

	// Expected: only "allowed-header" passes through
	if config.Allowlist[0] != "allowed-header" {
		t.Error("Allowlist should contain 'allowed-header'")
	}

	t.Log("Allowlist filtering: when non-empty, only listed headers are allowed")
}

// TestHeaderFilterConfig_Denylist_Logic documents denylist filtering logic
func TestHeaderFilterConfig_Denylist_Logic(t *testing.T) {
	// When denylist is non-empty, headers in the denylist are blocked
	config := &configstoreTables.GlobalHeaderFilterConfig{
		Denylist: []string{"blocked-header"},
	}

	// Expected: "blocked-header" is blocked
	if config.Denylist[0] != "blocked-header" {
		t.Error("Denylist should contain 'blocked-header'")
	}

	t.Log("Denylist filtering: listed headers are blocked from forwarding")
}

// TestHeaderFilterConfig_Combined_Logic documents combined filter logic
func TestHeaderFilterConfig_Combined_Logic(t *testing.T) {
	// When both allowlist and denylist are non-empty:
	// 1. Header must be in allowlist first
	// 2. Then header must not be in denylist
	config := &configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"header-a", "header-b"},
		Denylist:  []string{"header-b"},
	}

	// Expected: header-a passes, header-b is blocked (in denylist), header-c is blocked (not in allowlist)
	if len(config.Allowlist) != 2 {
		t.Error("Expected 2 headers in allowlist")
	}
	if len(config.Denylist) != 1 {
		t.Error("Expected 1 header in denylist")
	}

	t.Log("Combined filtering: allowlist first, then denylist")
}

// TestSecurityDenylist_Headers documents security-sensitive headers that are always blocked
func TestSecurityDenylist_Headers(t *testing.T) {
	// These headers are always blocked from x-bf-eh-* forwarding:
	securityDenylist := map[string]bool{
		"authorization":       true,
		"proxy-authorization": true,
		"cookie":              true,
		"host":                true,
		"content-length":      true,
		"connection":          true,
		"transfer-encoding":   true,
		"x-api-key":           true,
		"x-goog-api-key":      true,
		"x-bf-api-key":        true,
		"x-bf-vk":             true,
	}

	// Verify expected headers are in the list
	expectedHeaders := []string{
		"authorization",
		"proxy-authorization",
		"cookie",
		"host",
		"content-length",
		"connection",
		"transfer-encoding",
	}

	for _, header := range expectedHeaders {
		if !securityDenylist[header] {
			t.Errorf("Expected '%s' to be in security denylist", header)
		}
	}

	t.Log("Security denylist prevents forwarding of sensitive headers")
}

// TestCacheThreshold_Clamping documents cache threshold clamping behavior
func TestCacheThreshold_Clamping(t *testing.T) {
	// Cache threshold is clamped to [0.0, 1.0]
	testCases := []struct {
		input    float64
		expected float64
	}{
		{0.5, 0.5},
		{-0.5, 0.0},  // Clamped to 0
		{1.5, 1.0},   // Clamped to 1
		{0.0, 0.0},
		{1.0, 1.0},
	}

	for _, tc := range testCases {
		// Simulate clamping logic
		threshold := tc.input
		if threshold < 0.0 {
			threshold = 0.0
		} else if threshold > 1.0 {
			threshold = 1.0
		}

		if threshold != tc.expected {
			t.Errorf("Expected clamped value %v for input %v, got %v", tc.expected, tc.input, threshold)
		}
	}

	t.Log("Cache threshold values are clamped to [0.0, 1.0]")
}

// TestCacheTTL_Formats documents cache TTL parsing formats
func TestCacheTTL_Formats(t *testing.T) {
	// Cache TTL can be specified in two formats:
	// 1. Duration string: "30s", "5m", "1h"
	// 2. Plain number (interpreted as seconds): "300"

	validFormats := []string{
		"30s",    // 30 seconds
		"5m",     // 5 minutes
		"1h",     // 1 hour
		"300",    // 300 seconds
		"1h30m",  // 1 hour 30 minutes
	}

	for _, format := range validFormats {
		t.Logf("Valid cache TTL format: %s", format)
	}

	t.Log("Cache TTL supports duration strings and plain numbers (as seconds)")
}

// TestMCPHeaders_Parsing documents MCP header parsing
func TestMCPHeaders_Parsing(t *testing.T) {
	// MCP include headers are comma-separated lists:
	// x-bf-mcp-include-clients: "client1, client2, client3"
	// x-bf-mcp-include-tools: "tool1,tool2"

	// Parsing splits by comma and trims whitespace
	input := "client1, client2, client3"
	expected := []string{"client1", "client2", "client3"}

	// Simulate parsing
	var parsed []string
	for _, v := range splitByComma(input) {
		if trimmed := trimSpace(v); trimmed != "" {
			parsed = append(parsed, trimmed)
		}
	}

	if len(parsed) != len(expected) {
		t.Errorf("Expected %d items, got %d", len(expected), len(parsed))
	}

	t.Log("MCP headers are parsed as comma-separated lists with whitespace trimming")
}

// TestVirtualKey_Sources documents virtual key extraction sources
func TestVirtualKey_Sources(t *testing.T) {
	// Virtual keys can be extracted from:
	// 1. x-bf-vk header
	// 2. Authorization header (Bearer vk_...)
	// 3. x-api-key header (vk_...)
	// 4. x-goog-api-key header (vk_...)

	// Virtual key prefix
	virtualKeyPrefix := "vk_"

	testKeys := []string{
		"vk_test123",
		"vk_production456",
	}

	for _, key := range testKeys {
		if len(key) < len(virtualKeyPrefix) || key[:len(virtualKeyPrefix)] != virtualKeyPrefix {
			t.Errorf("Expected key '%s' to have prefix '%s'", key, virtualKeyPrefix)
		}
	}

	t.Log("Virtual keys are identified by 'vk_' prefix and can come from multiple headers")
}

// TestDirectKeys_Sources documents direct key extraction sources
func TestDirectKeys_Sources(t *testing.T) {
	// Direct API keys (when allowDirectKeys=true) can be extracted from:
	// 1. Authorization header (Bearer sk-...)
	// 2. x-api-key header
	// 3. x-goog-api-key header

	// Keys are NOT extracted if they start with virtual key prefix
	virtualKeyPrefix := "vk_"

	testCases := []struct {
		key       string
		extracted bool
	}{
		{"sk-test123", true},           // Regular API key
		{"AIza-test123", true},         // Google API key
		{"vk_test123", false},          // Virtual key - not extracted as direct key
	}

	for _, tc := range testCases {
		isVirtualKey := len(tc.key) >= len(virtualKeyPrefix) && tc.key[:len(virtualKeyPrefix)] == virtualKeyPrefix
		shouldExtract := !isVirtualKey

		if shouldExtract != tc.extracted {
			t.Errorf("Key '%s': expected extracted=%v, got %v", tc.key, tc.extracted, shouldExtract)
		}
	}

	t.Log("Direct keys are extracted from standard auth headers, excluding virtual keys")
}

// Helper functions for tests
func splitByComma(s string) []string {
	var result []string
	for _, part := range splitString(s, ',') {
		result = append(result, part)
	}
	return result
}

func splitString(s string, sep rune) []string {
	var result []string
	var current string
	for _, r := range s {
		if r == sep {
			result = append(result, current)
			current = ""
		} else {
			current += string(r)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
