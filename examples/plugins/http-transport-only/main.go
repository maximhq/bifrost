package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Plugin configuration
type PluginConfig struct {
	RequireAuth bool `json:"require_auth"`  // Toggle auth header enforcement
	RateLimit   int  `json:"rate_limit"`    // Max requests per window (0 = unlimited)
	RateWindow  int  `json:"rate_window"`   // Rate limit window in seconds (default: 60)
	MaxBodySize int  `json:"max_body_size"` // Max request body size in bytes (0 = unlimited)
}

var (
	// Default configuration
	pluginConfig = &PluginConfig{
		RequireAuth: true,        // Require auth by default
		RateLimit:   10,          // 10 requests per window by default
		RateWindow:  60,          // 60 second window by default
		MaxBodySize: 1024 * 1024, // 1MB by default
	}

	rateLimiter = &RateLimiter{
		requests: make(map[string][]time.Time),
	}
)

type RateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
}

func (rl *RateLimiter) Allow(key string, limit int, window int) bool {
	// If rate limiting is disabled (limit = 0), allow all requests
	if limit <= 0 {
		return true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-time.Duration(window) * time.Second)

	// Clean old requests
	if reqs, ok := rl.requests[key]; ok {
		validReqs := []time.Time{}
		for _, t := range reqs {
			if t.After(windowStart) {
				validReqs = append(validReqs, t)
			}
		}
		rl.requests[key] = validReqs

		// Check if limit exceeded
		if len(validReqs) >= limit {
			return false
		}
	}

	// Add new request
	rl.requests[key] = append(rl.requests[key], now)
	return true
}

// Init is called when the plugin is loaded (optional)
func Init(config any) error {
	fmt.Println("[HTTP-Transport-Only Plugin] Init called")

	// Parse configuration
	if configMap, ok := config.(map[string]interface{}); ok {
		// Parse require_auth toggle
		if requireAuth, ok := configMap["require_auth"].(bool); ok {
			pluginConfig.RequireAuth = requireAuth
			fmt.Printf("[HTTP-Transport-Only Plugin] Auth enforcement: %v\n", pluginConfig.RequireAuth)
		}

		// Parse rate_limit
		if rateLimit, ok := configMap["rate_limit"].(float64); ok {
			pluginConfig.RateLimit = int(rateLimit)
			if pluginConfig.RateLimit <= 0 {
				fmt.Println("[HTTP-Transport-Only Plugin] Rate limiting disabled")
			} else {
				fmt.Printf("[HTTP-Transport-Only Plugin] Rate limit: %d requests per %d seconds\n",
					pluginConfig.RateLimit, pluginConfig.RateWindow)
			}
		}

		// Parse rate_window
		if rateWindow, ok := configMap["rate_window"].(float64); ok {
			pluginConfig.RateWindow = int(rateWindow)
			fmt.Printf("[HTTP-Transport-Only Plugin] Rate window: %d seconds\n", pluginConfig.RateWindow)
		}

		// Parse max_body_size
		if maxBodySize, ok := configMap["max_body_size"].(float64); ok {
			pluginConfig.MaxBodySize = int(maxBodySize)
			if pluginConfig.MaxBodySize <= 0 {
				fmt.Println("[HTTP-Transport-Only Plugin] Request size validation disabled")
			} else {
				fmt.Printf("[HTTP-Transport-Only Plugin] Max body size: %d bytes\n", pluginConfig.MaxBodySize)
			}
		}
	}

	fmt.Printf("[HTTP-Transport-Only Plugin] Configuration loaded: %+v\n", pluginConfig)
	return nil
}

// GetName returns the name of the plugin (required)
// This is the system identifier - not editable by users
// Users can set a custom display_name in the config for the UI
func GetName() string {
	return "http-transport-only"
}

