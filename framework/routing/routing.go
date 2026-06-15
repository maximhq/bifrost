package routing

import (
	"fmt"
	"regexp"
	"strings"
)

// headerKeyPattern matches header map access patterns like headers["X-Api-Key"] or headers['X-Api-Key']
var headerKeyPattern = regexp.MustCompile(`headers\[["']([^"']+)["']\]`)

// headerInPattern matches "in headers" membership test patterns like "X-Api-Key" in headers or 'X-Api-Key' in headers
var headerInPattern = regexp.MustCompile(`["']([^"']+)["']\s+in\s+headers`)

// paramKeyPattern matches param map access patterns like params["Region"] or params['Region']
var paramKeyPattern = regexp.MustCompile(`params\[["']([^"']+)["']\]`)

// paramInPattern matches "in params" membership test patterns like "Region" in params or 'Region' in params
var paramInPattern = regexp.MustCompile(`["']([^"']+)["']\s+in\s+params`)

// normalizeMapKeysInCEL lowercases header and param keys in CEL expressions
// so that headers["X-Api-Key"] becomes headers["x-api-key"], "X-Api-Key" in headers becomes "x-api-key" in headers,
// params["Region"] becomes params["region"], and "Region" in params becomes "region" in params.
// This ensures CEL expressions match against the normalized (lowercase) map keys at runtime.
func NormalizeMapKeysInCEL(expr string) string {
	toLower := func(match string) string {
		return strings.ToLower(match)
	}
	// Normalize bracket access
	expr = headerKeyPattern.ReplaceAllStringFunc(expr, toLower)
	expr = paramKeyPattern.ReplaceAllStringFunc(expr, toLower)
	// Normalize "in" membership test
	expr = headerInPattern.ReplaceAllStringFunc(expr, toLower)
	expr = paramInPattern.ReplaceAllStringFunc(expr, toLower)
	return expr
}

// validateCELExpression performs basic validation on CEL expression format
func ValidateCELExpression(expr string) error {
	normalized := strings.TrimSpace(expr)
	if normalized == "" || normalized == "true" || normalized == "false" {
		return nil // Empty, true, or false are valid
	}

	// List of allowed operators and keywords
	validPatterns := []string{
		"==", "!=", "&&", "||", ">", "<", ">=", "<=",
		"in ", "matches ", ".startsWith(", ".contains(", ".endsWith(",
		"[", "]", "(", ")", "!",
	}

	// Check if expression contains at least one valid operator
	hasPattern := false
	for _, pattern := range validPatterns {
		if strings.Contains(normalized, pattern) {
			hasPattern = true
			break
		}
	}

	if !hasPattern {
		return fmt.Errorf("expression must contain at least one operator: %s", expr)
	}

	return nil
}
