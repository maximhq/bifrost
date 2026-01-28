package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Plugin configuration
type PluginConfig struct {
	EnableHTTPHooks     bool   `json:"enable_http_hooks"`    // Enable HTTP transport hooks
	EnableLLMHooks      bool   `json:"enable_llm_hooks"`     // Enable LLM hooks
	EnableMCPHooks      bool   `json:"enable_mcp_hooks"`     // Enable MCP hooks
	EnableObservability bool   `json:"enable_observability"` // Enable observability/trace injection
	EnableLogging       bool   `json:"enable_logging"`       // Enable detailed logging
	TrackRequests       bool   `json:"track_requests"`       // Track request count
	InjectUptime        bool   `json:"inject_uptime"`        // Inject server uptime in system messages
	CustomHeaderPrefix  string `json:"custom_header_prefix"` // Custom prefix for plugin headers
}

var (
	// Default configuration
	pluginConfig = &PluginConfig{
		EnableHTTPHooks:     true,
		EnableLLMHooks:      true,
		EnableMCPHooks:      true,
		EnableObservability: true,
		EnableLogging:       true,
		TrackRequests:       true,
		InjectUptime:        true,
		CustomHeaderPrefix:  "X-Multi-Plugin",
	}

	// Plugin state
	requestCount int64
	startTime    time.Time
)

// Init is called when the plugin is loaded (optional)
func Init(config any) error {
	fmt.Println("[Multi-Interface Plugin] Init called")
	startTime = time.Now()

	// Parse configuration
	if configMap, ok := config.(map[string]interface{}); ok {
		if enableHTTP, ok := configMap["enable_http_hooks"].(bool); ok {
			pluginConfig.EnableHTTPHooks = enableHTTP
		}
		if enableLLM, ok := configMap["enable_llm_hooks"].(bool); ok {
			pluginConfig.EnableLLMHooks = enableLLM
		}
		if enableMCP, ok := configMap["enable_mcp_hooks"].(bool); ok {
			pluginConfig.EnableMCPHooks = enableMCP
		}
		if enableObs, ok := configMap["enable_observability"].(bool); ok {
			pluginConfig.EnableObservability = enableObs
		}
		if enableLogging, ok := configMap["enable_logging"].(bool); ok {
			pluginConfig.EnableLogging = enableLogging
		}
		if trackReq, ok := configMap["track_requests"].(bool); ok {
			pluginConfig.TrackRequests = trackReq
		}
		if injectUptime, ok := configMap["inject_uptime"].(bool); ok {
			pluginConfig.InjectUptime = injectUptime
		}
		if headerPrefix, ok := configMap["custom_header_prefix"].(string); ok {
			pluginConfig.CustomHeaderPrefix = headerPrefix
		}
	}

	fmt.Printf("[Multi-Interface Plugin] Configuration loaded:\n")
	fmt.Printf("  HTTP Hooks: %v\n", pluginConfig.EnableHTTPHooks)
	fmt.Printf("  LLM Hooks: %v\n", pluginConfig.EnableLLMHooks)
	fmt.Printf("  MCP Hooks: %v\n", pluginConfig.EnableMCPHooks)
	fmt.Printf("  Observability: %v\n", pluginConfig.EnableObservability)
	fmt.Printf("  Request Tracking: %v\n", pluginConfig.TrackRequests)

	return nil
}

// GetName returns the name of the plugin (required)
// This is the system identifier - not editable by users
// Users can set a custom display_name in the config for the UI
func GetName() string {
	return "multi-interface"
}

// ============================================================================
// HTTPTransportPlugin Interface
// ============================================================================

// HTTPTransportPreHook handles HTTP-layer request interception
func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if !pluginConfig.EnableHTTPHooks {
		return nil, nil
	}

	if pluginConfig.EnableLogging {
		fmt.Println("[Multi-Interface Plugin] HTTPTransportPreHook called")
	}

	// Add request tracking (configurable)
	if pluginConfig.TrackRequests {
		requestCount++
		req.Headers[fmt.Sprintf("%s-Request-Number", pluginConfig.CustomHeaderPrefix)] = fmt.Sprintf("%d", requestCount)
	}

	// Store HTTP metadata in context for later hooks
	ctx.SetValue(schemas.BifrostContextKey("multi-http-request-time"), time.Now())
	ctx.SetValue(schemas.BifrostContextKey("multi-http-path"), req.Path)

	return nil, nil
}

// HTTPTransportPostHook handles HTTP-layer response interception
func HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	if !pluginConfig.EnableHTTPHooks {
		return nil
	}

	if pluginConfig.EnableLogging {
		fmt.Println("[Multi-Interface Plugin] HTTPTransportPostHook called")
	}

	// Calculate HTTP duration
	if startTime, ok := ctx.Value(schemas.BifrostContextKey("multi-http-request-time")).(time.Time); ok {
		duration := time.Since(startTime)
		resp.Headers[fmt.Sprintf("%s-Duration-Ms", pluginConfig.CustomHeaderPrefix)] = fmt.Sprintf("%d", duration.Milliseconds())
	}

	// Add plugin info header
	var interfaces []string
	if pluginConfig.EnableHTTPHooks {
		interfaces = append(interfaces, "http")
	}
	if pluginConfig.EnableLLMHooks {
		interfaces = append(interfaces, "llm")
	}
	if pluginConfig.EnableMCPHooks {
		interfaces = append(interfaces, "mcp")
	}
	if pluginConfig.EnableObservability {
		interfaces = append(interfaces, "observability")
	}
	resp.Headers[fmt.Sprintf("%s-Interfaces", pluginConfig.CustomHeaderPrefix)] = fmt.Sprintf("%v", interfaces)

	return nil
}

