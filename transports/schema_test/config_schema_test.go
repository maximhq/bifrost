package schema_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
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

// compileSchema loads and compiles the config.schema.json for validation tests.
func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schemaPath := getSchemaPath(t)
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("failed to parse schema JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("config.schema.json", schemaDoc); err != nil {
		t.Fatalf("failed to add schema resource: %v", err)
	}
	compiled, err := c.Compile("config.schema.json")
	if err != nil {
		t.Fatalf("failed to compile schema: %v", err)
	}
	return compiled
}

// validateConfig unmarshals a JSON config string and validates it against the schema.
func validateConfig(t *testing.T, schema *jsonschema.Schema, configJSON string) error {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal([]byte(configJSON), &v); err != nil {
		t.Fatalf("invalid test JSON: %v", err)
	}
	return schema.Validate(v)
}

func TestSchemaVertexKeyDeployments(t *testing.T) {
	schemaPath := getSchemaPath(t)
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	t.Run("vertex_key $def includes deployments field", func(t *testing.T) {
		_, found := navigateJSON(schema, "$defs", "vertex_key", "allOf", 1, "properties", "vertex_key_config", "properties", "deployments")
		if !found {
			t.Error("$defs/vertex_key is missing 'deployments' property — vertex provider uses getModelDeployment() on every request")
		}
	})

	t.Run("vertex_key $def includes project_number field", func(t *testing.T) {
		_, found := navigateJSON(schema, "$defs", "vertex_key", "allOf", 1, "properties", "vertex_key_config", "properties", "project_number")
		if !found {
			t.Error("$defs/vertex_key is missing 'project_number' property — VertexKeyConfig Go struct defines this field")
		}
	})

	t.Run("vertex config with deployments validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"providers": {
				"vertex": {
					"keys": [{
						"key_id": "test",
						"name": "test",
						"value": "",
						"weight": 1,
						"models": ["gemini-2.0-flash"],
						"vertex_key_config": {
							"project_id": "my-project",
							"region": "us-central1",
							"auth_credentials": "",
							"project_number": "123456",
							"deployments": {"gemini-2.0-flash": "gemini-2.0-flash-001"}
						}
					}]
				}
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("vertex config with deployments should be valid, got: %v", err)
		}
	})
}

func TestSchemaGovernanceModelConfigs(t *testing.T) {
	schemaPath := getSchemaPath(t)
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	t.Run("governance includes model_configs property", func(t *testing.T) {
		_, found := navigateJSON(schema, "properties", "governance", "properties", "model_configs")
		if !found {
			t.Error("governance is missing 'model_configs' property — GovernanceData struct and per-model rate limiting depend on it")
		}
	})

	t.Run("governance with model_configs validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"governance": {
				"rate_limits": [{"id": "rl-1", "token_max_limit": 1000000, "token_reset_duration": "1m"}],
				"model_configs": [{"id": "mc-1", "model_name": "gemini-2.0-flash", "provider": "vertex", "rate_limit_id": "rl-1"}]
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("governance with model_configs should be valid, got: %v", err)
		}
	})
}

// loadSchema reads and parses config.schema.json into a generic map.
func loadSchema(t *testing.T) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(getSchemaPath(t))
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}
	return schema
}

func TestSchemaClientMCPFields(t *testing.T) {
	schema := loadSchema(t)
	fields := []string{
		"allowed_headers",
		"mcp_agent_depth",
		"mcp_tool_execution_timeout",
		"mcp_code_mode_binding_level",
		"mcp_tool_sync_interval",
	}
	for _, field := range fields {
		t.Run("client has "+field, func(t *testing.T) {
			_, found := navigateJSON(schema, "properties", "client", "properties", field)
			if !found {
				t.Errorf("client is missing '%s' property — ClientConfig Go struct defines this field", field)
			}
		})
	}

	t.Run("client MCP fields validate successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"client": {
				"allowed_headers": ["X-Custom-Header"],
				"mcp_agent_depth": 5,
				"mcp_tool_execution_timeout": 60,
				"mcp_code_mode_binding_level": "server",
				"mcp_tool_sync_interval": 10
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("client with MCP fields should be valid, got: %v", err)
		}
	})
}

