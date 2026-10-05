package lib

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/santhosh-tekuri/jsonschema/v6"
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

func TestValidateConfigSchema_CustomSchemaURL(t *testing.T) {
	schema := loadLocalSchema(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(schema)
	}))
	t.Cleanup(server.Close)
	t.Setenv(ConfigSchemaURLEnv, "")

	config := []byte(`{"$schema":"` + server.URL + `"}`)
	err := ValidateConfigSchema(config)
	if err != nil {
		t.Errorf("expected schema to load from the config's custom $schema URL, got error: %v", err)
	}
}

func TestValidateConfigSchema_CustomSchemaURL_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	t.Setenv(ConfigSchemaURLEnv, "")

	config := []byte(`{"$schema":"` + server.URL + `"}`)
	err := ValidateConfigSchema(config)
	if err == nil {
		t.Fatal("expected a non-2xx schema response to fail")
	}
	if !strings.Contains(err.Error(), "failed to get config schema from") {
		t.Errorf("expected load error to name the schema location, got: %v", err)
	}
}

func TestValidateConfigSchema_CustomSchemaFilePath(t *testing.T) {
	schemaPath := filepath.Join(t.TempDir(), "config.schema.json")
	if err := os.WriteFile(schemaPath, loadLocalSchema(t), 0644); err != nil {
		t.Fatalf("failed to write temp schema: %v", err)
	}
	t.Setenv(ConfigSchemaURLEnv, schemaPath)

	err := ValidateConfigSchema([]byte(`{"$schema":"/opt/bifrost/config.schema.json"}`))
	if err != nil {
		t.Errorf("expected custom schema filesystem path to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_ConfigSchemaFilePath(t *testing.T) {
	schemaPath := filepath.Join(t.TempDir(), "config.schema.json")
	if err := os.WriteFile(schemaPath, loadLocalSchema(t), 0644); err != nil {
		t.Fatalf("failed to write temp schema: %v", err)
	}
	oldCandidates := localSchemaCandidates
	localSchemaCandidates = nil
	t.Cleanup(func() { localSchemaCandidates = oldCandidates })

	config := []byte(`{"$schema":"` + schemaPath + `"}`)
	err := ValidateConfigSchema(config)
	if err != nil {
		t.Errorf("expected config $schema filesystem path to load schema, got error: %v", err)
	}
}

func TestValidateConfigSchema_FileURL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "schema dir") // space exercises percent-decoding
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	schemaPath := filepath.Join(dir, "config.schema.json")
	if err := os.WriteFile(schemaPath, loadLocalSchema(t), 0644); err != nil {
		t.Fatalf("failed to write temp schema: %v", err)
	}
	fileURL := "file://" + strings.ReplaceAll(schemaPath, " ", "%20")
	t.Setenv(ConfigSchemaURLEnv, fileURL)

	err := ValidateConfigSchema([]byte(`{"$schema":"/opt/bifrost/config.schema.json"}`))
	if err != nil {
		t.Errorf("expected file:// schema URL to load schema from filesystem, got error: %v", err)
	}
}

