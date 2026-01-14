package server

import (
	"testing"
)

// TestConfig is a sample config struct for testing
type TestConfig struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Count   int    `json:"count"`
}

func TestMarshalPluginConfig_WithPointerType(t *testing.T) {
	// Test case 1: source is already *T
	expected := &TestConfig{
		Name:    "test-plugin",
		Enabled: true,
		Count:   42,
	}

	result, err := MarshalPluginConfig[TestConfig](expected)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result != expected {
		t.Errorf("Expected same pointer, got different pointer")
	}

	if result.Name != expected.Name {
		t.Errorf("Expected Name=%s, got %s", expected.Name, result.Name)
	}
	if result.Enabled != expected.Enabled {
		t.Errorf("Expected Enabled=%v, got %v", expected.Enabled, result.Enabled)
	}
	if result.Count != expected.Count {
		t.Errorf("Expected Count=%d, got %d", expected.Count, result.Count)
	}
}

func TestMarshalPluginConfig_WithMap(t *testing.T) {
	// Test case 2: source is map[string]any
	configMap := map[string]any{
		"name":    "test-plugin",
		"enabled": true,
		"count":   42,
	}

	result, err := MarshalPluginConfig[TestConfig](configMap)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.Name != "test-plugin" {
		t.Errorf("Expected Name=test-plugin, got %s", result.Name)
	}
	if result.Enabled != true {
		t.Errorf("Expected Enabled=true, got %v", result.Enabled)
	}
	if result.Count != 42 {
		t.Errorf("Expected Count=42, got %d", result.Count)
	}
}

func TestMarshalPluginConfig_WithString(t *testing.T) {
	// Test case 3: source is string (JSON)
	configStr := `{"name":"test-plugin","enabled":true,"count":42}`

	result, err := MarshalPluginConfig[TestConfig](configStr)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.Name != "test-plugin" {
		t.Errorf("Expected Name=test-plugin, got %s", result.Name)
	}
	if result.Enabled != true {
		t.Errorf("Expected Enabled=true, got %v", result.Enabled)
	}
	if result.Count != 42 {
		t.Errorf("Expected Count=42, got %d", result.Count)
	}
}

func TestMarshalPluginConfig_WithInvalidType(t *testing.T) {
	// Test case 4: source is invalid type (should return error)
	invalidSource := 12345

	result, err := MarshalPluginConfig[TestConfig](invalidSource)
	if err == nil {
		t.Fatal("Expected error for invalid type, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for invalid type, got %v", result)
	}

	expectedError := "invalid config type"
	if err.Error() != expectedError {
		t.Errorf("Expected error message '%s', got '%s'", expectedError, err.Error())
	}
}

func TestMarshalPluginConfig_WithInvalidJSONString(t *testing.T) {
	// Test case 5: source is string but invalid JSON
	invalidJSON := `{"name":"test-plugin","enabled":true,count:42}` // missing quotes around count

	result, err := MarshalPluginConfig[TestConfig](invalidJSON)
	if err == nil {
		t.Fatal("Expected error for invalid JSON, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for invalid JSON, got %v", result)
	}
}

func TestMarshalPluginConfig_WithInvalidMapData(t *testing.T) {
	// Test case 6: source is map but contains invalid data types
	configMap := map[string]any{
		"name":    "test-plugin",
		"enabled": "not-a-boolean", // wrong type
		"count":   42,
	}

	result, err := MarshalPluginConfig[TestConfig](configMap)
	if err == nil {
		t.Fatal("Expected error for invalid map data, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for invalid map data, got %v", result)
	}
}

func TestMarshalPluginConfig_WithEmptyMap(t *testing.T) {
	// Test case 7: source is empty map (should work, return zero values)
	configMap := map[string]any{}

	result, err := MarshalPluginConfig[TestConfig](configMap)
	if err != nil {
		t.Fatalf("Expected no error for empty map, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// All fields should have zero values
	if result.Name != "" {
		t.Errorf("Expected empty Name, got %s", result.Name)
	}
	if result.Enabled != false {
		t.Errorf("Expected Enabled=false, got %v", result.Enabled)
	}
	if result.Count != 0 {
		t.Errorf("Expected Count=0, got %d", result.Count)
	}
}

func TestMarshalPluginConfig_WithEmptyString(t *testing.T) {
	// Test case 8: source is empty string (should fail as invalid JSON)
	configStr := ""

	result, err := MarshalPluginConfig[TestConfig](configStr)
	if err == nil {
		t.Fatal("Expected error for empty string, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for empty string, got %v", result)
	}
}

func TestMarshalPluginConfig_WithNil(t *testing.T) {
	// Test case 9: source is nil (should return error as invalid type)
	result, err := MarshalPluginConfig[TestConfig](nil)
	if err == nil {
		t.Fatal("Expected error for nil source, got nil")
	}

	if result != nil {
		t.Errorf("Expected nil result for nil source, got %v", result)
	}
}

// Benchmark tests
func BenchmarkMarshalPluginConfig_WithPointerType(b *testing.B) {
	config := &TestConfig{
		Name:    "test-plugin",
		Enabled: true,
		Count:   42,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MarshalPluginConfig[TestConfig](config)
	}
}

func BenchmarkMarshalPluginConfig_WithMap(b *testing.B) {
	configMap := map[string]any{
		"name":    "test-plugin",
		"enabled": true,
		"count":   42,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MarshalPluginConfig[TestConfig](configMap)
	}
}

func BenchmarkMarshalPluginConfig_WithString(b *testing.B) {
	configStr := `{"name":"test-plugin","enabled":true,"count":42}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MarshalPluginConfig[TestConfig](configStr)
	}
}

// Complex config for additional testing
type ComplexConfig struct {
	Settings map[string]string `json:"settings"`
	Tags     []string          `json:"tags"`
	Metadata map[string]any    `json:"metadata"`
	Nested   *TestConfig       `json:"nested"`
}

func TestMarshalPluginConfig_WithComplexType(t *testing.T) {
	// Test with a more complex nested structure
	configMap := map[string]any{
		"settings": map[string]any{
			"key1": "value1",
			"key2": "value2",
		},
		"tags": []any{"tag1", "tag2", "tag3"},
		"metadata": map[string]any{
			"version": "1.0.0",
			"author":  "test",
		},
		"nested": map[string]any{
			"name":    "nested-config",
			"enabled": true,
			"count":   10,
		},
	}

	result, err := MarshalPluginConfig[ComplexConfig](configMap)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if len(result.Settings) != 2 {
		t.Errorf("Expected 2 settings, got %d", len(result.Settings))
	}
	if len(result.Tags) != 3 {
		t.Errorf("Expected 3 tags, got %d", len(result.Tags))
	}
	if result.Nested == nil {
		t.Fatal("Expected non-nil nested config")
	}
	if result.Nested.Name != "nested-config" {
		t.Errorf("Expected nested name=nested-config, got %s", result.Nested.Name)
	}
}