func TestSchemaProviderRawRequest(t *testing.T) {
	schema := loadSchema(t)

	for _, def := range []string{"provider", "provider_with_bedrock_config", "provider_with_vllm_config", "provider_with_azure_config", "provider_with_vertex_config"} {
		t.Run(def+" has send_back_raw_request", func(t *testing.T) {
			_, found := navigateJSON(schema, "$defs", def, "properties", "send_back_raw_request")
			if !found {
				t.Errorf("$defs/%s is missing 'send_back_raw_request' property — ProviderConfig Go struct defines this field", def)
			}
		})
		t.Run(def+" has custom_provider_config", func(t *testing.T) {
			_, found := navigateJSON(schema, "$defs", def, "properties", "custom_provider_config")
			if !found {
				t.Errorf("$defs/%s is missing 'custom_provider_config' property — ProviderConfig Go struct defines this field", def)
			}
		})
	}

	t.Run("provider with send_back_raw_request validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"providers": {
				"openai": {
					"keys": [{"name": "test", "value": "sk-test", "weight": 1, "models": ["gpt-4"]}],
					"send_back_raw_request": true,
					"send_back_raw_response": true
				}
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("provider with send_back_raw_request should be valid, got: %v", err)
		}
	})

	t.Run("provider with custom_provider_config validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"providers": {
				"openai": {
					"keys": [{"name": "test", "value": "sk-test", "weight": 1, "models": ["gpt-4"]}],
					"custom_provider_config": {
						"base_provider_type": "openai",
						"is_key_less": false,
						"allowed_requests": {
							"chat_completion": true,
							"chat_completion_stream": true
						}
					}
				}
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("provider with custom_provider_config should be valid, got: %v", err)
		}
	})
}

func TestSchemaGovernanceProviders(t *testing.T) {
	schema := loadSchema(t)

	t.Run("governance includes providers property", func(t *testing.T) {
		_, found := navigateJSON(schema, "properties", "governance", "properties", "providers")
		if !found {
			t.Error("governance is missing 'providers' property — GovernanceConfig Go struct defines this field")
		}
	})

	t.Run("governance with providers validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"governance": {
				"providers": [
					{"name": "openai", "budget_id": "b-1", "send_back_raw_request": true}
				]
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("governance with providers should be valid, got: %v", err)
		}
	})
}

func TestSchemaMCPToolSyncInterval(t *testing.T) {
	schema := loadSchema(t)

	t.Run("mcp includes tool_sync_interval property", func(t *testing.T) {
		_, found := navigateJSON(schema, "properties", "mcp", "properties", "tool_sync_interval")
		if !found {
			t.Error("mcp is missing 'tool_sync_interval' property — MCPConfig Go struct defines this field")
		}
	})

	t.Run("mcp with tool_sync_interval validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"mcp": {
				"client_configs": [],
				"tool_sync_interval": "10m"
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("mcp with tool_sync_interval should be valid, got: %v", err)
		}
	})
}

func TestSchemaMCPToolManagerCodeMode(t *testing.T) {
	schema := loadSchema(t)

	t.Run("mcp_tool_manager_config includes code_mode_binding_level", func(t *testing.T) {
		_, found := navigateJSON(schema, "$defs", "mcp_tool_manager_config", "properties", "code_mode_binding_level")
		if !found {
			t.Error("$defs/mcp_tool_manager_config is missing 'code_mode_binding_level' — MCPToolManagerConfig Go struct defines this field")
		}
	})

	t.Run("tool_manager_config with code_mode_binding_level validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"mcp": {
				"client_configs": [],
				"tool_manager_config": {
					"tool_execution_timeout": 30,
					"max_agent_depth": 10,
					"code_mode_binding_level": "tool"
				}
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("tool_manager_config with code_mode_binding_level should be valid, got: %v", err)
		}
	})
}

