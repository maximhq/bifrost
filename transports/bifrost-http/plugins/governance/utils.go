// Package governance provides utility functions for the governance plugin
package governance

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"gorm.io/gorm"
)

// ExtractHeadersFromContext extracts governance headers from the context
func (gp *GovernancePlugin) ExtractHeadersFromContext(ctx context.Context) map[string]string {
	headers := make(map[string]string)

	// Extract governance headers
	if teamID := gp.GetStringFromContext(ctx, "x-bf-team"); teamID != "" {
		headers["x-bf-team"] = teamID
	}
	if userID := gp.GetStringFromContext(ctx, "x-bf-user"); userID != "" {
		headers["x-bf-user"] = userID
	}
	if customerID := gp.GetStringFromContext(ctx, "x-bf-customer"); customerID != "" {
		headers["x-bf-customer"] = customerID
	}

	return headers
}

// ExtractVirtualKey extracts the virtual key from the x-bf-vk header
func (gp *GovernancePlugin) ExtractVirtualKey(ctx context.Context) string {
	return gp.GetStringFromContext(ctx, "x-bf-vk")
}

// ExtractRequestID extracts the request ID from context
func (gp *GovernancePlugin) ExtractRequestID(ctx context.Context) string {
	return gp.GetStringFromContext(ctx, "request-id")
}

// GetStringFromContext safely extracts a string value from context
func (gp *GovernancePlugin) GetStringFromContext(ctx context.Context, key string) string {
	if value := ctx.Value(lib.ContextKey(key)); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// GetOriginalRequestFromContext attempts to extract the original request from context
func (gp *GovernancePlugin) GetOriginalRequestFromContext(ctx context.Context) *schemas.BifrostRequest {
	// This is a placeholder - you might need to adjust based on how the original request is stored in context
	// For now, we'll return nil and rely on the information from the response
	return nil
}

// CalculateCost calculates the cost for a request (placeholder implementation)
func (gp *GovernancePlugin) CalculateCost(provider schemas.ModelProvider, model string, tokens int64) int64 {
	// Placeholder cost calculation - implement actual pricing logic later
	// This is just a simple example
	baseCostPerToken := int64(1) // 1 cent per 1000 tokens
	return (tokens * baseCostPerToken) / 1000
}

// AutoMigrateGovernanceTables ensures all governance tables exist
func AutoMigrateGovernanceTables(db *gorm.DB) error {
	// List of all governance models to migrate (new hierarchical system)
	models := []interface{}{
		&Budget{},
		&RateLimit{},
		&Customer{},
		&Team{},
		&VirtualKey{},
	}

	for _, model := range models {
		if err := db.AutoMigrate(model); err != nil {
			return fmt.Errorf("failed to migrate model %T: %w", model, err)
		}
	}

	return nil
}

// Standalone utility functions for use across the governance plugin

// extractHeadersFromContext extracts governance headers from context (standalone version)
func extractHeadersFromContext(ctx context.Context) map[string]string {
	headers := make(map[string]string)

	// Extract governance headers using lib.ContextKey
	if teamID := getStringFromContext(ctx, "x-bf-team"); teamID != "" {
		headers["x-bf-team"] = teamID
	}
	if userID := getStringFromContext(ctx, "x-bf-user"); userID != "" {
		headers["x-bf-user"] = userID
	}
	if customerID := getStringFromContext(ctx, "x-bf-customer"); customerID != "" {
		headers["x-bf-customer"] = customerID
	}

	return headers
}

// calculateCostInCents calculates cost in cents (standalone version)
func calculateCostInCents(provider schemas.ModelProvider, model string, tokensUsed int64) int64 {
	// Placeholder cost calculation - implement actual pricing logic later
	baseCostPerToken := int64(1) // 1 cent per 10 tokens
	return (tokensUsed * baseCostPerToken) / 10
}

// getStringFromContext safely extracts a string value from context (helper)
func getStringFromContext(ctx context.Context, key string) string {
	if value := ctx.Value(lib.ContextKey(key)); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}
