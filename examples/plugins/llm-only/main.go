package main

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// Plugin configuration
type PluginConfig struct {
	InjectSystemMessage bool   `json:"inject_system_message"` // Toggle system message injection
	SystemMessageText   string `json:"system_message_text"`   // Custom system message
	EnableLogging       bool   `json:"enable_logging"`        // Toggle detailed logging
	LogRequests         bool   `json:"log_requests"`          // Log request details
	LogResponses        bool   `json:"log_responses"`         // Log response details
}

var (
	// Default configuration
	pluginConfig = &PluginConfig{
		InjectSystemMessage: true,
		SystemMessageText:   "You are a helpful assistant. This message was added by an LLM plugin.",
		EnableLogging:       true,
		LogRequests:         true,
		LogResponses:        true,
	}
)

// Init is called when the plugin is loaded (optional)
func Init(config any) error {
	fmt.Println("[LLM-Only Plugin] Init called")

	// Parse configuration
	if configMap, ok := config.(map[string]interface{}); ok {
		if injectMsg, ok := configMap["inject_system_message"].(bool); ok {
			pluginConfig.InjectSystemMessage = injectMsg
			fmt.Printf("[LLM-Only Plugin] System message injection: %v\n", pluginConfig.InjectSystemMessage)
		}

		if msgText, ok := configMap["system_message_text"].(string); ok {
			pluginConfig.SystemMessageText = msgText
			fmt.Printf("[LLM-Only Plugin] System message: %s\n", pluginConfig.SystemMessageText)
		}

		if enableLogging, ok := configMap["enable_logging"].(bool); ok {
			pluginConfig.EnableLogging = enableLogging
			fmt.Printf("[LLM-Only Plugin] Logging enabled: %v\n", pluginConfig.EnableLogging)
		}

		if logReq, ok := configMap["log_requests"].(bool); ok {
			pluginConfig.LogRequests = logReq
		}

		if logResp, ok := configMap["log_responses"].(bool); ok {
			pluginConfig.LogResponses = logResp
		}
	}

	fmt.Printf("[LLM-Only Plugin] Configuration loaded: %+v\n", pluginConfig)
	return nil
}

// GetName returns the name of the plugin (required)
// This is the system identifier - not editable by users
// Users can set a custom display_name in the config for the UI
func GetName() string {
	return "llm-only"
}

// PreLLMHook is called before the LLM provider is invoked
// This example demonstrates request modification and logging
func PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if pluginConfig.EnableLogging {
		fmt.Println("[LLM-Only Plugin] PreLLMHook called")
	}

	// Example: Log the request (configurable)
	if pluginConfig.LogRequests && req.ChatRequest != nil {
		fmt.Printf("[LLM-Only Plugin] Provider: %s, Model: %s\n",
			req.ChatRequest.Provider, req.ChatRequest.Model)
		if pluginConfig.EnableLogging {
			fmt.Printf("[LLM-Only Plugin] Message count: %d\n", len(req.ChatRequest.Input))
		}
	}

	// Example: Store metadata in context
	ctx.SetValue(schemas.BifrostContextKey("llm-plugin-timestamp"), "pre-hook-timestamp")

	// Example: Modify the request (add a system message) - configurable
	if pluginConfig.InjectSystemMessage && req.ChatRequest != nil && req.ChatRequest.Input != nil {
		systemMsg := schemas.ChatMessage{
			Role:    "system",
			Content: &schemas.ChatMessageContent{ContentStr: &pluginConfig.SystemMessageText},
		}
		req.ChatRequest.Input = append([]schemas.ChatMessage{systemMsg}, req.ChatRequest.Input...)
		if pluginConfig.EnableLogging {
			fmt.Println("[LLM-Only Plugin] System message injected")
		}
	}

	// Return modified request, no short-circuit, no error
	return req, nil, nil
}

// PostLLMHook is called after the LLM provider responds
// This example demonstrates response modification and logging
func PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if pluginConfig.EnableLogging {
		fmt.Println("[LLM-Only Plugin] PostLLMHook called")
	}

	// Retrieve metadata from context
	if pluginConfig.EnableLogging {
		timestamp := ctx.Value(schemas.BifrostContextKey("llm-plugin-timestamp"))
		fmt.Printf("[LLM-Only Plugin] Request timestamp: %v\n", timestamp)
	}

	// Example: Log the response (configurable)
	if pluginConfig.LogResponses && resp != nil && resp.ChatResponse != nil {
		fmt.Printf("[LLM-Only Plugin] Response ID: %s, Model: %s\n",
			resp.ChatResponse.ID, resp.ChatResponse.Model)
		if pluginConfig.EnableLogging && len(resp.ChatResponse.Choices) > 0 {
			fmt.Printf("[LLM-Only Plugin] Choices count: %d\n", len(resp.ChatResponse.Choices))
		}
	}

	// Example: Log errors if present
	if bifrostErr != nil && bifrostErr.Error != nil {
		fmt.Printf("[LLM-Only Plugin] Error occurred: %v\n", bifrostErr.Error.Message)
	}

	// Return unmodified response and error
	return resp, bifrostErr, nil
}

// Cleanup is called when the plugin is unloaded (required)
func Cleanup() error {
	if pluginConfig.EnableLogging {
		fmt.Println("[LLM-Only Plugin] Cleanup called")
	}
	return nil
}
