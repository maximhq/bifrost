package main

// SpecialColumns defines column values that need specific formats or valid enum values
// Map structure: table_name -> column_name -> value
var SpecialColumns = map[string]map[string]string{
	// governance_budgets: reset_duration must be a valid duration string
	"governance_budgets": {
		"reset_duration": "1d",
	},

	// governance_rate_limits: duration columns need valid duration format
	"governance_rate_limits": {
		"token_reset_duration":   "1h",
		"request_reset_duration": "1h",
	},

	// oauth_configs: status is an enum-like field
	"oauth_configs": {
		"status":       "pending",
		"redirect_uri": "https://example.com/callback",
	},

	// oauth_tokens: token_type must be valid
	"oauth_tokens": {
		"token_type": "Bearer",
	},

	// logs: status and object_type are enum-like
	"logs": {
		"status":      "success",
		"object_type": "chat_completion",
	},

	// mcp_tool_logs: status is enum-like
	"mcp_tool_logs": {
		"status": "success",
	},

	// routing_rules: scope is enum-like, cel_expression needs valid syntax
	"routing_rules": {
		"scope":          "global",
		"cel_expression": "true",
	},

	// config_mcp_clients: connection_type and auth_type are enum-like
	"config_mcp_clients": {
		"connection_type": "sse",
		"auth_type":       "none",
	},

	// config_providers: provider name must be valid
	"config_providers": {
		"name": "openai",
	},

	// config_plugins: is_custom should be false (true requires a valid path)
	"config_plugins": {
		"is_custom": "false",
	},

	// governance_virtual_keys: is_active should be a boolean
	"governance_virtual_keys": {
		"is_active": "true",
	},

	// config_keys: provider must be valid
	"config_keys": {
		"provider": "openai",
	},

	// governance_virtual_key_provider_configs: provider must be valid
	"governance_virtual_key_provider_configs": {
		"provider": "openai",
	},

	// config_log_store: type must be valid
	"config_log_store": {
		"type": "postgres",
	},

	// config_vector_store: type must be valid
	"config_vector_store": {
		"type": "redis",
	},
}

// SkipTables lists tables that should be skipped during faker data generation
// These are typically system/migration tracking tables or tables that
// get populated automatically by the application
var SkipTables = []string{
	// Migration tracking tables
	"gorp_migrations",
	"schema_migrations",
	"migrations",

	// Tables that are auto-populated or system-managed
	"governance_config",        // Key-value config populated by app
	"governance_model_pricing", // Pricing data populated by sync
	"framework_configs",        // Single row config managed by app

	// Join tables that need both FK values to exist
	"governance_virtual_key_provider_config_keys",
}

// RequiredColumnsOverride specifies columns that MUST have a value even if nullable
// This helps ensure data integrity for testing
var RequiredColumnsOverride = map[string][]string{
	"governance_virtual_keys": {"name", "value"},
	"config_keys":             {"name", "key_id", "value"},
	"config_providers":        {"name"},
	"governance_budgets":      {"max_limit", "reset_duration"},
	"routing_rules":           {"name", "cel_expression", "provider", "scope"},
}

// JSONArrayColumns lists columns that should have JSON array default instead of object
var JSONArrayColumns = map[string][]string{
	"config_mcp_clients": {
		"tools_to_execute_json",
		"tools_to_auto_execute_json",
	},
	"governance_virtual_key_mcp_configs": {
		"tools_to_execute",
	},
	"governance_virtual_key_provider_configs": {
		"allowed_models",
	},
	"config_keys": {
		"models_json",
	},
	"routing_rules": {
		"fallbacks",
	},
	"oauth_configs": {
		"scopes",
	},
	"oauth_tokens": {
		"scopes",
	},
}

// GetJSONDefault returns the appropriate JSON default for a column
func GetJSONDefault(table, column string) string {
	if cols, ok := JSONArrayColumns[table]; ok {
		for _, c := range cols {
			if c == column {
				return "[]"
			}
		}
	}
	return "{}"
}

// IsSkippedTable returns true if the table should be skipped
func IsSkippedTable(tableName string) bool {
	for _, skip := range SkipTables {
		if skip == tableName {
			return true
		}
	}
	return false
}

// GetSpecialValue returns a special value for a column if one is defined
func GetSpecialValue(table, column string) (string, bool) {
	if tableSpecials, ok := SpecialColumns[table]; ok {
		if val, ok := tableSpecials[column]; ok {
			return val, true
		}
	}
	return "", false
}

// RowsPerTableOverride specifies how many rows to generate for specific tables
// Default is 2 rows per table
var RowsPerTableOverride = map[string]int{
	// Tables that typically have single config row
	"config_log_store":    1,
	"config_vector_store": 1,
	"config_hashes":       1,

	// Tables that benefit from more test data
	"logs":          3,
	"mcp_tool_logs": 2,
}

// GetRowsForTable returns the number of rows to generate for a table
func GetRowsForTable(tableName string, defaultRows int) int {
	if override, ok := RowsPerTableOverride[tableName]; ok {
		return override
	}
	return defaultRows
}

// NullableJSONColumns lists columns that should be NULL instead of empty JSON
// These are columns where an empty {} or [] triggers validation errors
var NullableJSONColumns = map[string][]string{
	"config_providers": {
		"custom_provider_config_json", // Requires base_provider_type if set
		"network_config_json",
		"concurrency_buffer_json",
		"proxy_config_json",
	},
	"config_keys": {
		"azure_deployments_json",
		"vertex_deployments_json",
		"bedrock_deployments_json",
		"bedrock_batch_s3_config_json",
	},
	"config_mcp_clients": {
		"stdio_config_json",
		"tool_pricing_json",
		"mcp_client_config_json", // OAuth related
	},
	"config_plugins": {
		"path",       // Custom plugin path - should be NULL if is_custom=false
		"config_json", // Plugin config - keep null for simplicity
	},
	"governance_teams": {
		"profile", // JSON fields that might have validation
		"config",
		"claims",
	},
	"governance_customers": {
		// Any JSON config columns
	},
	"config_client": {
		"prometheus_labels_json",   // JSON serialized []string
		"allowed_origins_json",     // JSON serialized []string
		"allowed_headers_json",     // JSON serialized []string
		"header_filter_config_json", // JSON serialized config
	},
	"oauth_configs": {
		"mcp_client_config_json",
	},
}

// ShouldBeNull returns true if the column should be NULL instead of a default value
func ShouldBeNull(table, column string) bool {
	if cols, ok := NullableJSONColumns[table]; ok {
		for _, c := range cols {
			if c == column {
				return true
			}
		}
	}
	return false
}
