package schema_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// getSchemaPath returns the absolute path to config.schema.json.
func getSchemaPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to get caller info")
	}
	schemaPath := filepath.Join(filepath.Dir(filename), "..", "config.schema.json")
	if _, err := os.Stat(schemaPath); err != nil {
		t.Fatalf("config.schema.json not found at %s", schemaPath)
	}
	return schemaPath
}

// navigateJSON traverses a nested JSON structure using a sequence of keys.
// Supports string keys for objects and int keys for arrays.
func navigateJSON(data interface{}, keys ...interface{}) (interface{}, bool) {
	current := data
	for _, key := range keys {
		switch k := key.(type) {
		case string:
			m, ok := current.(map[string]interface{})
			if !ok {
				return nil, false
			}
			current, ok = m[k]
			if !ok {
				return nil, false
			}
		case int:
			arr, ok := current.([]interface{})
			if !ok || k >= len(arr) {
				return nil, false
			}
			current = arr[k]
		default:
			return nil, false
		}
	}
	return current, true
}

// findPostgresPortType finds the port type in a store's postgres config branch.
// It handles both anyOf and oneOf schema patterns used by config_store and logs_store.
func findPostgresPortType(schema map[string]interface{}, storeName string) (string, bool) {
	configBlock, ok := navigateJSON(schema, "properties", storeName, "properties", "config")
	if !ok {
		return "", false
	}
	configMap, ok := configBlock.(map[string]interface{})
	if !ok {
		return "", false
	}

	var branches []interface{}
	if anyOf, exists := configMap["anyOf"]; exists {
		branches, _ = anyOf.([]interface{})
	} else if oneOf, exists := configMap["oneOf"]; exists {
		branches, _ = oneOf.([]interface{})
	}

	for _, branch := range branches {
		thenBlock, ok := navigateJSON(branch, "then")
		if !ok {
			continue
		}
		portType, ok := navigateJSON(thenBlock, "properties", "port", "type")
		if !ok {
			continue
		}
		if typeStr, ok := portType.(string); ok {
			return typeStr, true
		}
	}
	return "", false
}

func TestSchemaLogsStorePortType(t *testing.T) {
	schemaPath := getSchemaPath(t)
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	t.Run("logs_store port type is string", func(t *testing.T) {
		portType, found := findPostgresPortType(schema, "logs_store")
		if !found {
			t.Fatal("could not find logs_store postgres port type in schema")
		}
		if portType != "string" {
			t.Errorf("logs_store.config.port type = %q, want %q (Go code uses *schemas.EnvVar)", portType, "string")
		}
	})

	t.Run("config_store port type is string", func(t *testing.T) {
		portType, found := findPostgresPortType(schema, "config_store")
		if !found {
			t.Fatal("could not find config_store postgres port type in schema")
		}
		if portType != "string" {
			t.Errorf("config_store.config.port type = %q, want %q (Go code uses *schemas.EnvVar)", portType, "string")
		}
	})

	t.Run("both store port types are consistent", func(t *testing.T) {
		logsPortType, logsFound := findPostgresPortType(schema, "logs_store")
		configPortType, configFound := findPostgresPortType(schema, "config_store")
		if !logsFound || !configFound {
			t.Fatal("both store port types must be found in schema")
		}
		if logsPortType != configPortType {
			t.Errorf("port type mismatch: logs_store=%q, config_store=%q", logsPortType, configPortType)
		}
	})
}