func TestFilePathFromSchemaLocation(t *testing.T) {
	cases := map[string]string{
		"/opt/bifrost/config.schema.json":               "/opt/bifrost/config.schema.json",
		"file:///opt/bifrost/config.schema.json":        "/opt/bifrost/config.schema.json",
		"file://localhost/opt/config.schema.json":       "/opt/config.schema.json",
		"file:///opt/my%20dir/config.schema.json":       "/opt/my dir/config.schema.json",
		"https://www.getbifrost.ai/schema-as-path-only": "https://www.getbifrost.ai/schema-as-path-only",
	}
	for input, want := range cases {
		if got := filePathFromSchemaLocation(input); got != want {
			t.Errorf("filePathFromSchemaLocation(%q) = %q, want %q", input, got, want)
		}
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
	// Valid virtual key MCP config identifying the MCP client by mcp_client_id (DB form)
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

func TestValidateConfigSchema_VirtualKeyMCPConfig_ValidWithClientName(t *testing.T) {
	// Valid virtual key MCP config identifying the MCP client by mcp_client_name
	// (config-file form — resolved to mcp_client_id at startup). Either identifier
	// alone is sufficient; neither is required at the JSON Schema level.
	validConfig := `{
		"governance": {
			"virtual_keys": [
				{
					"id": "vk-1",
					"name": "Test Virtual Key",
					"value": "vk_test_123456",
					"mcp_configs": [
						{
							"mcp_client_name": "my-mcp-client",
							"tools_to_execute": ["tool1"]
						}
					]
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected virtual key MCP config with mcp_client_name to pass validation, got error: %v", err)
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

func TestValidateConfigSchema_MCPClientConfig_Valid_Sse(t *testing.T) {
	// Valid MCP client config with sse connection type
	validConfig := `{
		"mcp": {
			"client_configs": [
				{
					"name": "my-mcp-client",
					"connection_type": "sse",
					"connection_string": "http://localhost:8080"
				}
			]
		}
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected valid MCP client config (sse) to pass validation, got error: %v", err)
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
					"connection_string": "http://localhost:8080"
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
	// Valid semantic cache plugin with provider, embedding model, and dimension. Keys are injected at runtime.
	validConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semantic_cache",
				"config": {
					"provider": "openai",
					"embedding_model": "text-embedding-3-small",
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
	// Missing required field: provider for semantic mode (dimension > 1)
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semantic_cache",
				"config": {
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

func TestValidateConfigSchema_SemanticCachePlugin_ProviderWithoutKeys(t *testing.T) {
	// Keys are not required at schema level for provider-backed config.
	validConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semantic_cache",
				"config": {
					"provider": "openai",
					"embedding_model": "text-embedding-3-small",
					"dimension": 1536
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected provider-backed semantic cache config without plugin keys to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_SemanticCachePlugin_ProviderWithoutEmbeddingModel(t *testing.T) {
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semantic_cache",
				"config": {
					"provider": "openai",
					"dimension": 1536
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected provider-backed semantic cache config without embedding_model to fail validation")
	}
}

func TestValidateConfigSchema_SemanticCachePlugin_DirectModeValid(t *testing.T) {
	validConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semantic_cache",
				"config": {
					"dimension": 1
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected direct-only semantic cache config to pass validation, got error: %v", err)
	}
}

func TestValidateConfigSchema_SemanticCachePlugin_DirectModeWithEmbeddingModelInvalid(t *testing.T) {
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semantic_cache",
				"config": {
					"dimension": 1,
					"embedding_model": "text-embedding-3-small"
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected direct-only semantic cache config with embedding_model to fail validation")
	}
}

func TestValidateConfigSchema_SemanticCachePlugin_DimensionOneWithProviderInvalid(t *testing.T) {
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semantic_cache",
				"config": {
					"provider": "openai",
					"embedding_model": "text-embedding-3-small",
					"dimension": 1
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t))
	if err == nil {
		t.Error("expected dimension: 1 with provider in semantic cache plugin to fail validation")
	}
}

func TestValidateConfigSchema_SemanticCachePlugin_MissingDimension(t *testing.T) {
	// Missing required field: dimension
	invalidConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "semantic_cache",
				"config": {
					"provider": "openai",
					"embedding_model": "text-embedding-3-small"
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
					"trace_type": "genai_extension",
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

func TestValidateConfigSchema_OtelPlugin_GrpcAddress(t *testing.T) {
	// gRPC configs use bare host:port — previously failed because host:port satisfies both
	// format:uri (RFC 3986 opaque URI) and the host:port pattern, causing oneOf to reject it.
	grpcConfig := `{
		"plugins": [
			{
				"enabled": true,
				"name": "otel",
				"config": {
					"collector_url": "something.somewhere.svc.cluster.local:1111",
					"trace_type": "open_inference",
					"protocol": "grpc",
					"metrics_enabled": true,
					"metrics_endpoint": "something.somewhere.svc.cluster.local:1111"
				}
			}
		]
	}`

	err := ValidateConfigSchema([]byte(grpcConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected gRPC OTel config to pass validation, got error: %v", err)
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
					"trace_type": "genai_extension",
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
					"trace_type": "genai_extension"
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

func TestValidateConfigSchema_AzureKeyConfig_ApiVersionOptional(t *testing.T) {
	// api_version is optional; Azure uses the provider default when it is omitted.
	validConfig := `{
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

	err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t))
	if err != nil {
		t.Errorf("expected config missing optional 'api_version' in Azure key config to pass validation, got: %v", err)
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

func TestValidateConfigSchema_VertexKeyConfig_AWSWorkloadIdentity(t *testing.T) {
	// A federation block with only an audience is the minimal valid shape.
	validConfig := `{
		"providers": {
			"vertex": {
				"keys": [
					{
						"name": "vertex-eks-key",
						"value": "",
						"weight": 1.0,
						"vertex_key_config": {
							"project_id": "my-gcp-project",
							"region": "us-central1",
							"aws_workload_identity": {
								"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/eks/providers/aws",
								"service_account_email": "vertex@my-gcp-project.iam.gserviceaccount.com",
								"token_lifetime_seconds": 1800,
								"aws_region": "us-east-1",
								"aws_role_arn": "env.VERTEX_AWS_ROLE_ARN"
							}
						}
					}
				]
			}
		}
	}`

	if err := ValidateConfigSchema([]byte(validConfig), loadLocalSchema(t)); err != nil {
		t.Errorf("expected Vertex key with aws_workload_identity to pass validation, got: %v", err)
	}
}

func TestValidateConfigSchema_VertexKeyConfig_AWSWorkloadIdentity_MissingAudience(t *testing.T) {
	invalidConfig := `{
		"providers": {
			"vertex": {
				"keys": [
					{
						"name": "vertex-eks-key",
						"value": "",
						"weight": 1.0,
						"vertex_key_config": {
							"project_id": "my-gcp-project",
							"region": "us-central1",
							"aws_workload_identity": {
								"service_account_email": "vertex@my-gcp-project.iam.gserviceaccount.com"
							}
						}
					}
				]
			}
		}
	}`

	if err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t)); err == nil {
		t.Error("expected aws_workload_identity without 'audience' to fail validation")
	}
}

func TestValidateConfigSchema_VertexKeyConfig_AWSWorkloadIdentity_UnknownField(t *testing.T) {
	invalidConfig := `{
		"providers": {
			"vertex": {
				"keys": [
					{
						"name": "vertex-eks-key",
						"value": "",
						"weight": 1.0,
						"vertex_key_config": {
							"project_id": "my-gcp-project",
							"region": "us-central1",
							"aws_workload_identity": {
								"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/eks/providers/aws",
								"pool_id": "eks"
							}
						}
					}
				]
			}
		}
	}`

	if err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t)); err == nil {
		t.Error("expected unknown field inside aws_workload_identity to fail validation")
	}
}

func TestValidateConfigSchema_VertexKeyConfig_AWSWorkloadIdentity_ExcludesAuthCredentials(t *testing.T) {
	// Federation and a credentials JSON are two different identities; the schema must refuse both at once.
	invalidConfig := `{
		"providers": {
			"vertex": {
				"keys": [
					{
						"name": "vertex-eks-key",
						"value": "",
						"weight": 1.0,
						"vertex_key_config": {
							"project_id": "my-gcp-project",
							"region": "us-central1",
							"auth_credentials": "{\"type\":\"service_account\"}",
							"aws_workload_identity": {
								"audience": "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/eks/providers/aws"
							}
						}
					}
				]
			}
		}
	}`

	if err := ValidateConfigSchema([]byte(invalidConfig), loadLocalSchema(t)); err == nil {
		t.Error("expected auth_credentials alongside aws_workload_identity to fail validation")
	}

	// An empty auth_credentials string, as written by the UI when it clears the JSON tab, is fine.
	emptyCredentials := strings.Replace(invalidConfig, `"auth_credentials": "{\"type\":\"service_account\"}"`, `"auth_credentials": ""`, 1)
	if err := ValidateConfigSchema([]byte(emptyCredentials), loadLocalSchema(t)); err != nil {
		t.Errorf("expected empty auth_credentials alongside aws_workload_identity to pass validation, got: %v", err)
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

func TestValidateConfigSchema_VirtualKeyDeleteAfterExpireRequiresExpiresAt(t *testing.T) {
	schema := loadLocalSchema(t)
	withoutExpiry := `{
		"governance": {
			"virtual_keys": [
				{"id": "vk-1", "name": "Test Key", "delete_after_expire": true}
			]
		}
	}`
	if err := ValidateConfigSchema([]byte(withoutExpiry), schema); err == nil {
		t.Error("expected delete_after_expire without expires_at to fail validation")
	}

	withExpiry := `{
		"governance": {
			"virtual_keys": [
				{"id": "vk-1", "name": "Test Key", "expires_at": "2027-01-01T00:00:00Z", "delete_after_expire": true}
			]
		}
	}`
	if err := ValidateConfigSchema([]byte(withExpiry), schema); err != nil {
		t.Errorf("expected delete_after_expire with expires_at to pass validation, got error: %v", err)
	}
}

// getSchemaPath returns the absolute path to config.schema.json.
func getSchemaPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to get caller info")
	}
	schemaPath := filepath.Join(filepath.Dir(filename), "..", "..", "config.schema.json")
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
			t.Errorf("logs_store.config.port type = %q, want %q (Go code uses *schemas.SecretVar)", portType, "string")
		}
	})

	t.Run("config_store port type is string", func(t *testing.T) {
		portType, found := findPostgresPortType(schema, "config_store")
		if !found {
			t.Fatal("could not find config_store postgres port type in schema")
		}
		if portType != "string" {
			t.Errorf("config_store.config.port type = %q, want %q (Go code uses *schemas.SecretVar)", portType, "string")
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

func TestSchemaPostgresPasswordCommand(t *testing.T) {
	compiled := compileSchema(t)

	config := `{
		"config_store": {
			"enabled": true,
			"type": "postgres",
			"config": {
				"host": "db.example.com",
				"port": "5432",
				"user": "bifrost",
				"password_command": {
					"command": "aws",
					"args": ["rds", "generate-db-auth-token"],
					"timeout": "10s"
				},
				"db_name": "bifrost",
				"ssl_mode": "require",
				"conn_max_lifetime": "10m"
			}
		},
		"logs_store": {
			"enabled": true,
			"type": "postgres",
			"config": {
				"host": "db.example.com",
				"port": "5432",
				"user": "bifrost",
				"password_command": {
					"command": "aws",
					"args": ["rds", "generate-db-auth-token"],
					"timeout": "10s"
				},
				"db_name": "bifrost",
				"ssl_mode": "require",
				"conn_max_lifetime": "10m"
			}
		}
	}`

	if err := validateConfig(t, compiled, config); err != nil {
		t.Fatalf("postgres password_command config should be valid, got: %v", err)
	}
}

func TestSchemaPostgresPasswordCommandValidation(t *testing.T) {
	compiled := compileSchema(t)

	tests := []struct {
		name   string
		config string
	}{
		{
			name: "config_store rejects password and password_command together",
			config: postgresStoreConfig("config_store", `"password": "secret",
				"password_command": {"command": "aws"}`),
		},
		{
			name: "logs_store rejects password and password_command together",
			config: postgresStoreConfig("logs_store", `"password": "secret",
				"password_command": {"command": "aws"}`),
		},
		{
			name:   "config_store rejects empty password command",
			config: postgresStoreConfig("config_store", `"password_command": {"command": ""}`),
		},
		{
			name:   "config_store rejects inline args in password command",
			config: postgresStoreConfig("config_store", `"password_command": {"command": "aws rds"}`),
		},
		{
			name:   "config_store rejects zero password command timeout",
			config: postgresStoreConfig("config_store", `"password_command": {"command": "aws", "timeout": "0s"}`),
		},
		{
			name:   "logs_store rejects zero password command timeout",
			config: postgresStoreConfig("logs_store", `"password_command": {"command": "aws", "timeout": "0s"}`),
		},
		{
			name: "config_store rejects zero conn max lifetime",
			config: postgresStoreConfig("config_store", `"password_command": {"command": "aws"},
				"conn_max_lifetime": "0s"`),
		},
		{
			name: "logs_store rejects zero conn max lifetime",
			config: postgresStoreConfig("logs_store", `"password_command": {"command": "aws"},
				"conn_max_lifetime": "0s"`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateConfig(t, compiled, tt.config); err == nil {
				t.Fatal("config should be invalid")
			}
		})
	}
}

func TestSchemaLogsStoreWriterConfig(t *testing.T) {
	compiled := compileSchema(t)

	validConfig := `{
		"logs_store": {
			"enabled": true,
			"type": "sqlite",
			"config": {
				"path": "/tmp/logs.db"
			},
			"writer": {
				"max_batch_size": 500,
				"batch_interval": "2s",
				"max_batch_bytes": 1048576,
				"write_queue_capacity": 2000,
				"deferred_usage_concurrency": 3
			}
		}
	}`
	if err := validateConfig(t, compiled, validConfig); err != nil {
		t.Fatalf("logs_store.writer config should be valid, got: %v", err)
	}

	tests := []struct {
		name   string
		writer string
	}{
		{
			name:   "rejects zero max batch size",
			writer: `"max_batch_size": 0`,
		},
		{
			name:   "rejects zero batch interval",
			writer: `"batch_interval": "0s"`,
		},
		{
			name:   "rejects zero max batch bytes",
			writer: `"max_batch_bytes": 0`,
		},
		{
			name:   "rejects zero write queue capacity",
			writer: `"write_queue_capacity": 0`,
		},
		{
			name:   "rejects zero deferred usage concurrency",
			writer: `"deferred_usage_concurrency": 0`,
		},
		{
			name:   "rejects unknown writer field",
			writer: `"unknown": 1`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := fmt.Sprintf(`{
				"logs_store": {
					"enabled": true,
					"type": "sqlite",
					"config": {
						"path": "/tmp/logs.db"
					},
					"writer": {
						%s
					}
				}
			}`, tt.writer)
			if err := validateConfig(t, compiled, config); err == nil {
				t.Fatal("config should be invalid")
			}
		})
	}
}

// TestSchemaPostgresSessionTimeouts pins that both Postgres stores accept the
// runtime-pool session timeouts postgresconn.Config reads (statement_timeout,
// idle_in_transaction_session_timeout) with every value time.ParseDuration takes
// there, including the documented "0" and negative values that keep the server
// default, and still reject values it would refuse at startup.
func TestSchemaPostgresSessionTimeouts(t *testing.T) {
	compiled := compileSchema(t)
	for _, store := range []string{"config_store", "logs_store"} {
		for _, field := range []string{"statement_timeout", "idle_in_transaction_session_timeout"} {
			for _, value := range []string{"30s", "60s", "1m30s", "1.5s", "0", "-1s", "+1s"} {
				config := postgresStoreConfig(store, fmt.Sprintf(`"password": "secret", %q: %q`, field, value))
				if err := validateConfig(t, compiled, config); err != nil {
					t.Errorf("%s.config.%s = %q should be valid, got: %v", store, field, value, err)
				}
			}
			for _, value := range []string{"30", "abc", "", "5 s"} {
				config := postgresStoreConfig(store, fmt.Sprintf(`"password": "secret", %q: %q`, field, value))
				if err := validateConfig(t, compiled, config); err == nil {
					t.Errorf("%s.config.%s = %q should be rejected", store, field, value)
				}
			}
		}
	}
}

func postgresStoreConfig(storeName string, passwordFields string) string {
	return fmt.Sprintf(`{
		"%s": {
			"enabled": true,
			"type": "postgres",
			"config": {
				"host": "db.example.com",
				"port": "5432",
				"user": "bifrost",
				%s,
				"db_name": "bifrost",
				"ssl_mode": "require"
			}
		}
	}`, storeName, passwordFields)
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

func TestSchemaComplexityAnalyzerDependencies(t *testing.T) {
	compiled := compileSchema(t)

	tests := []struct {
		name      string
		analyzer  string
		wantError bool
	}{
		{
			name:     "base analyzer remains valid",
			analyzer: complexityAnalyzerSchemaConfig(),
		},
		{
			name:     "disabled session does not require semantic config",
			analyzer: complexityAnalyzerSchemaConfig(`,"session":{"enabled":false}`),
		},
		{
			name:      "enabled session requires semantic config",
			analyzer:  complexityAnalyzerSchemaConfig(`,"session":{"enabled":true}`),
			wantError: true,
		},
		{
			name:     "enabled session accepts semantic config",
			analyzer: complexityAnalyzerSchemaConfig(`,"session":{"enabled":true}`, `,"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"}`),
		},
		{
			name:     "semantic fallback none does not require llm config",
			analyzer: complexityAnalyzerSchemaConfig(`,"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small","fallback":"none"}`),
		},
		{
			name:      "semantic fallback llm requires llm config",
			analyzer:  complexityAnalyzerSchemaConfig(`,"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small","fallback":"llm"}`),
			wantError: true,
		},
		{
			name: "semantic fallback llm accepts llm config",
			analyzer: complexityAnalyzerSchemaConfig(
				`,"llm":{"provider":"openai","model":"gpt-4o-mini"}`,
				`,"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small","fallback":"llm"}`,
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := fmt.Sprintf(`{"governance":{"complexity_analyzer_config":%s}}`, tt.analyzer)
			err := validateConfig(t, compiled, config)
			if tt.wantError && err == nil {
				t.Fatal("expected schema validation to fail")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("expected schema validation to pass: %v", err)
			}
		})
	}
}

func complexityAnalyzerSchemaConfig(fields ...string) string {
	return `{"keywords":{"simple_keywords":["simple"],"medium_keywords":["medium"],"complex_keywords":["complex"]}` + strings.Join(fields, "") + `}`
}

// TestSchemaGuardrailRuleTarget verifies explicit MCP targets without breaking legacy LLM rules.
func TestSchemaGuardrailRuleTarget(t *testing.T) {
	compiled := compileSchema(t)

	tests := []struct {
		name      string
		target    string
		wantError bool
	}{
		{name: "explicit MCP target is valid", target: `,"target":"mcp"`},
		{name: "omitted target remains valid", target: ""},
		{name: "unknown target is rejected", target: `,"target":"agent"`, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := fmt.Sprintf(`{
				"guardrails_config": {
					"guardrail_rules": [{
						"id": 1,
						"name": "MCP rule",
						"enabled": true,
						"cel_expression": "true",
						"apply_to": "input"%s
					}]
				}
			}`, tt.target)

			err := validateConfig(t, compiled, config)
			if tt.wantError && err == nil {
				t.Fatal("config should be invalid")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("config should be valid, got: %v", err)
			}
		})
	}
}

// TestSchemaGuardrailRuleConversationWindow verifies config accepts the explicit
// history switch and rejects non-boolean values before Enterprise reconciliation.
func TestSchemaGuardrailRuleConversationWindow(t *testing.T) {
	compiled := compileSchema(t)

	tests := []struct {
		name      string
		window    string
		wantError bool
	}{
		{name: "current input only is valid", window: `,"send_all_conversation_turns":false,"max_turns_to_send":0`},
		{name: "all history is valid", window: `,"send_all_conversation_turns":true`},
		{name: "non boolean switch is rejected", window: `,"send_all_conversation_turns":"false"`, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := fmt.Sprintf(`{
				"guardrails_config": {
					"guardrail_rules": [{
						"id": 1,
						"name": "Conversation rule",
						"enabled": true,
						"cel_expression": "true",
						"apply_to": "input"%s
					}]
				}
			}`, test.window)

			err := validateConfig(t, compiled, config)
			if test.wantError && err == nil {
				t.Fatal("config should be invalid")
			}
			if !test.wantError && err != nil {
				t.Fatalf("config should be valid, got: %v", err)
			}
		})
	}
}

func TestSchemaSCIMConfigValidation(t *testing.T) {
	compiled := compileSchema(t)

	tests := []struct {
		name      string
		config    string
		wantError bool
	}{
		{
			name:   "disabled okta with empty config is valid",
			config: `{"scim_config":{"enabled":false,"provider":"okta","config":{}}}`,
		},
		{
			name:   "disabled entra with empty config is valid",
			config: `{"scim_config":{"enabled":false,"provider":"entra","config":{}}}`,
		},
		{
			name:   "disabled keycloak with empty config is valid",
			config: `{"scim_config":{"enabled":false,"provider":"keycloak","config":{}}}`,
		},
		{
			name:      "enabled okta with empty config is invalid",
			config:    `{"scim_config":{"enabled":true,"provider":"okta","config":{}}}`,
			wantError: true,
		},
		{
			name:      "enabled entra with empty config is invalid",
			config:    `{"scim_config":{"enabled":true,"provider":"entra","config":{}}}`,
			wantError: true,
		},
		{
			name:      "enabled keycloak with empty config is invalid",
			config:    `{"scim_config":{"enabled":true,"provider":"keycloak","config":{}}}`,
			wantError: true,
		},
		{
			name: "enabled keycloak with required config is valid",
			config: `{
				"scim_config": {
					"enabled": true,
					"provider": "keycloak",
					"config": {
						"serverUrl": "https://keycloak.company.com",
						"realm": "bifrost-prod",
						"clientId": "bifrost",
						"clientSecret": "env.KEYCLOAK_CLIENT_SECRET"
					}
				}
			}`,
		},
		{
			name:      "enabled generic with empty config is invalid",
			config:    `{"scim_config":{"enabled":true,"provider":"generic","config":{}}}`,
			wantError: true,
		},
		{
			name:      "enabled zitadel with empty config is invalid",
			config:    `{"scim_config":{"enabled":true,"provider":"zitadel","config":{}}}`,
			wantError: true,
		},
		{
			name:   "enabled zitadel with required config is valid",
			config: `{"scim_config":{"enabled":true,"provider":"zitadel","config":{"domain":"acme.zitadel.cloud","clientId":"bifrost"}}}`,
		},
		{
			name:      "enabled google with empty config is invalid",
			config:    `{"scim_config":{"enabled":true,"provider":"google","config":{}}}`,
			wantError: true,
		},
		{
			name:   "enabled google with required config is valid",
			config: `{"scim_config":{"enabled":true,"provider":"google","config":{"domain":"company.com","clientId":"bifrost"}}}`,
		},
		{
			name:      "enabled google with credentialMode env but no serviceAccountEnvVar is invalid",
			config:    `{"scim_config":{"enabled":true,"provider":"google","config":{"domain":"company.com","clientId":"bifrost","credentialMode":"env"}}}`,
			wantError: true,
		},
		{
			name:      "enabled sailpoint with empty config is invalid",
			config:    `{"scim_config":{"enabled":true,"provider":"sailpoint","config":{}}}`,
			wantError: true,
		},
		{
			name:   "enabled sailpoint with required config is valid",
			config: `{"scim_config":{"enabled":true,"provider":"sailpoint","config":{"product":"isc","tenant":"acme"}}}`,
		},
		{
			name:      "unknown provider is rejected",
			config:    `{"scim_config":{"enabled":true,"provider":"pingfederate","config":{}}}`,
			wantError: true,
		},
		{
			name: "enabled generic with required config is valid",
			config: `{
				"scim_config": {
					"enabled": true,
					"provider": "generic",
					"config": {
						"issuerUrl": "https://idp.company.com",
						"clientId": "bifrost"
					}
				}
			}`,
		},
		{
			name: "enabled generic with claim SCIM attributes is valid",
			config: `{
				"scim_config": {
					"enabled": true,
					"provider": "generic",
					"config": {
						"issuerUrl": "https://idp.company.com",
						"clientId": "bifrost",
						"claimScimAttributes": {
							"department": {"attributeType": "user", "attributeValue": "department"}
						}
					}
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(t, compiled, tt.config)
			if tt.wantError && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("expected config to validate, got: %v", err)
			}
		})
	}
}

func TestSchemaKeyAliases(t *testing.T) {
	schema := loadSchema(t)

	t.Run("base_key $def includes aliases field", func(t *testing.T) {
		_, found := navigateJSON(schema, "$defs", "base_key", "properties", "aliases")
		if !found {
			t.Error("$defs/base_key is missing 'aliases' property — aliases replaced per-provider deployments maps")
		}
	})

	t.Run("vertex_key $def includes project_number field", func(t *testing.T) {
		_, found := navigateJSON(schema, "$defs", "vertex_key", "allOf", 1, "properties", "vertex_key_config", "properties", "project_number")
		if !found {
			t.Error("$defs/vertex_key is missing 'project_number' property — VertexKeyConfig Go struct defines this field")
		}
	})

	t.Run("vertex_key_config does not include deployments field", func(t *testing.T) {
		_, found := navigateJSON(schema, "$defs", "vertex_key", "allOf", 1, "properties", "vertex_key_config", "properties", "deployments")
		if found {
			t.Error("$defs/vertex_key still has 'deployments' in vertex_key_config — deployments were moved to top-level key aliases")
		}
	})

	t.Run("key with aliases validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"providers": {
				"vertex": {
					"keys": [{
						"name": "test",
						"value": "",
						"weight": 1,
						"models": ["gemini-2.0-flash"],
						"aliases": {"gemini-2.0-flash": "gemini-2.0-flash-001"},
						"vertex_key_config": {
							"project_id": "my-project",
							"region": "us-central1",
							"auth_credentials": "",
							"project_number": "123456"
						}
					}]
				}
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("key with aliases should be valid, got: %v", err)
		}
	})

	t.Run("azure key with aliases validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"providers": {
				"azure": {
					"keys": [{
						"name": "test",
						"value": "my-api-key",
						"weight": 1,
						"models": ["gpt-4o"],
						"aliases": {"gpt-4o": "gpt-4o-deployment"},
						"azure_key_config": {
							"endpoint": "https://my-resource.openai.azure.com",
							"api_version": "2024-02-01"
						}
					}]
				}
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("azure key with aliases should be valid, got: %v", err)
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
		"mcp_disable_auto_tool_inject",
		"mcp_enable_temp_token_auth",
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
				"mcp_tool_sync_interval": 10,
				"mcp_disable_auto_tool_inject": false,
				"mcp_enable_temp_token_auth": true
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

func TestSchemaMCPClientEndpointSlug(t *testing.T) {
	schema := loadSchema(t)

	t.Run("mcp_client_config includes endpoint_slug property", func(t *testing.T) {
		_, found := navigateJSON(schema, "$defs", "mcp_client_config", "properties", "endpoint_slug")
		if !found {
			t.Error("$defs/mcp_client_config is missing 'endpoint_slug' — MCPClientConfig Go struct serializes this field")
		}
	})

	clientConfig := func(slug string) string {
		return fmt.Sprintf(`{
			"mcp": {
				"client_configs": [
					{"name": "example", "connection_type": "http", "connection_string": "https://example.com/mcp", "endpoint_slug": %s}
				]
			}
		}`, slug)
	}

	t.Run("client config with valid endpoint_slug validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		if err := validateConfig(t, compiled, clientConfig(`"my-server-1"`)); err != nil {
			t.Errorf("client config with endpoint_slug should be valid, got: %v", err)
		}
	})

	t.Run("client config with non-url-safe endpoint_slug rejected", func(t *testing.T) {
		compiled := compileSchema(t)
		if err := validateConfig(t, compiled, clientConfig(`"My Server"`)); err == nil {
			t.Error("non-url-safe endpoint_slug must be rejected by the schema")
		}
	})
}

func TestSchemaMCPVirtualMCPs(t *testing.T) {
	schema := loadSchema(t)

	t.Run("mcp includes virtual_mcps property", func(t *testing.T) {
		_, found := navigateJSON(schema, "properties", "mcp", "properties", "virtual_mcps")
		if !found {
			t.Error("mcp is missing 'virtual_mcps' property — MCPConfig Go struct defines this field")
		}
	})

	t.Run("virtual_mcp_config def includes endpoint_slug property", func(t *testing.T) {
		_, found := navigateJSON(schema, "$defs", "virtual_mcp_config", "properties", "endpoint_slug")
		if !found {
			t.Error("$defs/virtual_mcp_config is missing 'endpoint_slug' — VirtualMCPConfig Go struct serializes this field")
		}
	})

	t.Run("virtual_mcps entry with explicit endpoint_slug validates", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"mcp": {
				"virtual_mcps": [
					{
						"name": "Platform Tools",
						"endpoint_slug": "platform-tools",
						"enabled": true,
						"tools": [
							{"mcp_client_name": "github", "tool_names": ["create_pull_request"]}
						],
						"virtual_key_ids": ["vk-1"]
					}
				]
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("virtual_mcps entry should be valid, got: %v", err)
		}
	})

	t.Run("virtual_mcps entry with non-url-safe endpoint_slug rejected", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"mcp": {
				"virtual_mcps": [
					{"name": "Bad Slug", "endpoint_slug": "Not A Slug", "tools": [{"mcp_client_id": "c1"}]}
				]
			}
		}`
		if err := validateConfig(t, compiled, config); err == nil {
			t.Error("non-url-safe endpoint_slug must be rejected by the schema")
		}
	})

	t.Run("virtual_mcps entry missing required name/tools rejected", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{"mcp": {"virtual_mcps": [{"endpoint_slug": "x"}]}}`
		if err := validateConfig(t, compiled, config); err == nil {
			t.Error("virtual_mcps entry without name and tools must be rejected")
		}
	})

	t.Run("deprecated tool_groups still validates for backward compatibility", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"mcp": {
				"tool_groups": [
					{"name": "Legacy", "tools": [{"mcp_client_name": "github"}], "team_ids": ["t-1"]}
				]
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("deprecated tool_groups should still validate, got: %v", err)
		}
	})
}

