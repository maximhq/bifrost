package server

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// TestConfig is a sample config struct for testing
type TestConfig struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Count   int    `json:"count"`
}

type updateStatusOnlyConfigStore struct {
	configstore.ConfigStore
	calls []schemas.KeyStatus
}

type noopTestLogger struct{}

func (noopTestLogger) Debug(string, ...any)                   {}
func (noopTestLogger) Info(string, ...any)                    {}
func (noopTestLogger) Warn(string, ...any)                    {}
func (noopTestLogger) Error(string, ...any)                   {}
func (noopTestLogger) Fatal(string, ...any)                   {}
func (noopTestLogger) SetLevel(schemas.LogLevel)              {}
func (noopTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func (s *updateStatusOnlyConfigStore) UpdateStatus(ctx context.Context, provider schemas.ModelProvider, keyID string, status, errorMsg string) error {
	s.calls = append(s.calls, schemas.KeyStatus{
		Provider: provider,
		KeyID:    keyID,
		Status:   schemas.KeyStatusType(status),
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: errorMsg}},
	})
	return nil
}

func TestUpdateKeyStatus_KeylessProviderUpdatesProviderStatusInMemory(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	store := &updateStatusOnlyConfigStore{}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ConfigStore: store,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"mock-openai": {
					CustomProviderConfig: &schemas.CustomProviderConfig{IsKeyLess: true},
					Status:               "unknown",
				},
			},
		},
	}

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "mock-openai",
		KeyID:    "",
		Status:   schemas.KeyStatusListModelsFailed,
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: "preview missing model"}},
	}})

	provider := server.Config.Providers["mock-openai"]
	if provider.Status != string(schemas.KeyStatusListModelsFailed) {
		t.Fatalf("expected provider status %q, got %q", schemas.KeyStatusListModelsFailed, provider.Status)
	}
	if provider.Description != "preview missing model" {
		t.Fatalf("expected provider description to be updated, got %q", provider.Description)
	}
	if len(store.calls) != 1 {
		t.Fatalf("expected one status update call, got %d", len(store.calls))
	}
	if store.calls[0].Provider != "mock-openai" || store.calls[0].KeyID != "" {
		t.Fatalf("expected provider-level status update, got provider=%q keyID=%q", store.calls[0].Provider, store.calls[0].KeyID)
	}
}

func TestUpdateKeyStatus_EmptyKeyIDDoesNotOverwriteKeyedProviderStatus(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	store := &updateStatusOnlyConfigStore{}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ConfigStore: store,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"openai": {
					Keys:   []schemas.Key{{ID: "key-1"}},
					Status: "healthy",
				},
			},
		},
	}

	server.updateKeyStatus(context.Background(), []schemas.KeyStatus{{
		Provider: "openai",
		KeyID:    "",
		Status:   schemas.KeyStatusListModelsFailed,
		Error:    &schemas.BifrostError{Error: &schemas.ErrorField{Message: "malformed status"}},
	}})

	provider := server.Config.Providers["openai"]
	if provider.Status != "healthy" {
		t.Fatalf("expected keyed provider status to remain unchanged, got %q", provider.Status)
	}
	if provider.Description != "" {
		t.Fatalf("expected keyed provider description to remain unchanged, got %q", provider.Description)
	}
	if len(store.calls) != 1 {
		t.Fatalf("expected one status update call, got %d", len(store.calls))
	}
	if store.calls[0].Provider != "openai" || store.calls[0].KeyID != "" {
		t.Fatalf("expected DB status update to retain empty key ID, got provider=%q keyID=%q", store.calls[0].Provider, store.calls[0].KeyID)
	}
}

func TestKeyEnabled(t *testing.T) {
	tests := []struct {
		name string
		key  schemas.Key
		want bool
	}{
		{"nil Enabled defaults to enabled", schemas.Key{}, true},
		{"explicit true is enabled", schemas.Key{Enabled: schemas.Ptr(true)}, true},
		{"explicit false is disabled", schemas.Key{Enabled: schemas.Ptr(false)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := keyEnabled(tt.key); got != tt.want {
				t.Fatalf("keyEnabled(%+v) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// TestRefreshLiveModelsForProvider_AllKeysDisabled cross-checks the "0
// enabled keys" case for issue #5037: discovery must skip scheduling any
// fetch (and therefore never touch s.Client, left nil here) rather than
// attempting doomed-to-fail per-key calls.
func TestRefreshLiveModelsForProvider_AllKeysDisabled(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ModelCatalog: modelcatalog.NewTestCatalog(nil),
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"custom-provider": {},
			},
		},
	}

	keys := []schemas.Key{
		{ID: "key-1", Enabled: schemas.Ptr(false)},
		{ID: "key-2", Enabled: schemas.Ptr(false)},
	}

	// Must return without panicking: s.Client is nil, so any scheduled fetch
	// would panic on the first ListModelsRequest call. Reaching the end of
	// this test proves no fetch was scheduled for either disabled key.
	server.RefreshLiveModelsForProvider(context.Background(), "custom-provider", keys)

	if models := server.Config.ModelCatalog.GetModelsForProvider("custom-provider"); len(models) != 0 {
		t.Fatalf("expected no live models to be written when all keys are disabled, got %v", models)
	}
}

// TestOnKeyAdded_DisabledKeySkipsFetch cross-checks that adding a key that
// is already disabled never schedules a discovery fetch for it.
func TestOnKeyAdded_DisabledKeySkipsFetch(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	disabledKey := schemas.Key{ID: "key-1", Enabled: schemas.Ptr(false)}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ModelCatalog: modelcatalog.NewTestCatalog(nil),
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"custom-provider": {Keys: []schemas.Key{disabledKey}},
			},
		},
	}

	// s.Client is left nil: if the disabled-key guard regresses, the fetch
	// goroutine would dereference it and panic, failing this test.
	if err := server.OnKeyAdded(context.Background(), "custom-provider", disabledKey); err != nil {
		t.Fatalf("OnKeyAdded returned unexpected error: %v", err)
	}

	if models := server.Config.ModelCatalog.GetModelsForProvider("custom-provider"); len(models) != 0 {
		t.Fatalf("expected no live models to be written for a disabled key, got %v", models)
	}
}

// TestOnKeyUpdated_DisabledKeySkipsFetchButInvalidatesCache cross-checks
// that toggling a key to disabled still evicts its stale cached models
// (InvalidateLive) even though the refetch is correctly skipped.
func TestOnKeyUpdated_DisabledKeySkipsFetchButInvalidatesCache(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive("custom-provider", "key-1", false, []string{"custom-provider/some-model"})

	disabledKey := schemas.Key{ID: "key-1", Enabled: schemas.Ptr(false)}
	server := &BifrostHTTPServer{
		Config: &lib.Config{
			ModelCatalog: catalog,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"custom-provider": {Keys: []schemas.Key{disabledKey}},
			},
		},
	}

	if models := server.Config.ModelCatalog.GetModelsForProvider("custom-provider"); len(models) == 0 {
		t.Fatalf("expected seeded live model before update, got none")
	}

	// s.Client is left nil: if the disabled-key guard regresses, the fetch
	// goroutine would dereference it and panic, failing this test.
	if err := server.OnKeyUpdated(context.Background(), "custom-provider", disabledKey); err != nil {
		t.Fatalf("OnKeyUpdated returned unexpected error: %v", err)
	}

	if models := server.Config.ModelCatalog.GetModelsForProvider("custom-provider"); len(models) != 0 {
		t.Fatalf("expected InvalidateLive to drop the disabled key's cached models, got %v", models)
	}
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
