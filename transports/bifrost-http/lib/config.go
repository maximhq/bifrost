// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ClientConfig represents the core configuration for Bifrost HTTP transport and the Bifrost Client.
// It includes settings for excess request handling, Prometheus metrics, and initial pool size.
type ClientConfig struct {
	DropExcessRequests      bool     `json:"drop_excess_requests"`      // Drop excess requests if the provider queue is full
	InitialPoolSize         int      `json:"initial_pool_size"`         // The initial pool size for the bifrost client
	PrometheusLabels        []string `json:"prometheus_labels"`         // The labels to be used for prometheus metrics
	EnableLogging           bool     `json:"enable_logging"`            // Enable logging of requests and responses
	EnableGovernance        bool     `json:"enable_governance"`         // Enable governance on all requests
	EnforceGovernanceHeader bool     `json:"enforce_governance_header"` // Enforce governance on all requests
}

// ProviderConfig represents the configuration for a specific AI model provider.
// It includes API keys, network settings, provider-specific metadata, and concurrency settings.
type ProviderConfig struct {
	Keys                     []schemas.Key                     `json:"keys"`                                  // API keys for the provider with UUIDs
	NetworkConfig            *schemas.NetworkConfig            `json:"network_config,omitempty"`              // Network-related settings
	MetaConfig               *schemas.MetaConfig               `json:"-"`                                     // Provider-specific metadata
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size,omitempty"` // Concurrency settings
	ProxyConfig              *schemas.ProxyConfig              `json:"proxy_config,omitempty"`                // Proxy configuration
	SendBackRawResponse      bool                              `json:"send_back_raw_response"`                // Include raw response in BifrostResponse
}

// ConfigMap maps provider names to their configurations.
type ConfigMap map[schemas.ModelProvider]ProviderConfig

// BifrostHTTPConfig represents the complete configuration structure for Bifrost HTTP transport.
// It includes both provider configurations and MCP configuration.
type BifrostHTTPConfig struct {
	ClientConfig   *ClientConfig      `json:"client"`    // Client configuration
	ProviderConfig ConfigMap          `json:"providers"` // Provider configurations
	MCPConfig      *schemas.MCPConfig `json:"mcp"`       // MCP configuration (optional)
}