func TestSchemaVKRotationCooldownBounds(t *testing.T) {
	compiled := compileSchema(t)

	cooldownConfig := func(value string) string {
		return fmt.Sprintf(`{"client": {"vk_rotation_cooldown": %s}}`, value)
	}

	t.Run("valid duration string accepted", func(t *testing.T) {
		if err := validateConfig(t, compiled, cooldownConfig(`"5m"`)); err != nil {
			t.Errorf("duration string cooldown should be valid, got: %v", err)
		}
	})

	t.Run("zero accepted", func(t *testing.T) {
		if err := validateConfig(t, compiled, cooldownConfig(`0`)); err != nil {
			t.Errorf("zero cooldown should be valid, got: %v", err)
		}
	})

	t.Run("30 days in nanoseconds accepted", func(t *testing.T) {
		if err := validateConfig(t, compiled, cooldownConfig(`2592000000000000`)); err != nil {
			t.Errorf("30-day cooldown should be valid, got: %v", err)
		}
	})

	t.Run("negative integer rejected", func(t *testing.T) {
		if err := validateConfig(t, compiled, cooldownConfig(`-1`)); err == nil {
			t.Error("negative cooldown must be rejected by the schema")
		}
	})

	t.Run("integer above 30 days rejected", func(t *testing.T) {
		if err := validateConfig(t, compiled, cooldownConfig(`2592000000000001`)); err == nil {
			t.Error("cooldown above 30 days must be rejected by the schema")
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
		var root struct {
			MCP schemas.MCPConfig `json:"mcp"`
		}
		if err := json.Unmarshal([]byte(config), &root); err != nil {
			t.Errorf("failed to unmarshal mcp config: %v", err)
		}
		if root.MCP.ToolManagerConfig == nil {
			t.Fatal("tool_manager_config missing after unmarshal")
		}
		if root.MCP.ToolManagerConfig.ToolExecutionTimeout.D() != 30*time.Second {
			t.Errorf("tool_execution_timeout should be 30 seconds, got: %v", root.MCP.ToolManagerConfig.ToolExecutionTimeout.D())
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
		// config.json declares OAuth inline via `oauth_config`; the
		// `oauth_config_id` FK is assigned server-side and is not settable here.
		"oauth_config",
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

func TestSchemaBedrockKeyConfigSTSFields(t *testing.T) {
	schema := loadSchema(t)

	stsFields := []string{"role_arn", "external_id", "session_name"}
	for _, field := range stsFields {
		t.Run("$defs/bedrock_key has "+field, func(t *testing.T) {
			_, found := navigateJSON(schema, "$defs", "bedrock_key", "allOf", 1, "properties", "bedrock_key_config", "properties", field)
			if !found {
				t.Errorf("$defs/bedrock_key bedrock_key_config is missing '%s' — BedrockKeyConfig Go struct defines this field for STS AssumeRole", field)
			}
		})
	}

	t.Run("$defs/bedrock_key has batch_s3_config", func(t *testing.T) {
		_, found := navigateJSON(schema, "$defs", "bedrock_key", "allOf", 1, "properties", "bedrock_key_config", "properties", "batch_s3_config")
		if !found {
			t.Error("$defs/bedrock_key bedrock_key_config is missing 'batch_s3_config' — BedrockKeyConfig Go struct defines this field for batch operations")
		}
	})

	t.Run("bedrock config with STS fields validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"providers": {
				"bedrock": {
					"keys": [
						{
							"name": "cross-account",
							"weight": 1,
							"models": ["us.anthropic.claude-sonnet-4-20250514-v1:0"],
							"bedrock_key_config": {
								"region": "us-west-2",
								"role_arn": "arn:aws:iam::123456789012:role/BedrockAccessRole",
								"session_name": "bifrost-cross-account",
								"external_id": "my-external-id"
							}
						}
					]
				}
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("bedrock config with STS AssumeRole fields should be valid, got: %v", err)
		}
	})

	t.Run("bedrock config with batch_s3_config validates successfully", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"providers": {
				"bedrock": {
					"keys": [
						{
							"name": "batch-key",
							"weight": 1,
							"models": ["us.anthropic.claude-sonnet-4-20250514-v1:0"],
							"bedrock_key_config": {
								"region": "us-east-1",
								"batch_s3_config": {
									"buckets": [
										{"bucket_name": "my-batch-bucket", "prefix": "bifrost/", "is_default": true},
										{"bucket_name": "my-secondary-bucket"}
									]
								}
							}
						}
					]
				}
			}
		}`
		if err := validateConfig(t, compiled, config); err != nil {
			t.Errorf("bedrock config with batch_s3_config should be valid, got: %v", err)
		}
	})

	t.Run("bedrock config with unknown fields is rejected", func(t *testing.T) {
		compiled := compileSchema(t)
		config := `{
			"providers": {
				"bedrock": {
					"keys": [
						{
							"name": "bad-key",
							"weight": 1,
							"models": ["us.anthropic.claude-sonnet-4-20250514-v1:0"],
							"bedrock_key_config": {
								"region": "us-east-1",
								"unknown_field": "should-fail"
							}
						}
					]
				}
			}
		}`
		if err := validateConfig(t, compiled, config); err == nil {
			t.Error("bedrock config with unknown fields should fail schema validation (additionalProperties: false)")
		}
	})
}

// TestSchemaLiveModelsSyncInterval pins the 0-or->=60 contract on
// framework.pricing.live_models_sync_interval. This schema is the source of
// truth users author against, so it has to reject the same values the config
// API rejects (ConfigHandler.updateConfig) and the file resolver silently
// clamps (ResolveFrameworkPricingConfig). A bare "minimum": 0 accepted 1-59
// and left the user with a config that validated and then behaved as 60.
func TestSchemaLiveModelsSyncInterval(t *testing.T) {
	compiled := compileSchema(t)

	tests := []struct {
		name      string
		value     string
		wantError bool
	}{
		{name: "zero disables the background refresh", value: "0"},
		{name: "the minimum enabled interval is accepted", value: "60"},
		{name: "the default is accepted", value: "3600"},
		{name: "below the minimum but above zero is rejected", value: "59", wantError: true},
		{name: "one second is rejected", value: "1", wantError: true},
		{name: "negative is rejected", value: "-1", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := `{"framework":{"pricing":{"live_models_sync_interval":` + tt.value + `}}}`
			err := validateConfig(t, compiled, config)
			if tt.wantError && err == nil {
				t.Errorf("live_models_sync_interval=%s should be rejected, got no error", tt.value)
			}
			if !tt.wantError && err != nil {
				t.Errorf("live_models_sync_interval=%s should be valid, got: %v", tt.value, err)
			}
		})
	}
}

// TestSchemaComplexitySemanticTimeout pins the timeout schema to what
// ComplexitySemanticConfig.UnmarshalJSON actually accepts. The two forms are one
// setting: the decoder takes a duration string or a number of milliseconds,
// rejects only negatives ("must be non-negative"), and normalized() then reads
// zero as "unset" and substitutes DefaultComplexitySemanticTimeout. A schema that
// accepted "0s" but rejected 0 flagged a config the gateway runs happily.
func TestSchemaComplexitySemanticTimeout(t *testing.T) {
	compiled := compileSchema(t)
	semantic := func(timeout string) string {
		return `{"governance": {"complexity_analyzer_config": {
			"keywords": {"simple_keywords": ["hi"], "medium_keywords": ["build"], "complex_keywords": ["design a system"]},
			"semantic": {"provider": "openai", "embedding_model": "text-embedding-3-small", "timeout": ` + timeout + `}
		}}}`
	}

	for name, timeout := range map[string]string{
		"zero as a number": "0",
		"zero as duration": `"0s"`,
		"number":           "1500",
		"fractional":       "1.5",
		"duration string":  `"1.5s"`,
	} {
		if err := validateConfig(t, compiled, semantic(timeout)); err != nil {
			t.Errorf("semantic timeout (%s) must be valid — the decoder accepts it, got: %v", name, err)
		}
	}

	for name, timeout := range map[string]string{
		"negative number":   "-1",
		"negative duration": `"-1s"`,
		"unitless string":   `"1500"`,
	} {
		if err := validateConfig(t, compiled, semantic(timeout)); err == nil {
			t.Errorf("semantic timeout (%s) must be rejected", name)
		}
	}
}

// TestSchemaComplexityLLMTimeout is the llm-block half of
// TestSchemaComplexitySemanticTimeout. Both blocks decode timeout the same way —
// a time.Duration with no empty-string sentinel, so absent, null, and zero all
// arrive as 0 and normalized() substitutes the default — which is why zero has to
// be accepted in both forms here too. Fields whose Go type is a string spell
// "unset" as "" and do reject an explicit zero; those carry a ^[1-9] pattern
// instead.
func TestSchemaComplexityLLMTimeout(t *testing.T) {
	compiled := compileSchema(t)
	llm := func(timeout string) string {
		return `{"governance": {"complexity_analyzer_config": {
			"keywords": {"simple_keywords": ["hi"], "medium_keywords": ["build"], "complex_keywords": ["design a system"]},
			"semantic": {"provider": "openai", "embedding_model": "text-embedding-3-small", "fallback": "llm"},
			"llm": {"provider": "openai", "model": "gpt-4.1-mini", "timeout": ` + timeout + `}
		}}}`
	}

	for name, timeout := range map[string]string{
		"zero as a number": "0",
		"zero as duration": `"0s"`,
		"number":           "4000",
		"fractional":       "2.5",
		"duration string":  `"2s"`,
	} {
		if err := validateConfig(t, compiled, llm(timeout)); err != nil {
			t.Errorf("llm timeout (%s) must be valid — the decoder accepts it and normalized() defaults zero, got: %v", name, err)
		}
	}

	for name, timeout := range map[string]string{
		"negative number":   "-1",
		"negative duration": `"-1s"`,
		"unitless string":   `"4000"`,
	} {
		if err := validateConfig(t, compiled, llm(timeout)); err == nil {
			t.Errorf("llm timeout (%s) must be rejected", name)
		}
	}
}

// TestSchemaComplexityLLMFallbackRequiresLLMBlock pins the schema to
// ComplexityAnalyzerConfig.Validate, which rejects a semantic block selecting the
// "llm" fallback with no llm block to run it. The conditional has to read the
// nested semantic.fallback and require both keys: an `if` that merely names a
// property passes vacuously when the property is absent, which would demand an
// llm block from every analyzer config that never mentioned a fallback.
func TestSchemaComplexityLLMFallbackRequiresLLMBlock(t *testing.T) {
	compiled := compileSchema(t)
	analyzer := func(body string) string {
		fields := `"keywords": {"simple_keywords": ["hi"], "medium_keywords": ["build"], "complex_keywords": ["design a system"]}`
		if body != "" {
			fields += ", " + body
		}
		return `{"governance": {"complexity_analyzer_config": {` + fields + `}}}`
	}
	const semanticBase = `"provider": "openai", "embedding_model": "text-embedding-3-small"`
	const llmBlock = `"llm": {"provider": "openai", "model": "gpt-4.1-mini"}`

	valid := map[string]string{
		"llm fallback with its classifier":   `"semantic": {` + semanticBase + `, "fallback": "llm"}, ` + llmBlock,
		"fallback none without an llm block": `"semantic": {` + semanticBase + `, "fallback": "none"}`,
		// Validate() lets an llm block sit dormant so toggling the fallback back on
		// does not lose it.
		"dormant llm block under fallback none": `"semantic": {` + semanticBase + `, "fallback": "none"}, ` + llmBlock,
		// The vacuous-truth guards: neither of these names a fallback, so the
		// conditional must not fire.
		"semantic block with no fallback key": `"semantic": {` + semanticBase + `}`,
		"no semantic block at all":            `"llm": {"provider": "openai", "model": "gpt-4.1-mini"}`,
		"keywords only":                       "",
	}
	for name, body := range valid {
		if err := validateConfig(t, compiled, analyzer(body)); err != nil {
			t.Errorf("%s must be valid — the loader accepts it, got: %v", name, err)
		}
	}

	missing := analyzer(`"semantic": {` + semanticBase + `, "fallback": "llm"}`)
	if err := validateConfig(t, compiled, missing); err == nil {
		t.Error("an llm fallback with no llm block must be rejected: Validate reports \"requires an llm config block\"")
	}
}

// TestSchemaBudgetQuarterStartMonth pins the schema to the value range the Go
// layer actually accepts. BudgetResetConfig.QuarterStartMonth is a plain int, so
// an omitted quarter_start_month and an explicit 0 are indistinguishable after
// unmarshal - validateBudget therefore has to treat 0 as "unset", and the schema
// has to let that value through or config.json rejects a value the runtime
// documents as valid and handles correctly.
func TestSchemaBudgetQuarterStartMonth(t *testing.T) {
	compiled := compileSchema(t)

	budgetConfig := func(resetConfig string) string {
		return `{
			"governance": {
				"budgets": [{
					"id": "b-1",
					"max_limit": 100,
					"reset_duration": "1Q",
					"reset_config": ` + resetConfig + `
				}]
			}
		}`
	}

	t.Run("explicit zero is accepted as the unset value", func(t *testing.T) {
		if err := validateConfig(t, compiled, budgetConfig(`{"quarter_start_month": 0}`)); err != nil {
			t.Errorf("quarter_start_month 0 is documented as January and accepted by validateBudget, so the schema must accept it, got: %v", err)
		}
	})

	t.Run("an omitted quarter start is accepted", func(t *testing.T) {
		if err := validateConfig(t, compiled, budgetConfig(`{}`)); err != nil {
			t.Errorf("an empty reset_config should be valid, got: %v", err)
		}
	})

	t.Run("the 1-12 range still validates", func(t *testing.T) {
		for _, month := range []int{1, 4, 7, 10, 12} {
			cfg := budgetConfig(fmt.Sprintf(`{"quarter_start_month": %d}`, month))
			if err := validateConfig(t, compiled, cfg); err != nil {
				t.Errorf("quarter_start_month %d should be valid, got: %v", month, err)
			}
		}
	})

	t.Run("out of range months are still rejected", func(t *testing.T) {
		for _, month := range []int{-1, 13} {
			cfg := budgetConfig(fmt.Sprintf(`{"quarter_start_month": %d}`, month))
			if err := validateConfig(t, compiled, cfg); err == nil {
				t.Errorf("quarter_start_month %d is outside 1-12 and must be rejected", month)
			}
		}
	})

	// $defs/budget_line is a second copy of the same shape, reached through
	// customers, access profiles and provider configs. A budget written there
	// deserializes into the same Go struct, so the two copies have to agree on
	// which values they accept.
	t.Run("the budget_line copy accepts the same range", func(t *testing.T) {
		lineConfig := func(month string) string {
			return `{
				"governance": {
					"customers": [{
						"id": "c-1",
						"name": "Acme",
						"budgets": [{
							"id": "b-1",
							"max_limit": 100,
							"reset_duration": "1Q",
							"reset_config": {"quarter_start_month": ` + month + `}
						}]
					}]
				}
			}`
		}

		if err := validateConfig(t, compiled, lineConfig("0")); err != nil {
			t.Errorf("quarter_start_month 0 must be accepted here exactly as it is under governance.budgets, got: %v", err)
		}
		if err := validateConfig(t, compiled, lineConfig("4")); err != nil {
			t.Errorf("quarter_start_month 4 should be valid, got: %v", err)
		}
		if err := validateConfig(t, compiled, lineConfig("13")); err == nil {
			t.Error("quarter_start_month 13 is outside 1-12 and must be rejected")
		}
	})
}

// TestSchemaResetConfigRequiresQuarterlyDuration pins the quarterly-only rule the
// reset_config description already states. validateResetConfig rejects a quarter
// definition on any non-quarterly duration, so without this constraint a
// config.json passes schema validation and only fails later at reconciliation -
// the slowest possible place to learn the file is wrong.
func TestSchemaResetConfigRequiresQuarterlyDuration(t *testing.T) {
	compiled := compileSchema(t)

	// governance.budgets and $defs/budget_line are separate copies of the same
	// shape, so both need the constraint and both are checked here.
	scopes := map[string]func(string) string{
		"governance.budgets": func(budget string) string {
			return `{"governance": {"budgets": [` + budget + `]}}`
		},
		"$defs/budget_line via customers": func(budget string) string {
			return `{"governance": {"customers": [{"id": "c-1", "name": "Acme", "budgets": [` + budget + `]}]}}`
		},
	}

	for name, wrap := range scopes {
		t.Run(name, func(t *testing.T) {
			quarterly := `{"id": "b-1", "max_limit": 100, "reset_duration": "1Q", "reset_config": {"quarter_start_month": 4}}`
			if err := validateConfig(t, compiled, wrap(quarterly)); err != nil {
				t.Errorf("reset_config on a 1Q budget must stay valid, got: %v", err)
			}

			for _, duration := range []string{"1M", "1h", "1Y"} {
				cfg := wrap(`{"id": "b-1", "max_limit": 100, "reset_duration": "` + duration + `", "reset_config": {"quarter_start_month": 4}}`)
				if err := validateConfig(t, compiled, cfg); err == nil {
					t.Errorf("reset_config with reset_duration %q must be rejected: validateResetConfig refuses it at reconciliation", duration)
				}
			}

			// A quarter definition with no duration at all is the same mistake with
			// the evidence missing, so it has to be rejected too.
			orphan := wrap(`{"id": "b-1", "max_limit": 100, "reset_config": {"quarter_start_month": 4}}`)
			if err := validateConfig(t, compiled, orphan); err == nil {
				t.Error("reset_config with no reset_duration must be rejected")
			}

			// Budgets without a quarter definition are unaffected by the constraint.
			plain := wrap(`{"id": "b-1", "max_limit": 100, "reset_duration": "1M"}`)
			if err := validateConfig(t, compiled, plain); err != nil {
				t.Errorf("a budget with no reset_config must stay valid on any duration, got: %v", err)
			}
		})
	}
}

// TestSchemaVectorStoreTypes pins the schema's store list to what NewVectorStore
// can actually construct. The two drifted once already: chromem became a
// selectable store type in the framework while the schema still listed four,
// so a config the loader accepts read as invalid to every editor pointed at the
// published schema.
func TestSchemaVectorStoreTypes(t *testing.T) {
	// Each supported type and the $defs section that describes its config block.
	wantTypes := map[vectorstore.VectorStoreType]string{
		vectorstore.VectorStoreTypeWeaviate: "weaviate_config",
		vectorstore.VectorStoreTypeRedis:    "redis_config",
		vectorstore.VectorStoreTypeQdrant:   "qdrant_config",
		vectorstore.VectorStoreTypePinecone: "pinecone_config",
		vectorstore.VectorStoreTypeChromem:  "chromem_config",
	}

	schema := loadSchema(t)
	rawEnum, found := navigateJSON(schema, "properties", "vector_store", "properties", "type", "enum")
	if !found {
		t.Fatal("vector_store.type is missing its enum")
	}
	enum, ok := rawEnum.([]interface{})
	if !ok {
		t.Fatalf("vector_store.type enum is not a list, got %T", rawEnum)
	}
	listed := make(map[string]bool, len(enum))
	for _, entry := range enum {
		value, ok := entry.(string)
		if !ok {
			t.Fatalf("vector_store.type enum holds a non-string entry %v", entry)
		}
		listed[value] = true
	}

	for storeType, configDef := range wantTypes {
		t.Run(string(storeType), func(t *testing.T) {
			if !listed[string(storeType)] {
				t.Errorf("vector_store.type enum is missing %q — NewVectorStore constructs this type", storeType)
			}
			if _, found := navigateJSON(schema, "$defs", configDef); !found {
				t.Errorf("$defs is missing %q — %q has no config schema", configDef, storeType)
			}
		})
	}

	// The reverse direction: a type the schema offers but the loader rejects
	// sends operators down a path that fails at boot.
	for value := range listed {
		if _, ok := wantTypes[vectorstore.VectorStoreType(value)]; !ok {
			t.Errorf("vector_store.type enum offers %q, which NewVectorStore cannot construct", value)
		}
	}

	// The backend config block is only meaningful next to the type that owns it.
	// Accepting a mismatched pair means the schema validates a config the loader
	// will reject at boot, which is the failure mode a schema exists to prevent.
	t.Run("config block must match the declared type", func(t *testing.T) {
		compiled := compileSchema(t)

		valid := map[string]string{
			"weaviate": `{"vector_store": {"enabled": true, "type": "weaviate", "config": {"scheme": "http", "host": "localhost:8080"}}}`,
			"redis":    `{"vector_store": {"enabled": true, "type": "redis", "config": {"addr": "localhost:6379"}}}`,
			"qdrant":   `{"vector_store": {"enabled": true, "type": "qdrant", "config": {"host": "localhost"}}}`,
			"pinecone": `{"vector_store": {"enabled": true, "type": "pinecone", "config": {"api_key": "k", "index_host": "h"}}}`,
			"chromem":  `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "/app/data/chromem"}}}`,
		}
		for name, config := range valid {
			if err := validateConfig(t, compiled, config); err != nil {
				t.Errorf("%s paired with its own config must be valid, got: %v", name, err)
			}
		}

		mismatched := map[string]string{
			// chromem_config requires nothing, so an empty object satisfies it. A
			// type-blind branch lets that stand in for every other backend and
			// waves through a redis store with no addr to dial.
			"redis with an empty config":    `{"vector_store": {"enabled": true, "type": "redis", "config": {}}}`,
			"redis with a chromem config":   `{"vector_store": {"enabled": true, "type": "redis", "config": {"path": "/app/data/chromem"}}}`,
			"weaviate with a redis config":  `{"vector_store": {"enabled": true, "type": "weaviate", "config": {"addr": "localhost:6379"}}}`,
			"qdrant with a pinecone config": `{"vector_store": {"enabled": true, "type": "qdrant", "config": {"api_key": "k", "index_host": "h"}}}`,
			"pinecone with a qdrant config": `{"vector_store": {"enabled": true, "type": "pinecone", "config": {"host": "localhost"}}}`,
			"chromem with a redis config":   `{"vector_store": {"enabled": true, "type": "chromem", "config": {"addr": "localhost:6379"}}}`,
			"weaviate with an empty config": `{"vector_store": {"enabled": true, "type": "weaviate", "config": {}}}`,
			"pinecone with an empty config": `{"vector_store": {"enabled": true, "type": "pinecone", "config": {}}}`,
		}
		for name, config := range mismatched {
			if err := validateConfig(t, compiled, config); err == nil {
				t.Errorf("%s must be rejected: the config block belongs to a different backend", name)
			}
		}
	})

	// NewVectorStore rejects a nil config for every service-backed type
	// ("<type> config is required"), because each needs an endpoint to dial.
	// Chromem is the sole exception: a nil config there means memory-only mode.
	t.Run("service-backed types require a config block", func(t *testing.T) {
		compiled := compileSchema(t)

		for _, storeType := range []string{"weaviate", "redis", "qdrant", "pinecone"} {
			omitted := `{"vector_store": {"enabled": true, "type": "` + storeType + `"}}`
			if err := validateConfig(t, compiled, omitted); err == nil {
				t.Errorf("%s with no config block must be rejected: it has no endpoint to dial", storeType)
			}
		}

		// The negative control lives in "chromem config validates" below, which
		// pins the memory-only form as valid.
	})

	t.Run("chromem config validates", func(t *testing.T) {
		compiled := compileSchema(t)
		persistent := `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "/app/data/chromem", "compress": false}}}`
		if err := validateConfig(t, compiled, persistent); err != nil {
			t.Errorf("a persistent chromem store must be valid, got: %v", err)
		}
		// Path is optional: an empty config is chromem's memory-only mode.
		memoryOnly := `{"vector_store": {"enabled": true, "type": "chromem"}}`
		if err := validateConfig(t, compiled, memoryOnly); err != nil {
			t.Errorf("a memory-only chromem store must be valid, got: %v", err)
		}
		unknownField := `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "/app/data/chromem", "collection": "x"}}}`
		if err := validateConfig(t, compiled, unknownField); err == nil {
			t.Error("an unknown chromem config field must be rejected: ChromemConfig defines only path and compress")
		}
		// compress only reaches chromem.NewPersistentDB, so enabling it without a
		// path silently does nothing — reject it rather than let an operator
		// believe their memory-only store is compressed.
		compressNoPath := `{"vector_store": {"enabled": true, "type": "chromem", "config": {"compress": true}}}`
		if err := validateConfig(t, compiled, compressNoPath); err == nil {
			t.Error("compress without path must be rejected: compression only applies to a persistent store")
		}
		compressEmptyPath := `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "", "compress": true}}}`
		if err := validateConfig(t, compiled, compressEmptyPath); err == nil {
			t.Error("compress with an empty path must be rejected: an empty path is memory-only mode")
		}
		compressWithPath := `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "/app/data/chromem", "compress": true}}}`
		if err := validateConfig(t, compiled, compressWithPath); err != nil {
			t.Errorf("compress alongside a path must be valid, got: %v", err)
		}
		// newChromemStore trims the path before deciding between the persistent
		// and memory-only db, so a blank path is memory-only however it is
		// spelled. Both compression settings must reject it: writing one reads
		// as a persistent store that silently never persists.
		blankPaths := map[string]string{
			"empty, no compress":          `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": ""}}}`,
			"whitespace, no compress":     `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "   "}}}`,
			"empty, compress false":       `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "", "compress": false}}}`,
			"whitespace, compress false":  `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": " \t ", "compress": false}}}`,
			"whitespace, compress true":   `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "   ", "compress": true}}}`,
			"newline only, compress true": `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "\n", "compress": true}}}`,
		}
		for name, config := range blankPaths {
			if err := validateConfig(t, compiled, config); err == nil {
				t.Errorf("a blank chromem path (%s) must be rejected: omit path for memory-only mode", name)
			}
		}
		// The rejection must not spill onto real paths under either setting.
		for name, config := range map[string]string{
			"compress false": `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "/app/data/chromem", "compress": false}}}`,
			"compress true":  `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "./relative path/chromem", "compress": true}}}`,
		} {
			if err := validateConfig(t, compiled, config); err != nil {
				t.Errorf("a non-blank chromem path (%s) must stay valid, got: %v", name, err)
			}
		}
	})
}