// ============================================================================
// LLMPlugin Interface
// ============================================================================

// PreLLMHook is called before the LLM provider is invoked
func PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if !pluginConfig.EnableLLMHooks {
		return req, nil, nil
	}

	if pluginConfig.EnableLogging {
		fmt.Println("[Multi-Interface Plugin] PreLLMHook called")
		httpPath := ctx.Value(schemas.BifrostContextKey("multi-http-path"))
		fmt.Printf("[Multi-Interface Plugin] Processing LLM request from path: %v\n", httpPath)
	}

	// Store LLM metadata
	ctx.SetValue(schemas.BifrostContextKey("multi-llm-start-time"), time.Now())

	// Example: Add system prompt with uptime (configurable)
	if pluginConfig.InjectUptime && req.ChatRequest != nil && req.ChatRequest.Input != nil {
		var content string
		if pluginConfig.TrackRequests {
			content = fmt.Sprintf("Processing request #%d. Server uptime: %v", requestCount, time.Since(startTime))
		} else {
			content = fmt.Sprintf("Server uptime: %v", time.Since(startTime))
		}
		systemMsg := schemas.ChatMessage{
			Role:    "system",
			Content: &schemas.ChatMessageContent{ContentStr: &content},
		}
		req.ChatRequest.Input = append([]schemas.ChatMessage{systemMsg}, req.ChatRequest.Input...)
	}

	return req, nil, nil
}

// PostLLMHook is called after the LLM provider responds
func PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if !pluginConfig.EnableLLMHooks {
		return resp, bifrostErr, nil
	}

	if pluginConfig.EnableLogging {
		fmt.Println("[Multi-Interface Plugin] PostLLMHook called")
	}

	// Calculate LLM duration
	if startTime, ok := ctx.Value(schemas.BifrostContextKey("multi-llm-start-time")).(time.Time); ok {
		duration := time.Since(startTime)
		if pluginConfig.EnableLogging {
			fmt.Printf("[Multi-Interface Plugin] LLM call took: %v\n", duration)
		}

		// Store for observability
		ctx.SetValue(schemas.BifrostContextKey("multi-llm-duration"), duration)
	}

	return resp, bifrostErr, nil
}

// ============================================================================
// MCPPlugin Interface
// ============================================================================

// PreMCPHook is called before MCP tool/resource calls are executed
func PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if !pluginConfig.EnableMCPHooks {
		return req, nil, nil
	}

	if pluginConfig.EnableLogging {
		fmt.Println("[Multi-Interface Plugin] PreMCPHook called")
		httpPath := ctx.Value(schemas.BifrostContextKey("multi-http-path"))
		fmt.Printf("[Multi-Interface Plugin] Processing MCP request from path: %v\n", httpPath)
	}

	// Store MCP metadata
	ctx.SetValue(schemas.BifrostContextKey("multi-mcp-start-time"), time.Now())
	ctx.SetValue(schemas.BifrostContextKey("multi-mcp-type"), req.RequestType)

	// Example: Log the MCP call
	if pluginConfig.EnableLogging && req.ChatAssistantMessageToolCall != nil && req.ChatAssistantMessageToolCall.Function.Name != nil {
		fmt.Printf("[Multi-Interface Plugin] MCP tool call: %s\n", *req.ChatAssistantMessageToolCall.Function.Name)
	}

	return req, nil, nil
}

// PostMCPHook is called after MCP tool/resource calls complete
func PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if !pluginConfig.EnableMCPHooks {
		return resp, bifrostErr, nil
	}

	if pluginConfig.EnableLogging {
		fmt.Println("[Multi-Interface Plugin] PostMCPHook called")
	}

	// Calculate MCP duration
	if startTime, ok := ctx.Value(schemas.BifrostContextKey("multi-mcp-start-time")).(time.Time); ok {
		duration := time.Since(startTime)
		if pluginConfig.EnableLogging {
			fmt.Printf("[Multi-Interface Plugin] MCP call took: %v\n", duration)
		}

		// Store for observability
		ctx.SetValue(schemas.BifrostContextKey("multi-mcp-duration"), duration)
	}

	return resp, bifrostErr, nil
}

// ============================================================================
// ObservabilityPlugin Interface
// ============================================================================

// Inject receives completed traces for forwarding to observability backends
func Inject(ctx context.Context, trace *schemas.Trace) error {
	if !pluginConfig.EnableObservability {
		return nil
	}

	if pluginConfig.EnableLogging {
		fmt.Println("[Multi-Interface Plugin] Inject called - sending trace to observability backend")
	}

	// Example: Format trace as JSON
	traceJSON, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		fmt.Printf("[Multi-Interface Plugin] Failed to marshal trace: %v\n", err)
		return err
	}

	// Example: Log the trace (in production, send to OTEL, Datadog, etc.)
	if pluginConfig.EnableLogging {
		fmt.Printf("[Multi-Interface Plugin] Trace data:\n%s\n", string(traceJSON))
	}

	// In production, you would send this to your observability backend here
	// Example: sendToDatadog(traceJSON)
	// Example: sendToOTEL(trace)

	return nil
}

// ============================================================================
// Cleanup
// ============================================================================

// Cleanup is called when the plugin is unloaded (required)
func Cleanup() error {
	uptime := time.Since(startTime)
	if pluginConfig.TrackRequests {
		fmt.Printf("[Multi-Interface Plugin] Cleanup called - processed %d requests over %v\n",
			requestCount, uptime)
	} else {
		fmt.Printf("[Multi-Interface Plugin] Cleanup called - uptime: %v\n", uptime)
	}
	return nil
}
