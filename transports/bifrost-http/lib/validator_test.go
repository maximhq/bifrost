package lib

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// loadLocalSchema reads the local config.schema.json for use in tests,
// avoiding remote fetches during test execution.
func loadLocalSchema(t *testing.T) []byte {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to get current file path")
	}
	schemaPath := filepath.Join(filepath.Dir(filename), "..", "..", "config.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("failed to read config.schema.json: %v", err)
	}
	return data
}

func TestValidateConfigSchema_ValidConfig(t *testing.T) {
	// Minimal valid config matching the schema
	validConfig := `{
		"providers": {
			"openai": {
				"keys": [
					{
						"name": "default",
						"value": "sk-test-key",
						"weight": 1.0,
						"models": ["gpt-4"]
					}
				]
			}
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_EmptyObject(t *testing.T) {
	// Empty object should be valid (all properties are optional)
	emptyConfig := `{}`

	err := ValidateConfigSchema([]byte(emptyConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected empty config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_InvalidJSON(t *testing.T) {
	invalidJSON := `{invalid json`

	err := ValidateConfigSchema([]byte(invalidJSON), loadLocalSchema(t))
	if err == nil {
		t.Error("expected invalid JSON to fail validation")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("expected error to mention 'invalid JSON', got: %v", err)
	}
}

func TestValidateConfigSchema_InvalidType(t *testing.T) {
	// client.initial_pool_size should be an integer, not a string
	invalidConfig := `{
		"client": {
			"initial_pool_size": "not-a-number"
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config with wrong type to fail validation")
	}
}

func TestValidateConfigSchema_InvalidEnum(t *testing.T) {
	// vector_store.type must be one of: weaviate, redis, qdrant, pinecone
	invalidConfig := `{
		"vector_store": {
			"enabled": true,
			"type": "invalid-store-type"
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config with invalid enum value to fail validation")
	}
}

func TestValidateConfigSchema_MissingRequiredField(t *testing.T) {
	// governance.budgets items require id, max_limit, and reset_duration
	invalidConfig := `{
		"governance": {
			"budgets": [
				{
					"id": "budget-1"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing required fields to fail validation")
	}
}

func TestValidateConfigSchema_ValidGovernanceConfig(t *testing.T) {
	validConfig := `{
		"governance": {
			"budgets": [
				{
					"id": "budget-1",
					"max_limit": 100.0,
					"reset_duration": "1d"
				}
			],
			"virtual_keys": [
				{
					"id": "vk-1",
					"name": "Test Key",
					"value": "vk_test123"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid governance config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_ValidClientConfig(t *testing.T) {
	// allowed_origins with "*" or URI strings (but not both in same array according to oneOf)
	validConfig := `{
		"client": {
			"initial_pool_size": 500,
			"drop_excess_requests": true,
			"enable_logging": true,
			"log_retention_days": 30,
			"allowed_origins": ["https://example.com"]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid client config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_InvalidMinimum(t *testing.T) {
	// initial_pool_size has minimum: 1
	invalidConfig := `{
		"client": {
			"initial_pool_size": 0
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config with value below minimum to fail validation")
	}
}

// =============================================================================
// Provider Key Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_ProviderKey_Valid(t *testing.T) {
	// Valid provider key with all required fields: name, value, weight
	validConfig := `{
		"providers": {
			"openai": {
				"keys": [
					{
						"name": "my-key",
						"value": "sk-test-key-12345",
						"weight": 1.0
					}
				]
			}
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid provider key config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_ProviderKey_MissingName(t *testing.T) {
	// Missing required field: name
	invalidConfig := `{
		"providers": {
			"openai": {
				"keys": [
					{
						"value": "sk-test-key-12345",
						"weight": 1.0
					}
				]
			}
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'name' in provider key to fail validation")
	}
}

func TestValidateConfigSchema_ProviderKey_MissingWeight(t *testing.T) {
	// Missing required field: weight
	invalidConfig := `{
		"providers": {
			"openai": {
				"keys": [
					{
						"name": "my-key",
						"value": "sk-test-key-12345"
					}
				]
			}
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'weight' in provider key to fail validation")
	}
}

// =============================================================================
// Governance Budget Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_Budget_Valid(t *testing.T) {
	// Valid budget with all required fields: id, max_limit, reset_duration
	validConfig := `{
		"governance": {
			"budgets": [
				{
					"id": "budget-1",
					"max_limit": 100.0,
					"reset_duration": "30d"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid budget config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_Budget_MissingId(t *testing.T) {
	// Missing required field: id
	invalidConfig := `{
		"governance": {
			"budgets": [
				{
					"max_limit": 100.0,
					"reset_duration": "30d"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'id' in budget to fail validation")
	}
}

func TestValidateConfigSchema_Budget_MissingMaxLimit(t *testing.T) {
	// Missing required field: max_limit
	invalidConfig := `{
		"governance": {
			"budgets": [
				{
					"id": "budget-1",
					"reset_duration": "30d"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'max_limit' in budget to fail validation")
	}
}

func TestValidateConfigSchema_Budget_MissingResetDuration(t *testing.T) {
	// Missing required field: reset_duration
	invalidConfig := `{
		"governance": {
			"budgets": [
				{
					"id": "budget-1",
					"max_limit": 100.0
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'reset_duration' in budget to fail validation")
	}
}

// =============================================================================
// Governance Rate Limit Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_RateLimit_Valid(t *testing.T) {
	// Valid rate limit with required field: id
	validConfig := `{
		"governance": {
			"rate_limits": [
				{
					"id": "rate-limit-1",
					"token_max_limit": 10000,
					"token_reset_duration": "1h"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid rate limit config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_RateLimit_MissingId(t *testing.T) {
	// Missing required field: id
	invalidConfig := `{
		"governance": {
			"rate_limits": [
				{
					"token_max_limit": 10000,
					"token_reset_duration": "1h"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'id' in rate limit to fail validation")
	}
}

// =============================================================================
// Governance Customer Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_Customer_Valid(t *testing.T) {
	// Valid customer with all required fields: id, name
	validConfig := `{
		"governance": {
			"customers": [
				{
					"id": "customer-1",
					"name": "Acme Corp"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid customer config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_Customer_MissingId(t *testing.T) {
	// Missing required field: id
	invalidConfig := `{
		"governance": {
			"customers": [
				{
					"name": "Acme Corp"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'id' in customer to fail validation")
	}
}

func TestValidateConfigSchema_Customer_MissingName(t *testing.T) {
	// Missing required field: name
	invalidConfig := `{
		"governance": {
			"customers": [
				{
					"id": "customer-1"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'name' in customer to fail validation")
	}
}

// =============================================================================
// Governance Team Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_Team_Valid(t *testing.T) {
	// Valid team with all required fields: id, name
	validConfig := `{
		"governance": {
			"teams": [
				{
					"id": "team-1",
					"name": "Engineering"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid team config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_Team_MissingId(t *testing.T) {
	// Missing required field: id
	invalidConfig := `{
		"governance": {
			"teams": [
				{
					"name": "Engineering"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'id' in team to fail validation")
	}
}

func TestValidateConfigSchema_Team_MissingName(t *testing.T) {
	// Missing required field: name
	invalidConfig := `{
		"governance": {
			"teams": [
				{
					"id": "team-1"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'name' in team to fail validation")
	}
}

// =============================================================================
// Governance Virtual Key Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_VirtualKey_Valid(t *testing.T) {
	// Valid virtual key with all required fields: id, name, value
	validConfig := `{
		"governance": {
			"virtual_keys": [
				{
					"id": "vk-1",
					"name": "Test Virtual Key",
					"value": "vk_test_123456"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid virtual key config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_VirtualKey_MissingId(t *testing.T) {
	// Missing required field: id
	invalidConfig := `{
		"governance": {
			"virtual_keys": [
				{
					"name": "Test Virtual Key",
					"value": "vk_test_123456"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'id' in virtual key to fail validation")
	}
}

func TestValidateConfigSchema_VirtualKey_MissingName(t *testing.T) {
	// Missing required field: name
	invalidConfig := `{
		"governance": {
			"virtual_keys": [
				{
					"id": "vk-1",
					"value": "vk_test_123456"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'name' in virtual key to fail validation")
	}
}

// =============================================================================
// Virtual Key Provider Config Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_VirtualKeyProviderConfig_Valid(t *testing.T) {
	// Valid virtual key provider config with required field: provider
	validConfig := `{
		"governance": {
			"virtual_keys": [
				{
					"id": "vk-1",
					"name": "Test Virtual Key",
					"value": "vk_test_123456",
					"provider_configs": [
						{
							"provider": "openai"
						}
					]
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid virtual key provider config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_VirtualKeyProviderConfig_MissingProvider(t *testing.T) {
	// Missing required field: provider
	invalidConfig := `{
		"governance": {
			"virtual_keys": [
				{
					"id": "vk-1",
					"name": "Test Virtual Key",
					"value": "vk_test_123456",
					"provider_configs": [
						{
							"weight": 1.0
						}
					]
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'provider' in virtual key provider config to fail validation")
	}
}

// =============================================================================
// Virtual Key MCP Config Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_VirtualKeyMCPConfig_Valid(t *testing.T) {
	// Valid virtual key MCP config with required field: mcp_client_id
	validConfig := `{
		"governance": {
			"virtual_keys": [
				{
					"id": "vk-1",
					"name": "Test Virtual Key",
					"value": "vk_test_123456",
					"mcp_configs": [
						{
							"mcp_client_id": 1
						}
					]
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid virtual key MCP config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_VirtualKeyMCPConfig_MissingMCPClientId(t *testing.T) {
	// Missing required field: mcp_client_id
	invalidConfig := `{
		"governance": {
			"virtual_keys": [
				{
					"id": "vk-1",
					"name": "Test Virtual Key",
					"value": "vk_test_123456",
					"mcp_configs": [
						{
							"tools_to_execute": ["tool1"]
						}
					]
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'mcp_client_id' in virtual key MCP config to fail validation")
	}
}

// =============================================================================
// MCP Client Config Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_MCPClientConfig_Valid_Stdio(t *testing.T) {
	// Valid MCP client config with stdio connection type
	validConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "stdio",
					"stdio_config": {
						"command": "/usr/bin/my-tool"
					}
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid MCP client config (stdio) to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_MCPClientConfig_Valid_Websocket(t *testing.T) {
	// Valid MCP client config with websocket connection type
	validConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "websocket",
					"websocket_config": {
						"url": "ws://localhost:8080"
					}
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid MCP client config (websocket) to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_MCPClientConfig_Valid_Http(t *testing.T) {
	// Valid MCP client config with http connection type
	validConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "http",
					"http_config": {
						"url": "http://localhost:8080"
					}
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid MCP client config (http) to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_MCPClientConfig_MissingName(t *testing.T) {
	// Missing required field: name
	invalidConfig := `{
		"mcp": {
			"client_configs": [
				{
					"connection_type": "stdio",
					"stdio_config": {
						"command": "/usr/bin/my-tool"
					}
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'name' in MCP client config to fail validation")
	}
}

func TestValidateConfigSchema_MCPClientConfig_MissingConnectionType(t *testing.T) {
	// Missing required field: connection_type
	invalidConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"stdio_config": {
						"command": "/usr/bin/my-tool"
					}
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'connection_type' in MCP client config to fail validation")
	}
}

func TestValidateConfigSchema_MCPClientConfig_MissingStdioConfig(t *testing.T) {
	// Missing conditional required field: stdio_config when connection_type is stdio
	invalidConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "stdio"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'stdio_config' for stdio connection type to fail validation")
	}
}

func TestValidateConfigSchema_MCPClientConfig_MissingWebsocketConfig(t *testing.T) {
	// Missing conditional required field: websocket_config when connection_type is websocket
	invalidConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "websocket"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'websocket_config' for websocket connection type to fail validation")
	}
}

func TestValidateConfigSchema_MCPClientConfig_MissingHttpConfig(t *testing.T) {
	// Missing conditional required field: http_config when connection_type is http
	invalidConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "http"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'http_config' for http connection type to fail validation")
	}
}

func TestValidateConfigSchema_MCPClientConfig_StdioConfig_MissingCommand(t *testing.T) {
	// Missing required field in stdio_config: command
	invalidConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "stdio",
					"stdio_config": {
						"args": ["--help"]
					}
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'command' in stdio_config to fail validation")
	}
}

func TestValidateConfigSchema_MCPClientConfig_WebsocketConfig_MissingUrl(t *testing.T) {
	// Missing required field in websocket_config: url
	invalidConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "websocket",
					"websocket_config": {}
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'url' in websocket_config to fail validation")
	}
}

func TestValidateConfigSchema_MCPClientConfig_HttpConfig_MissingUrl(t *testing.T) {
	// Missing required field in http_config: url
	invalidConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "http",
					"http_config": {}
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'url' in http_config to fail validation")
	}
}

// =============================================================================
// Concurrency Config Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_ConcurrencyConfig_Valid(t *testing.T) {
	// Valid concurrency config with all required fields: concurrency, buffer_size
	validConfig := `{
		"providers": {
			"openai": {
				"keys": [
					{
						"name": "my-key",
						"value": "sk-test-key",
						"weight": 1.0
					}
				],
				"concurrency_and_buffer_size": {
					"concurrency": 10,
					"buffer_size": 100
				}
			}
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid concurrency config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_ConcurrencyConfig_MissingConcurrency(t *testing.T) {
	// Missing required field: concurrency
	invalidConfig := `{
		"providers": {
			"openai": {
				"keys": [
					{
						"name": "my-key",
						"value": "sk-test-key",
						"weight": 1.0
					}
				],
				"concurrency_and_buffer_size": {
					"buffer_size": 100
				}
			}
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'concurrency' in concurrency config to fail validation")
	}
}

func TestValidateConfigSchema_ConcurrencyConfig_MissingBufferSize(t *testing.T) {
	// Missing required field: buffer_size
	invalidConfig := `{
		"providers": {
			"openai": {
				"keys": [
					{
						"name": "my-key",
						"value": "sk-test-key",
						"weight": 1.0
					}
				],
				"concurrency_and_buffer_size": {
					"concurrency": 10
				}
			}
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'buffer_size' in concurrency config to fail validation")
	}
}

// =============================================================================
// Plugin Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_Plugin_Valid(t *testing.T) {
	// Valid plugin with all required fields: enabled, name, config
	// Note: telemetry plugin requires config object
	validConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "telemetry",
				"config": {}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid plugin config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_Plugin_MissingEnabled(t *testing.T) {
	// Missing required field: enabled
	invalidConfig := `{
		"plugins": [
			{
				"name": "telemetry"
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'enabled' in plugin to fail validation")
	}
}

func TestValidateConfigSchema_Plugin_MissingName(t *testing.T) {
	// Missing required field: name
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'name' in plugin to fail validation")
	}
}

// =============================================================================
// Semantic Cache Plugin Config Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_SemanticCachePlugin_Valid(t *testing.T) {
	// Valid semantic cache plugin with all required fields: provider, keys, dimension
	validConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semanticcache",
				"config": {
					"provider": "openai",
					"keys": ["sk-test-key"],
					"dimension": 1536
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid semantic cache plugin config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_SemanticCachePlugin_MissingProvider(t *testing.T) {
	// Missing required field: provider
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semanticcache",
				"config": {
					"keys": ["sk-test-key"],
					"dimension": 1536
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'provider' in semantic cache plugin to fail validation")
	}
}

func TestValidateConfigSchema_SemanticCachePlugin_MissingKeys(t *testing.T) {
	// Missing required field: keys
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semanticcache",
				"config": {
					"provider": "openai",
					"dimension": 1536
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'keys' in semantic cache plugin to fail validation")
	}
}

func TestValidateConfigSchema_SemanticCachePlugin_MissingDimension(t *testing.T) {
	// Missing required field: dimension
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semanticcache",
				"config": {
					"provider": "openai",
					"keys": ["sk-test-key"]
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'dimension' in semantic cache plugin to fail validation")
	}
}

// =============================================================================
// OTEL Plugin Config Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_OtelPlugin_Valid(t *testing.T) {
	// Valid OTEL plugin with all required fields: collector_url, trace_type, protocol
	validConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "otel",
				"config": {
					"collector_url": "http://localhost:4318",
					"trace_type": "otel",
					"protocol": "http"
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid OTEL plugin config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_OtelPlugin_MissingCollectorUrl(t *testing.T) {
	// Missing required field: collector_url
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "otel",
				"config": {
					"trace_type": "otel",
					"protocol": "http"
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'collector_url' in OTEL plugin to fail validation")
	}
}

func TestValidateConfigSchema_OtelPlugin_MissingTraceType(t *testing.T) {
	// Missing required field: trace_type
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "otel",
				"config": {
					"collector_url": "http://localhost:4318",
					"protocol": "http"
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'trace_type' in OTEL plugin to fail validation")
	}
}

func TestValidateConfigSchema_OtelPlugin_MissingProtocol(t *testing.T) {
	// Missing required field: protocol
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "otel",
				"config": {
					"collector_url": "http://localhost:4318",
					"trace_type": "otel"
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'protocol' in OTEL plugin to fail validation")
	}
}

// =============================================================================
// Maxim Plugin Config Required Fields Tests
// =============================================================================

func TestValidateConfigSchema_MaximPlugin_Valid(t *testing.T) {
	// Valid Maxim plugin with required field: api_key
	validConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "maxim",
				"config": {
					"api_key": "maxim-api-key-12345"
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid Maxim plugin config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_MaximPlugin_MissingApiKey(t *testing.T) {
	// Missing required field: api_key
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "maxim",
				"config": {
					"log_repo_id": "my-log-repo"
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'api_key' in Maxim plugin to fail validation")
	}
}

// =============================================================================
// Azure Key Config Required Fields Tests
// Note: Azure provider uses a special key schema that extends base_key
// The azure_key_config is only valid within the azure provider's keys array
// =============================================================================

func TestValidateConfigSchema_AzureKeyConfig_MissingEndpoint(t *testing.T) {
	// Missing required field: endpoint in azure_key_config
	// This test validates that when azure_key_config is present, endpoint is required
	invalidConfig := `{
		"providers": {
			"azure": {
				"keys": [
					{
						"name": "azure-key",
						"value": "azure-api-key",
						"weight": 1.0,
						"azure_key_config": {
							"api_version": "2024-02-15-preview"
						}
					}
				]
			}
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'endpoint' in Azure key config to fail validation")
	}
}

func TestValidateConfigSchema_AzureKeyConfig_MissingApiVersion(t *testing.T) {
	// Missing required field: api_version in azure_key_config
	invalidConfig := `{
		"providers": {
			"azure": {
				"keys": [
					{
						"name": "azure-key",
						"value": "azure-api-key",
						"weight": 1.0,
						"azure_key_config": {
							"endpoint": "https://my-resource.openai.azure.com"
						}
					}
				]
			}
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'api_version' in Azure key config to fail validation")
	}
}

// =============================================================================
// Vertex Key Config Required Fields Tests
// Note: Vertex provider uses a special key schema that extends base_key
// =============================================================================

func TestValidateConfigSchema_VertexKeyConfig_MissingProjectId(t *testing.T) {
	// Missing required field: project_id in vertex_key_config
	invalidConfig := `{
		"providers": {
			"vertex": {
				"keys": [
					{
						"name": "vertex-key",
						"value": "vertex-api-key",
						"weight": 1.0,
						"vertex_key_config": {
							"region": "us-central1"
						}
					}
				]
			}
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'project_id' in Vertex key config to fail validation")
	}
}

func TestValidateConfigSchema_VertexKeyConfig_MissingRegion(t *testing.T) {
	// Missing required field: region in vertex_key_config
	invalidConfig := `{
		"providers": {
			"vertex": {
				"keys": [
					{
						"name": "vertex-key",
						"value": "vertex-api-key",
						"weight": 1.0,
						"vertex_key_config": {
							"project_id": "my-gcp-project"
						}
					}
				]
			}
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'region' in Vertex key config to fail validation")
	}
}

// =============================================================================
// Bedrock Key Config Required Fields Tests
// Note: Bedrock provider uses a special key schema that extends base_key
// =============================================================================

func TestValidateConfigSchema_BedrockKeyConfig_MissingRegion(t *testing.T) {
	// Missing required field: region in bedrock_key_config
	invalidConfig := `{
		"providers": {
			"bedrock": {
				"keys": [
					{
						"name": "bedrock-key",
						"value": "bedrock-api-key",
						"weight": 1.0,
						"bedrock_key_config": {
							"access_key": "AKIAIOSFODNN7EXAMPLE"
						}
					}
				]
			}
		}
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected config missing 'region' in Bedrock key config to fail validation")
	}
}

// =============================================================================
// Guardrails Config Tests
// Note: Guardrails is an enterprise feature. The guardrails_config schema
// validation is tested but the detailed rules/providers validation is only
// available in the enterprise version. The public schema validates that the
// guardrails_config exists but doesn't expose detailed structure.
// =============================================================================

// Guardrails tests are skipped for the public schema as guardrails_config
// is an enterprise feature with a different schema structure.
// Enterprise-specific tests should be added to the enterprise test suite.