// TestSchemaGovernanceProjectsValidation pins the constraints a governance.projects declaration
// has to satisfy before the reconciler sees it: the two policies that cannot combine, the model
// tier the provider's own budget already covers, a rate limit that would enforce nothing, negative
// caps, and a members list the file cannot declare.
func TestSchemaGovernanceProjectsValidation(t *testing.T) {
	compiled := compileSchema(t)

	tests := []struct {
		name      string
		project   string
		wantError bool
	}{
		{
			name: "a full declaration is valid",
			project: `{
				"name": "atlas", "access_rule": "union", "split_policy": "equal",
				"budgets": [{"max_limit": 100, "reset_duration": "1M"}],
				"rate_limit": {"request_max_limit": 10, "request_reset_duration": "1m"},
				"provider_configs": [{"provider_name": "openai", "allowed_models": ["gpt-4o"],
					"budgets": [{"max_limit": 40, "reset_duration": "1d"}],
					"model_budgets": [{"model_name": "gpt-4o", "budgets": [{"max_limit": 5, "reset_duration": "1d"}]}]}],
				"mcp_configs": [{"mcp_client_name": "github", "tools_to_execute": ["*"]}]
			}`,
		},
		{name: "an open project cannot split equally", project: `{"name": "p", "access_rule": "union", "membership_mode": "open", "split_policy": "equal"}`, wantError: true},
		{name: "an open project with no split is valid", project: `{"name": "p", "access_rule": "union", "membership_mode": "open", "split_policy": "none"}`},
		{name: "an open project with the default split is valid", project: `{"name": "p", "access_rule": "union", "membership_mode": "open"}`},
		{name: "the wildcard model tier is rejected", project: `{"name": "p", "access_rule": "union", "provider_configs": [{"provider_name": "openai", "model_budgets": [{"model_name": "*"}]}]}`, wantError: true},
		{name: "an empty rate limit is rejected", project: `{"name": "p", "access_rule": "union", "rate_limit": {}}`, wantError: true},
		{name: "a token cap without its window is rejected", project: `{"name": "p", "access_rule": "union", "rate_limit": {"token_max_limit": 1000}}`, wantError: true},
		{name: "a request cap without its window is rejected", project: `{"name": "p", "access_rule": "union", "rate_limit": {"request_max_limit": 10, "token_reset_duration": "1h"}}`, wantError: true},
		{name: "one complete pair is a valid rate limit", project: `{"name": "p", "access_rule": "union", "rate_limit": {"token_max_limit": 1000, "token_reset_duration": "1h"}}`},
		{name: "a negative budget cap is rejected", project: `{"name": "p", "access_rule": "union", "budgets": [{"max_limit": -1, "reset_duration": "1M"}]}`, wantError: true},
		{name: "a negative token cap is rejected", project: `{"name": "p", "access_rule": "union", "rate_limit": {"token_max_limit": -1, "token_reset_duration": "1h"}}`, wantError: true},
		{name: "a negative request cap is rejected", project: `{"name": "p", "access_rule": "union", "rate_limit": {"request_max_limit": -1, "request_reset_duration": "1m"}}`, wantError: true},
		{name: "a members list cannot be declared", project: `{"name": "p", "access_rule": "union", "members": [{"user_id": "u-1"}]}`, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := fmt.Sprintf(`{"governance": {"projects": [%s]}}`, tt.project)
			err := validateConfig(t, compiled, config)
			if tt.wantError && err == nil {
				t.Fatal("config should be invalid")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("config should be valid, got: %v", err)
			}
		})
	}
}