func TestSchemaMCPClientConfigFields(t *testing.T) {
	schema := loadSchema(t)

	fields := []string{
		"client_id",
		"is_code_mode_client",
		"connection_string",
		"auth_type",
		"oauth_config_id",
		"headers",
		"tools_to_execute",
		"tools_to_auto_execute",
		"tool_sync_interval",
	}
	for _, field := range fields {
		t.Run("mcp_client_config has "+field, func(t *testing.T) {
			_, found := navigateJSON(schema, "$defs", "mcp_client_config", "properties", field)
			if !found {
				t.Errorf("$defs/mcp_client_config is missing '%s' property — MCPClientConfig Go struct defines this field", field)
			}
		})
	}

	t.Run("mcp_client_config with new fields validates (stdio)", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"mcp": {
				"client_configs": [{
					"client_id": "mcp-1",
					"name": "test-mcp",
					"is_code_mode_client": false,
					"connection_type": "stdio",
					"auth_type": "none",
					"tools_to_execute": ["*"],
					"tools_to_auto_execute": [],
					"stdio_config": {
						"command": "npx",
						"args": ["-y", "@modelcontextprotocol/server-filesystem"]
					}
				}]
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("mcp_client_config with new fields (stdio) should be valid, got: %v", err)
		}
	})

	t.Run("mcp_client_config with SSE connection validates", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"mcp": {
				"client_configs": [{
					"name": "sse-client",
					"connection_type": "sse",
					"connection_string": "http://localhost:8080/sse",
					"auth_type": "headers",
					"headers": {"Authorization": "Bearer token123"}
				}]
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("mcp_client_config with SSE connection should be valid, got: %v", err)
		}
	})
}

func TestSchemaMCPConnectionTypeSSE(t *testing.T) {
	schema := loadSchema(t)

	t.Run("connection_type enum includes sse", func(t *testing.T) {
		enumVal, found := navigateJSON(schema, "$defs", "mcp_client_config", "properties", "connection_type", "enum")
		if !found {
			t.Fatal("could not find connection_type enum in mcp_client_config")
		}
		enumArr, ok := enumVal.([]interface{})
		if !ok {
			t.Fatal("connection_type enum is not an array")
		}
		hasSSE := false
		for _, v := range enumArr {
			if s, ok := v.(string); ok && s == "sse" {
				hasSSE = true
				break
			}
		}
		if !hasSSE {
			t.Error("connection_type enum does not include 'sse' — MCPConnectionType supports SSE")
		}
	})
}

func TestSchemaAllowedOriginsWildcard(t *testing.T) {
	schemaPath := getSchemaPath(t)
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read schema: %v", err)
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	t.Run("allowed_origins uses anyOf not oneOf", func(t *testing.T) {
		items, found := navigateJSON(schema, "properties", "client", "properties", "allowed_origins", "items")
		if !found {
			t.Fatal("could not find allowed_origins.items in schema")
		}
		itemsMap, ok := items.(map[string]interface{})
		if !ok {
			t.Fatal("allowed_origins.items is not an object")
		}
		if _, hasOneOf := itemsMap["oneOf"]; hasOneOf {
			t.Error("allowed_origins.items uses 'oneOf' — should use 'anyOf' because '*' matches both const and format:uri subschemas")
		}
		if _, hasAnyOf := itemsMap["anyOf"]; !hasAnyOf {
			t.Error("allowed_origins.items should use 'anyOf'")
		}
	})

	t.Run("allowed_origins wildcard validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"client": {
				"allowed_origins": ["*"]
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("allowed_origins with '*' should be valid, got: %v", err)
		}
	})
}