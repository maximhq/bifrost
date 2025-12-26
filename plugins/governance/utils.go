// Package governance provides utility functions for the governance plugin
package governance

import (
	"context"
)

// getStringFromContext safely extracts a string value from context
func getStringFromContext(ctx context.Context, key any) string {
	if value := ctx.Value(key); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// equalPtr compares two pointers of comparable type for value equality
// Returns true if both are nil or both are non-nil with equal values
func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