// copilotKeyConfig builds a github-copilot provider config with the given key body.
func copilotKeyConfig(keyBody string) string {
	return fmt.Sprintf(`{
		"providers": {
			"github-copilot": {
				"keys": [{%s}]
			}
		}
	}`, keyBody)
}

// TestSchemaGithubCopilotCredentialRequired pins that the schema demands one of the two
// credential forms. core/utils.go rejects a key carrying neither, and the schema is the
// source of truth for config fields, so it has to reject the same shape rather than
// deferring the failure to boot.
func TestSchemaGithubCopilotCredentialRequired(t *testing.T) {
	compiled := compileSchema(t)

	const appConfig = `"github_copilot_key_config": {
		"app_id": "123456",
		"installation_id": "87654321",
		"repository_id": "999000111",
		"private_key": "pem"
	}`

	valid := []struct {
		name   string
		config string
	}{
		{
			name:   "direct token alone",
			config: copilotKeyConfig(`"name": "k", "value": "tid=abc", "models": ["*"], "weight": 1.0`),
		},
		{
			name:   "github app config alone",
			config: copilotKeyConfig(`"name": "k", "models": ["*"], "weight": 1.0, ` + appConfig),
		},
		{
			name:   "both forms together",
			config: copilotKeyConfig(`"name": "k", "value": "tid=abc", "models": ["*"], "weight": 1.0, ` + appConfig),
		},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateConfig(t, compiled, tt.config); err != nil {
				t.Fatalf("config should be valid: %v", err)
			}
		})
	}

	invalid := []struct {
		name   string
		config string
	}{
		{
			name:   "neither credential form",
			config: copilotKeyConfig(`"name": "k", "models": ["*"], "weight": 1.0`),
		},
		{
			name:   "empty token with no app config",
			config: copilotKeyConfig(`"name": "k", "value": "", "models": ["*"], "weight": 1.0`),
		},
		{
			name: "app config missing the private key",
			config: copilotKeyConfig(`"name": "k", "models": ["*"], "weight": 1.0,
				"github_copilot_key_config": {"app_id": "1", "installation_id": "2", "repository_id": "3"}`),
		},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateConfig(t, compiled, tt.config); err == nil {
				t.Fatal("config should be invalid")
			}
		})
	}
}

