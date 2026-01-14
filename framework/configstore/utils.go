package configstore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// marshalToString marshals the given value to a JSON string.
func marshalToString(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// marshalToStringPtr marshals the given value to a JSON string and returns a pointer to the string.
func marshalToStringPtr(v any) (*string, error) {
	if v == nil {
		return nil, nil
	}
	data, err := marshalToString(v)
	if err != nil {
		return nil, err
	}
	return &data, nil
}

// deepCopy creates a deep copy of a given type
func deepCopy[T any](in T) (T, error) {
	var out T
	b, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

// substituteEnvVars replaces resolved environment variable values with their original env.VAR_NAME references
func substituteEnvVars(config *ProviderConfig, provider schemas.ModelProvider, envKeys map[string][]EnvKeyInfo) {
	// Create a map for quick lookup of env vars by provider and key ID
	envVarMap := make(map[string]string) // key: "provider.keyID.field" -> env var name

	for envVar, keyInfos := range envKeys {
		for _, keyInfo := range keyInfos {
			if keyInfo.Provider == provider {
				// For API keys
				if keyInfo.KeyType == "api_key" {
					envVarMap[fmt.Sprintf("%s.%s.value", provider, keyInfo.KeyID)] = envVar
				}
				// For Azure config
				if keyInfo.KeyType == "azure_config" {
					field := strings.TrimPrefix(keyInfo.ConfigPath, fmt.Sprintf("providers.%s.keys[%s].azure_key_config.", provider, keyInfo.KeyID))
					envVarMap[fmt.Sprintf("%s.%s.azure.%s", provider, keyInfo.KeyID, field)] = envVar
				}
				// For Vertex config
				if keyInfo.KeyType == "vertex_config" {
					field := strings.TrimPrefix(keyInfo.ConfigPath, fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.", provider, keyInfo.KeyID))
					envVarMap[fmt.Sprintf("%s.%s.vertex.%s", provider, keyInfo.KeyID, field)] = envVar
				}
				// For Bedrock config
				if keyInfo.KeyType == "bedrock_config" {
					field := strings.TrimPrefix(keyInfo.ConfigPath, fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.", provider, keyInfo.KeyID))
					envVarMap[fmt.Sprintf("%s.%s.bedrock.%s", provider, keyInfo.KeyID, field)] = envVar
				}
			}
		}
	}

	// Substitute values in keys
	for i, key := range config.Keys {
		keyPrefix := fmt.Sprintf("%s.%s", provider, key.ID)

		// Substitute API key value
		if envVar, exists := envVarMap[fmt.Sprintf("%s.value", keyPrefix)]; exists {
			config.Keys[i].Value = fmt.Sprintf("env.%s", envVar)
		}

		// Substitute Azure config
		if key.AzureKeyConfig != nil {
			if envVar, exists := envVarMap[fmt.Sprintf("%s.azure.endpoint", keyPrefix)]; exists {
				config.Keys[i].AzureKeyConfig.Endpoint = fmt.Sprintf("env.%s", envVar)
			}
			if envVar, exists := envVarMap[fmt.Sprintf("%s.azure.api_version", keyPrefix)]; exists {
				apiVersion := fmt.Sprintf("env.%s", envVar)
				config.Keys[i].AzureKeyConfig.APIVersion = &apiVersion
			}
		}

		// Substitute Vertex config
		if key.VertexKeyConfig != nil {
			if envVar, exists := envVarMap[fmt.Sprintf("%s.vertex.project_id", keyPrefix)]; exists {
				config.Keys[i].VertexKeyConfig.ProjectID = fmt.Sprintf("env.%s", envVar)
			}
			if envVar, exists := envVarMap[fmt.Sprintf("%s.vertex.project_number", keyPrefix)]; exists {
				config.Keys[i].VertexKeyConfig.ProjectNumber = fmt.Sprintf("env.%s", envVar)
			}
			if envVar, exists := envVarMap[fmt.Sprintf("%s.vertex.region", keyPrefix)]; exists {
				config.Keys[i].VertexKeyConfig.Region = fmt.Sprintf("env.%s", envVar)
			}
			if envVar, exists := envVarMap[fmt.Sprintf("%s.vertex.auth_credentials", keyPrefix)]; exists {
				config.Keys[i].VertexKeyConfig.AuthCredentials = fmt.Sprintf("env.%s", envVar)
			}
		}

		// Substitute Bedrock config
		if key.BedrockKeyConfig != nil {
			if envVar, exists := envVarMap[fmt.Sprintf("%s.bedrock.access_key", keyPrefix)]; exists {
				config.Keys[i].BedrockKeyConfig.AccessKey = fmt.Sprintf("env.%s", envVar)
			}
			if envVar, exists := envVarMap[fmt.Sprintf("%s.bedrock.secret_key", keyPrefix)]; exists {
				config.Keys[i].BedrockKeyConfig.SecretKey = fmt.Sprintf("env.%s", envVar)
			}
			if envVar, exists := envVarMap[fmt.Sprintf("%s.bedrock.session_token", keyPrefix)]; exists {
				config.Keys[i].BedrockKeyConfig.SessionToken = &[]string{fmt.Sprintf("env.%s", envVar)}[0]
			}
			if envVar, exists := envVarMap[fmt.Sprintf("%s.bedrock.region", keyPrefix)]; exists {
				config.Keys[i].BedrockKeyConfig.Region = &[]string{fmt.Sprintf("env.%s", envVar)}[0]
			}
			if envVar, exists := envVarMap[fmt.Sprintf("%s.bedrock.arn", keyPrefix)]; exists {
				config.Keys[i].BedrockKeyConfig.ARN = &[]string{fmt.Sprintf("env.%s", envVar)}[0]
			}
		}
	}
}

// substituteMCPEnvVars replaces resolved environment variable values with their original env.VAR_NAME references for MCP config
func substituteMCPEnvVars(config *schemas.MCPConfig, envKeys map[string][]EnvKeyInfo) {
	// Create a map for quick lookup of env vars by MCP client name
	envVarMap := make(map[string]string) // key: "clientName.connection_string" -> env var name

	for envVar, keyInfos := range envKeys {
		for _, keyInfo := range keyInfos {
			// For MCP connection strings
			if keyInfo.KeyType == "connection_string" {
				// Extract client name from config path like "mcp.client_configs.clientName.connection_string"
				pathParts := strings.Split(keyInfo.ConfigPath, ".")
				if len(pathParts) >= 3 && pathParts[0] == "mcp" && pathParts[1] == "client_configs" {
					clientName := pathParts[2]
					envVarMap[fmt.Sprintf("%s.connection_string", clientName)] = envVar
				}
			}
			// For MCP headers
			if keyInfo.KeyType == "mcp_header" {
				// Extract client name and header name from config path like "mcp.client_configs.clientName.headers.headerName"
				pathParts := strings.Split(keyInfo.ConfigPath, ".")
				if len(pathParts) >= 5 && pathParts[0] == "mcp" && pathParts[1] == "client_configs" && pathParts[3] == "headers" {
					clientName := pathParts[2]
					headerName := pathParts[4]
					envVarMap[fmt.Sprintf("%s.headers.%s", clientName, headerName)] = envVar
				}
			}
		}
	}

	// Substitute values in MCP client configs
	for i, clientConfig := range config.ClientConfigs {
		clientPrefix := clientConfig.Name

		// Substitute connection string
		if clientConfig.ConnectionString != nil {
			if envVar, exists := envVarMap[fmt.Sprintf("%s.connection_string", clientPrefix)]; exists {
				config.ClientConfigs[i].ConnectionString = &[]string{fmt.Sprintf("env.%s", envVar)}[0]
			}
		}

		// Substitute headers
		if clientConfig.Headers != nil {
			for header := range clientConfig.Headers {
				if envVar, exists := envVarMap[fmt.Sprintf("%s.headers.%s", clientPrefix, header)]; exists {
					clientConfig.Headers[header] = fmt.Sprintf("env.%s", envVar)
				}
			}
		}
	}
}

// substituteMCPClientEnvVars replaces resolved environment variable values with their original env.VAR_NAME references for a single MCP client config
// If existingHeaders is provided, it will restore redacted plain header values from the existing headers before substitution
func substituteMCPClientEnvVars(clientConfig *schemas.MCPClientConfig, envKeys map[string][]EnvKeyInfo, existingHeaders map[string]string) {
	// First, restore redacted plain header values from existing headers if provided
	// This handles the case where UI sends redacted headers that aren't env vars
	if existingHeaders != nil && clientConfig.Headers != nil {
		for header, value := range clientConfig.Headers {
			// Check if the value is redacted (contains **** pattern) and not an env var
			if strings.Contains(value, "****") && !strings.HasPrefix(value, "env.") {
				// If header exists in existing headers and wasn't an env var, restore it
				if oldHeaderValue, exists := existingHeaders[header]; exists {
					if !strings.HasPrefix(oldHeaderValue, "env.") {
						clientConfig.Headers[header] = oldHeaderValue
					}
				}
			}
		}
	}

	// Find the environment variable for this client's connection string and headers
	for envVar, keyInfos := range envKeys {
		for _, keyInfo := range keyInfos {
			// For MCP connection strings
			if keyInfo.KeyType == "connection_string" {
				// Extract client ID from config path like "mcp.client_configs.clientID.connection_string"
				pathParts := strings.Split(keyInfo.ConfigPath, ".")
				if len(pathParts) >= 3 && pathParts[0] == "mcp" && pathParts[1] == "client_configs" {
					clientID := pathParts[2]
					// If this environment variable is for the current client (match by ID)
					if clientID == clientConfig.ID && clientConfig.ConnectionString != nil {
						clientConfig.ConnectionString = &[]string{fmt.Sprintf("env.%s", envVar)}[0]
					}
				}
			}
			// For MCP headers
			if keyInfo.KeyType == "mcp_header" {
				// Extract client ID and header name from config path like "mcp.client_configs.clientID.headers.headerName"
				pathParts := strings.Split(keyInfo.ConfigPath, ".")
				if len(pathParts) >= 5 && pathParts[0] == "mcp" && pathParts[1] == "client_configs" && pathParts[3] == "headers" {
					clientID := pathParts[2]
					headerName := pathParts[4]
					// If this environment variable is for the current client (match by ID)
					if clientID == clientConfig.ID && clientConfig.Headers != nil {
						if headerValue, exists := clientConfig.Headers[headerName]; exists {
							// If it's already in env.VAR format, update to use the correct env var
							if strings.HasPrefix(headerValue, "env.") {
								clientConfig.Headers[headerName] = fmt.Sprintf("env.%s", envVar)
							} else if strings.Contains(headerValue, "****") {
								// If it's redacted (contains ****), restore to env.VAR format
								// This handles the case where UI sends redacted headers back for env vars
								clientConfig.Headers[headerName] = fmt.Sprintf("env.%s", envVar)
							}
							// If it's a plain value (not env. and not redacted), leave it as-is
						}
					}
				}
			}
		}
	}
}