// HTTPTransportPreHook is called at the HTTP layer before requests enter Bifrost core
// This example demonstrates authentication, rate limiting, and request validation
func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	fmt.Println("[HTTP-Transport-Only Plugin] HTTPTransportPreHook called")
	fmt.Printf("[HTTP-Transport-Only Plugin] Method: %s, Path: %s\n", req.Method, req.Path)

	// Example 1: Authentication check (configurable)
	authHeader := req.CaseInsensitiveHeaderLookup("Authorization")
	if pluginConfig.RequireAuth && authHeader == "" {
		fmt.Println("[HTTP-Transport-Only Plugin] Missing authorization header")
		return &schemas.HTTPResponse{
			StatusCode: 401,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: []byte(`{"error": "Unauthorized: Missing authorization header"}`),
		}, nil
	}

	// Example 2: Rate limiting by API key (configurable)
	if pluginConfig.RateLimit > 0 {
		apiKey := authHeader // In real implementation, extract from Bearer token
		if apiKey == "" {
			apiKey = "anonymous" // Default key for unauthenticated requests
		}

		if !rateLimiter.Allow(apiKey, pluginConfig.RateLimit, pluginConfig.RateWindow) {
			fmt.Println("[HTTP-Transport-Only Plugin] Rate limit exceeded")
			return &schemas.HTTPResponse{
				StatusCode: 429,
				Headers: map[string]string{
					"Content-Type":      "application/json",
					"Retry-After":       fmt.Sprintf("%d", pluginConfig.RateWindow),
					"X-RateLimit-Limit": fmt.Sprintf("%d", pluginConfig.RateLimit),
				},
				Body: []byte(`{"error": "Rate limit exceeded. Please try again later."}`),
			}, nil
		}
	}

	// Example 3: Request validation (configurable)
	if pluginConfig.MaxBodySize > 0 && req.Method == "POST" && len(req.Body) > pluginConfig.MaxBodySize {
		fmt.Printf("[HTTP-Transport-Only Plugin] Request body too large: %d bytes (max: %d)\n",
			len(req.Body), pluginConfig.MaxBodySize)
		return &schemas.HTTPResponse{
			StatusCode: 413,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: []byte(fmt.Sprintf(`{"error": "Request body too large. Max size: %d bytes"}`, pluginConfig.MaxBodySize)),
		}, nil
	}

	// Example 4: Add custom headers
	req.Headers["X-Plugin-Processed"] = "true"
	req.Headers["X-Request-Time"] = time.Now().Format(time.RFC3339)

	// Store metadata in context for PostHook
	ctx.SetValue(schemas.BifrostContextKey("http-plugin-start-time"), time.Now())

	// Return nil to continue processing
	return nil, nil
}

// HTTPTransportPostHook is called at the HTTP layer after Bifrost core processes the request
// This example demonstrates response modification and logging
func HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	fmt.Println("[HTTP-Transport-Only Plugin] HTTPTransportPostHook called")

	// Calculate request duration
	startTime := ctx.Value(schemas.BifrostContextKey("http-plugin-start-time"))
	if t, ok := startTime.(time.Time); ok {
		duration := time.Since(t)
		fmt.Printf("[HTTP-Transport-Only Plugin] Request duration: %v\n", duration)

		// Add duration header
		resp.Headers["X-Request-Duration-Ms"] = fmt.Sprintf("%d", duration.Milliseconds())
	}

	// Example: Add CORS headers
	resp.Headers["Access-Control-Allow-Origin"] = "*"
	resp.Headers["Access-Control-Allow-Methods"] = "GET, POST, OPTIONS"
	resp.Headers["Access-Control-Allow-Headers"] = "Content-Type, Authorization"

	// Example: Add security headers
	resp.Headers["X-Content-Type-Options"] = "nosniff"
	resp.Headers["X-Frame-Options"] = "DENY"
	resp.Headers["X-XSS-Protection"] = "1; mode=block"

	// Example: Log response details
	fmt.Printf("[HTTP-Transport-Only Plugin] Response status: %d, size: %d bytes\n",
		resp.StatusCode, len(resp.Body))

	// Example: Modify error responses to add custom metadata
	if resp.StatusCode >= 400 {
		var errorBody map[string]interface{}
		if err := json.Unmarshal(resp.Body, &errorBody); err == nil {
			errorBody["timestamp"] = time.Now().Format(time.RFC3339)
			errorBody["request_id"] = ctx.Value(schemas.BifrostContextKey("request_id"))
			if newBody, err := json.Marshal(errorBody); err == nil {
				resp.Body = newBody
			}
		}
	}

	return nil
}

// Cleanup is called when the plugin is unloaded (required)
func Cleanup() error {
	fmt.Println("[HTTP-Transport-Only Plugin] Cleanup called")
	return nil
}