// TestSchemaAccessProfileMCPGrants covers the access-profile MCP keys. The
// schema drifted from the Go struct once already: the profile grant model was
// renamed to virtual_mcps / mcp_configs while the schema still declared only the
// retired spellings under additionalProperties:false, so every GitOps file that
// used the current keys failed validation.
func TestSchemaAccessProfileMCPGrants(t *testing.T) {
	profileConfig := func(body string) string {
		return fmt.Sprintf(`{"access_profiles": [{"name": "platform-default", %s}]}`, body)
	}

	t.Run("access_profile def declares virtual_mcps and mcp_configs", func(t *testing.T) {
		schema := loadSchema(t)
		for _, key := range []string{"virtual_mcps", "mcp_configs"} {
			if _, found := navigateJSON(schema, "$defs", "access_profile", "properties", key); !found {
				t.Errorf("$defs/access_profile is missing %q — TableAccessProfile serializes this field", key)
			}
		}
	})

	t.Run("virtual_mcps assignment validates", func(t *testing.T) {
		compiled := compileSchema(t)
		if err := validateConfig(t, compiled, profileConfig(`"virtual_mcps": [{"virtual_mcp_id": 1}]`)); err != nil {
			t.Errorf("virtual_mcps assignment should be valid, got: %v", err)
		}
	})

	t.Run("mcp_configs allowlist validates", func(t *testing.T) {
		compiled := compileSchema(t)
		body := `"mcp_configs": [{"mcp_client_id": "github", "tools_to_execute": ["create_pull_request"]}]`
		if err := validateConfig(t, compiled, profileConfig(body)); err != nil {
			t.Errorf("mcp_configs allowlist should be valid, got: %v", err)
		}
	})

	t.Run("mcp_configs allow-all and deny-all allowlists validate", func(t *testing.T) {
		compiled := compileSchema(t)
		for _, tools := range []string{`["*"]`, `[]`} {
			body := fmt.Sprintf(`"mcp_configs": [{"mcp_client_id": "github", "tools_to_execute": %s}]`, tools)
			if err := validateConfig(t, compiled, profileConfig(body)); err != nil {
				t.Errorf("tools_to_execute %s should be valid, got: %v", tools, err)
			}
		}
	})

	t.Run("virtual_mcps entry missing virtual_mcp_id rejected", func(t *testing.T) {
		compiled := compileSchema(t)
		if err := validateConfig(t, compiled, profileConfig(`"virtual_mcps": [{}]`)); err == nil {
			t.Error("virtual_mcps entry without virtual_mcp_id must be rejected")
		}
	})

	t.Run("virtual_mcps entry still rejects the retired tool_group_id key", func(t *testing.T) {
		compiled := compileSchema(t)
		if err := validateConfig(t, compiled, profileConfig(`"virtual_mcps": [{"tool_group_id": 1}]`)); err == nil {
			t.Error("virtual_mcps entry must not accept the retired tool_group_id spelling")
		}
	})

	t.Run("deprecated grant keys still validate for backward compatibility", func(t *testing.T) {
		compiled := compileSchema(t)
		body := `"mcp_tool_groups": [{"tool_group_id": 1}],
			"mcp_servers": [{"mcp_server_id": "github"}],
			"mcp_tool_overrides": [{"mcp_client_id": "github", "tool_name": "create_pull_request", "action": "include"}]`
		if err := validateConfig(t, compiled, profileConfig(body)); err != nil {
			t.Errorf("deprecated access-profile grant keys should still validate, got: %v", err)
		}
	})
}

func TestSchemaVirtualMCPByName(t *testing.T) {
	compiled := compileSchema(t)

	profile := func(body string) string {
		return fmt.Sprintf(`{"access_profiles": [{"name": "p", "virtual_mcps": [%s]}]}`, body)
	}
	project := func(body string) string {
		return fmt.Sprintf(`{"governance": {"projects": [{"name": "proj", "access_rule": "union", "virtual_mcps": [%s]}]}}`, body)
	}

	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"name only", `{"virtual_mcp_name": "Platform Tools"}`, true},
		{"id only still valid", `{"virtual_mcp_id": 1}`, true},
		{"both accepted, id wins at load", `{"virtual_mcp_name": "Platform Tools", "virtual_mcp_id": 1}`, true},
		{"neither rejected", `{}`, false},
		{"unknown key rejected", `{"virtual_mcp_slug": "x"}`, false},
		{"id zero rejected", `{"virtual_mcp_id": 0}`, false},
	} {
		t.Run("access_profile/"+tc.name, func(t *testing.T) {
			err := validateConfig(t, compiled, profile(tc.body))
			if tc.valid && err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
			if !tc.valid && err == nil {
				t.Error("expected rejection")
			}
		})
		t.Run("project/"+tc.name, func(t *testing.T) {
			err := validateConfig(t, compiled, project(tc.body))
			if tc.valid && err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
			if !tc.valid && err == nil {
				t.Error("expected rejection")
			}
		})
	}
}

// TestValidateConfigSchema_IssuerURLRequiredForDiscovery pins the schema's own
// statement of the load-time invariant: enabling OAuth discovery without a
// pinned issuer_url is rejected by the schema, not only by validateClientConfig.
func TestValidateConfigSchema_IssuerURLRequiredForDiscovery(t *testing.T) {
	schemaPath := filepath.Join(t.TempDir(), "config.schema.json")
	if err := os.WriteFile(schemaPath, loadLocalSchema(t), 0644); err != nil {
		t.Fatalf("failed to write temp schema: %v", err)
	}
	t.Setenv(ConfigSchemaURLEnv, schemaPath)

	for _, tc := range []struct {
		name    string
		config  string
		wantErr bool
	}{
		{"oauth without issuer_url", `{"client":{"mcp_server_auth_mode":"oauth"}}`, true},
		{"both without issuer_url", `{"client":{"mcp_server_auth_mode":"both","oauth2_server_config":{}}}`, true},
		{"oauth with issuer_url", `{"client":{"mcp_server_auth_mode":"oauth","oauth2_server_config":{"issuer_url":"https://issuer.example.com"}}}`, false},
		{"headers without issuer_url", `{"client":{"mcp_server_auth_mode":"headers"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfigSchema([]byte(tc.config))
			if tc.wantErr && err == nil {
				t.Fatal("expected schema validation to reject discovery without issuer_url")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected config to pass schema validation, got: %v", err)
			}
		})
	}
}

// TestSchemaCodeModeLimitsValueBytes keeps config.schema.json aligned with the server:
// max_value_bytes is 0 (the default) or at least 1024, in both places the limits live.
func TestSchemaCodeModeLimitsValueBytes(t *testing.T) {
	wrap := map[string]func(string) string{
		"client.mcp_code_mode_limits": func(limits string) string {
			return `{"client": {"mcp_code_mode_limits": ` + limits + `}}`
		},
		"mcp.tool_manager_config.code_mode_limits": func(limits string) string {
			return `{"mcp": {"tool_manager_config": {"code_mode_limits": ` + limits + `}}}`
		},
	}
	for where, config := range wrap {
		for _, tc := range []struct {
			limits  string
			wantErr bool
		}{
			{`{}`, false},
			{`{"max_value_bytes": 0}`, false},
			{`{"max_value_bytes": 1024}`, false},
			{`{"max_value_bytes": 1048576}`, false},
			{`{"max_value_bytes": 10}`, true},
			{`{"max_value_bytes": 1023}`, true},
		} {
			err := ValidateConfigSchema([]byte(config(tc.limits)), loadLocalSchema(t))
			if (err != nil) != tc.wantErr {
				t.Errorf("%s %s: err=%v, wantErr=%v", where, tc.limits, err, tc.wantErr)
			}
		}
	}
}

// TestValidateConfigSchema_VirtualKeyMetadata checks that config.schema.json enforces the same
// metadata rules as the configstore validator, including the reserved keys.
func TestValidateConfigSchema_VirtualKeyMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		wantErr  bool
	}{
		{name: "string values", metadata: `{"cost_center": "cc-42", "owner.email": "a@example.com", "env-name": ""}`},
		{name: "empty object", metadata: `{}`},
		{name: "non-string value", metadata: `{"cost_center": 42}`, wantErr: true},
		{name: "key with space", metadata: `{"cost center": "cc-42"}`, wantErr: true},
		{name: "empty key", metadata: `{"": "cc-42"}`, wantErr: true},
		{name: "value too long", metadata: `{"k": "` + strings.Repeat("v", 513) + `"}`, wantErr: true},
		{name: "not an object", metadata: `["cc-42"]`, wantErr: true},
		{name: "reserved key", metadata: `{"isAsyncRequest": "true"}`, wantErr: true},
		{name: "load balancer prefix", metadata: `{"bifrost_alb_provider": "x"}`, wantErr: true},
		{name: "reserved name as a substring", metadata: `{"my_bifrost_alb_x": "v", "isAsyncRequestCount": "1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := `{"governance": {"virtual_keys": [{"id": "vk-1", "name": "Test Virtual Key", "value": "vk_test_123456", "metadata": ` + tt.metadata + `}]}}`
			err := ValidateConfigSchema([]byte(config), loadLocalSchema(t))
			if tt.wantErr && err == nil {
				t.Errorf("expected metadata %s to fail validation", tt.metadata)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected metadata %s to pass validation, got error: %v", tt.metadata, err)
			}
		})
	}
}
