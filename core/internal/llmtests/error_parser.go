package llmtests

import (
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// ERROR PARSING AND FORMATTING UTILITIES
// =============================================================================

// ParsedError represents a cleaned-up, human-readable error
type ParsedError struct {
	Category    string                 // Error category (HTTP, Auth, RateLimit, etc.)
	Title       string                 // Short, readable title
	Message     string                 // Main error message
	Details     []string               // Additional details
	Suggestions []string               // Potential solutions
	Technical   map[string]interface{} // Technical details for debugging
}

// ErrorCategory represents different types of errors
type ErrorCategory struct {
	Name        string
	Description string
	Color       string // For potential colored output
}

var (
	// Common error categories
	CategoryHTTP       = ErrorCategory{"HTTP", "HTTP/Network Error", "🔴"}
	CategoryAuth       = ErrorCategory{"Authentication", "Authentication/Authorization Error", "🔐"}
	CategoryRateLimit  = ErrorCategory{"Rate Limit", "Rate Limiting Error", "⏱️"}
	CategoryProvider   = ErrorCategory{"Provider", "Provider-Specific Error", "⚠️"}
	CategoryValidation = ErrorCategory{"Validation", "Input Validation Error", "📋"}
	CategoryTimeout    = ErrorCategory{"Timeout", "Request Timeout Error", "⏰"}
	CategoryQuota      = ErrorCategory{"Quota", "Quota/Billing Error", "💳"}
	CategoryModel      = ErrorCategory{"Model", "Model-Related Error", "🤖"}
	CategoryBifrost    = ErrorCategory{"Bifrost", "Bifrost Internal Error", "🌉"}
	CategoryUnknown    = ErrorCategory{"Unknown", "Unknown Error", "❓"}
)

// ParseBifrostError converts a BifrostError into a human-readable ParsedError
func ParseBifrostError(err *schemas.BifrostError) ParsedError {
	if err == nil {
		return ParsedError{
			Category: CategoryUnknown.Name,
			Title:    "Unknown Error",
			Message:  "Received nil error",
		}
	}

	parsed := ParsedError{
		Technical:   make(map[string]interface{}),
		Details:     make([]string, 0),
		Suggestions: make([]string, 0),
	}

	// Store technical details
	parsed.Technical["provider"] = err.ExtraFields.Provider
	parsed.Technical["is_bifrost_error"] = err.IsBifrostError
	if err.StatusCode != nil {
		parsed.Technical["status_code"] = *err.StatusCode
	}
	if err.EventID != nil {
		parsed.Technical["event_id"] = *err.EventID
	}

	// Categorize and parse the error
	parsed.Category, parsed.Title = categorizeError(err)
	parsed.Message = cleanErrorMessage(err.Error.Message)

	// Add provider context if available
	if err.ExtraFields.Provider != "" {
		parsed.Details = append(parsed.Details, fmt.Sprintf("Provider: %s", err.ExtraFields.Provider))
	}

	// Parse based on category
	switch parsed.Category {
	case CategoryHTTP.Name:
		parseHTTPError(err, &parsed)
	case CategoryAuth.Name:
		parseAuthError(err, &parsed)
	case CategoryRateLimit.Name:
		parseRateLimitError(err, &parsed)
	case CategoryProvider.Name:
		parseProviderError(err, &parsed)
	case CategoryValidation.Name:
		parseValidationError(err, &parsed)
	case CategoryTimeout.Name:
		parseTimeoutError(err, &parsed)
	case CategoryQuota.Name:
		parseQuotaError(err, &parsed)
	case CategoryModel.Name:
		parseModelError(err, &parsed)
	default:
		parseGenericError(err, &parsed)
	}

	return parsed
}

// categorizeError determines the error category based on status codes, types, and messages
func categorizeError(err *schemas.BifrostError) (category, title string) {
	// Check status code first
	if err.StatusCode != nil {
		switch *err.StatusCode {
		case 400:
			return CategoryValidation.Name, "Bad Request"
		case 401:
			return CategoryAuth.Name, "Authentication Required"
		case 403:
			return CategoryAuth.Name, "Access Forbidden"
		case 404:
			return CategoryModel.Name, "Model Not Found"
		case 408:
			return CategoryTimeout.Name, "Request Timeout"
		case 429:
			return CategoryRateLimit.Name, "Rate Limited"
		case 500, 502, 503, 504:
			return CategoryProvider.Name, "Provider Service Error"
		}

		if *err.StatusCode >= 400 && *err.StatusCode < 500 {
			return CategoryValidation.Name, "Client Error"
		}
		if *err.StatusCode >= 500 {
			return CategoryProvider.Name, "Server Error"
		}
	}

	// Check error type
	if err.Error.Type != nil {
		errorType := strings.ToLower(*err.Error.Type)
		switch {
		case strings.Contains(errorType, "auth"):
			return CategoryAuth.Name, "Authentication Error"
		case strings.Contains(errorType, "rate"):
			return CategoryRateLimit.Name, "Rate Limit Error"
		case strings.Contains(errorType, "quota"):
			return CategoryQuota.Name, "Quota Exceeded"
		case strings.Contains(errorType, "timeout"):
			return CategoryTimeout.Name, "Timeout Error"
		case strings.Contains(errorType, "validation"):
			return CategoryValidation.Name, "Validation Error"
		}
	}

	// Check error message for keywords
	message := strings.ToLower(err.Error.Message)
	switch {
	case strings.Contains(message, "unauthorized") || strings.Contains(message, "invalid api key"):
		return CategoryAuth.Name, "Invalid API Key"
	case strings.Contains(message, "rate limit") || strings.Contains(message, "too many requests"):
		return CategoryRateLimit.Name, "Rate Limited"
	case strings.Contains(message, "quota") || strings.Contains(message, "billing"):
		return CategoryQuota.Name, "Quota/Billing Issue"
	case strings.Contains(message, "timeout") || strings.Contains(message, "deadline"):
		return CategoryTimeout.Name, "Request Timeout"
	case strings.Contains(message, "model") && (strings.Contains(message, "not found") || strings.Contains(message, "does not exist")):
		return CategoryModel.Name, "Model Not Available"
	case strings.Contains(message, "connection") || strings.Contains(message, "network"):
		return CategoryHTTP.Name, "Network Error"
	case err.IsBifrostError:
		return CategoryBifrost.Name, "Bifrost Internal Error"
	}

	// Default based on HTTP status
	if err.StatusCode != nil && *err.StatusCode >= 400 {
		return CategoryHTTP.Name, fmt.Sprintf("HTTP %d Error", *err.StatusCode)
	}

	return CategoryUnknown.Name, "Unknown Error"
}

// cleanErrorMessage cleans up the error message for better readability
func cleanErrorMessage(message string) string {
	if message == "" {
		return "No error message provided"
	}

	// Remove common technical prefixes
	message = strings.TrimPrefix(message, "error: ")
	message = strings.TrimPrefix(message, "Error: ")
	message = strings.TrimPrefix(message, "failed to ")
	message = strings.TrimPrefix(message, "Failed to ")

	// Capitalize first letter
	if len(message) > 0 {
		message = strings.ToUpper(message[:1]) + message[1:]
	}

	return message
}

// parseHTTPError handles HTTP-specific error parsing
func parseHTTPError(err *schemas.BifrostError, parsed *ParsedError) {
	if err.StatusCode != nil {
		parsed.Details = append(parsed.Details, fmt.Sprintf("HTTP Status: %d", *err.StatusCode))

		// Add status-specific suggestions
		switch *err.StatusCode {
		case 502, 503, 504:
			parsed.Suggestions = append(parsed.Suggestions, "The provider service may be temporarily unavailable - retries should help")
			parsed.Suggestions = append(parsed.Suggestions, "Check the provider's status page for known issues")
		case 500:
			parsed.Suggestions = append(parsed.Suggestions, "This appears to be a provider-side error - consider using fallbacks")
		}
	}
}

// parseAuthError handles authentication-specific error parsing
func parseAuthError(err *schemas.BifrostError, parsed *ParsedError) {
	message := strings.ToLower(err.Error.Message)

	if strings.Contains(message, "api key") {
		parsed.Suggestions = append(parsed.Suggestions, "Verify your API key is correct and properly set in environment variables")
		parsed.Suggestions = append(parsed.Suggestions, "Check if the API key has the necessary permissions for this operation")
	}

	if strings.Contains(message, "unauthorized") {
		parsed.Suggestions = append(parsed.Suggestions, "Ensure you have valid credentials for this provider")
		parsed.Suggestions = append(parsed.Suggestions, "Check if your account has access to the requested model")
	}

	if strings.Contains(message, "forbidden") {
		parsed.Suggestions = append(parsed.Suggestions, "Your account may not have permission for this operation")
		parsed.Suggestions = append(parsed.Suggestions, "Contact your provider to verify account permissions")
	}
}

// parseRateLimitError handles rate limiting error parsing
func parseRateLimitError(err *schemas.BifrostError, parsed *ParsedError) {
	parsed.Suggestions = append(parsed.Suggestions, "Reduce request frequency or implement exponential backoff")
	parsed.Suggestions = append(parsed.Suggestions, "Consider upgrading your provider plan for higher rate limits")

	// Try to extract rate limit details from message
	message := err.Error.Message
	if strings.Contains(message, "per") {
		parsed.Details = append(parsed.Details, "Rate limit details may be in the error message")
	}
}

// parseProviderError handles provider-specific error parsing
func parseProviderError(err *schemas.BifrostError, parsed *ParsedError) {
	parsed.Details = append(parsed.Details, "This is a provider-specific error")

	// Provider-specific suggestions
	switch err.ExtraFields.Provider {
	case schemas.OpenAI:
		parsed.Suggestions = append(parsed.Suggestions, "Check OpenAI's status page: https://status.openai.com/")
	case schemas.Anthropic:
		parsed.Suggestions = append(parsed.Suggestions, "Check Anthropic's status page: https://status.anthropic.com/")
	case schemas.Azure:
		parsed.Suggestions = append(parsed.Suggestions, "Check Azure's status page: https://status.azure.com/")
	case schemas.Bedrock:
		parsed.Suggestions = append(parsed.Suggestions, "Check AWS service health: https://status.aws.amazon.com/")
	default:
		parsed.Suggestions = append(parsed.Suggestions, "Check the provider's status page or documentation")
	}

	parsed.Suggestions = append(parsed.Suggestions, "Consider using fallback providers if configured")
}

// parseValidationError handles validation error parsing
func parseValidationError(err *schemas.BifrostError, parsed *ParsedError) {
	parsed.Suggestions = append(parsed.Suggestions, "Verify all required parameters are provided")
	parsed.Suggestions = append(parsed.Suggestions, "Check parameter types and formats match API requirements")

	// Extract parameter information if available
	if err.Error.Param != nil {
		parsed.Details = append(parsed.Details, fmt.Sprintf("Related parameter: %v", err.Error.Param))
	}
}

// parseTimeoutError handles timeout error parsing
func parseTimeoutError(err *schemas.BifrostError, parsed *ParsedError) {
	parsed.Suggestions = append(parsed.Suggestions, "Increase request timeout settings if possible")
	parsed.Suggestions = append(parsed.Suggestions, "Try breaking large requests into smaller chunks")
	parsed.Suggestions = append(parsed.Suggestions, "Check network connectivity to the provider")
}

// parseQuotaError handles quota/billing error parsing
func parseQuotaError(err *schemas.BifrostError, parsed *ParsedError) {
	parsed.Suggestions = append(parsed.Suggestions, "Check your account billing and usage limits")
	parsed.Suggestions = append(parsed.Suggestions, "Consider upgrading your provider plan")
	parsed.Suggestions = append(parsed.Suggestions, "Monitor your token usage to avoid hitting limits")
}

// parseModelError handles model-specific error parsing
func parseModelError(err *schemas.BifrostError, parsed *ParsedError) {
	message := strings.ToLower(err.Error.Message)

	if strings.Contains(message, "not found") || strings.Contains(message, "does not exist") {
		parsed.Suggestions = append(parsed.Suggestions, "Verify the model name is correct and supported by the provider")
		parsed.Suggestions = append(parsed.Suggestions, "Check if you have access to this model with your current plan")
		parsed.Suggestions = append(parsed.Suggestions, "Consult the provider's documentation for available models")
	}

	if strings.Contains(message, "deprecated") {
		parsed.Suggestions = append(parsed.Suggestions, "This model is deprecated - consider switching to a newer model")
	}
}

// parseGenericError handles unknown/generic errors
func parseGenericError(err *schemas.BifrostError, parsed *ParsedError) {
	parsed.Suggestions = append(parsed.Suggestions, "Check the provider's documentation for more details")
	parsed.Suggestions = append(parsed.Suggestions, "Consider enabling debug logging for more information")

	if err.Error.Error != nil {
		parsed.Details = append(parsed.Details, fmt.Sprintf("Underlying error: %s", err.Error.Error.Error()))
	}
}

// =============================================================================
// FORMATTING AND DISPLAY FUNCTIONS
// =============================================================================

// FormatError formats a ParsedError for display
func FormatError(parsed ParsedError) string {
	var builder strings.Builder

	// Header with category and title
	categoryInfo := getCategory(parsed.Category)
	builder.WriteString(fmt.Sprintf("%s %s: %s\n", categoryInfo.Color, categoryInfo.Name, parsed.Title))

	// Main message
	builder.WriteString(fmt.Sprintf("Message: %s\n", parsed.Message))

	// Details
	if len(parsed.Details) > 0 {
		builder.WriteString("Details:\n")
		for _, detail := range parsed.Details {
			builder.WriteString(fmt.Sprintf("  • %s\n", detail))
		}
	}

	// Suggestions
	if len(parsed.Suggestions) > 0 {
		builder.WriteString("Suggestions:\n")
		for _, suggestion := range parsed.Suggestions {
			builder.WriteString(fmt.Sprintf("  💡 %s\n", suggestion))
		}
	}

	return builder.String()
}

// FormatErrorConcise formats a ParsedError in a concise format
func FormatErrorConcise(parsed ParsedError) string {
	categoryInfo := getCategory(parsed.Category)
	return fmt.Sprintf("%s %s: %s", categoryInfo.Color, parsed.Title, parsed.Message)
}

// LogError logs a BifrostError in a readable format
func LogError(t *testing.T, err *schemas.BifrostError, context string) {
	if err == nil {
		return
	}

	parsed := ParseBifrostError(err)
	t.Logf("❌ %s Error:\n%s", context, FormatError(parsed))
}

// LogErrorConcise logs a BifrostError in a concise format
func LogErrorConcise(t *testing.T, err *schemas.BifrostError, context string) {
	if err == nil {
		return
	}

	parsed := ParseBifrostError(err)
	t.Logf("❌ %s: %s", context, FormatErrorConcise(parsed))
}

// RequireNoError is like require.NoError but with better error formatting
// ALWAYS includes ❌ prefix in error messages for consistency
func RequireNoError(t *testing.T, err *schemas.BifrostError, msgAndArgs ...interface{}) {
	if err != nil {
		parsed := ParseBifrostError(err)
		message := "Expected no error"
		if len(msgAndArgs) > 0 {
			if msg, ok := msgAndArgs[0].(string); ok {
				if len(msgAndArgs) > 1 {
					message = fmt.Sprintf(msg, msgAndArgs[1:]...)
				} else {
					message = msg
				}
			}
		}
		// Ensure message has ❌ prefix
		if !strings.Contains(message, "❌") {
			message = fmt.Sprintf("❌ %s", message)
		}
		t.Fatalf("%s, but got:\n%s", message, FormatError(parsed))
	}
}

// AssertNoError is like assert.NoError but with better error formatting
func AssertNoError(t *testing.T, err *schemas.BifrostError, msgAndArgs ...interface{}) bool {
	if err != nil {
		parsed := ParseBifrostError(err)
		message := "Expected no error"
		if len(msgAndArgs) > 0 {
			if msg, ok := msgAndArgs[0].(string); ok {
				if len(msgAndArgs) > 1 {
					message = fmt.Sprintf(msg, msgAndArgs[1:]...)
				} else {
					message = msg
				}
			}
		}
		t.Fatalf("%s, but got:\n%s", message, FormatError(parsed))
		return false
	}
	return true
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// getCategory returns the category info for a category name
func getCategory(name string) ErrorCategory {
	switch name {
	case CategoryHTTP.Name:
		return CategoryHTTP
	case CategoryAuth.Name:
		return CategoryAuth
	case CategoryRateLimit.Name:
		return CategoryRateLimit
	case CategoryProvider.Name:
		return CategoryProvider
	case CategoryValidation.Name:
		return CategoryValidation
	case CategoryTimeout.Name:
		return CategoryTimeout
	case CategoryQuota.Name:
		return CategoryQuota
	case CategoryModel.Name:
		return CategoryModel
	case CategoryBifrost.Name:
		return CategoryBifrost
	default:
		return CategoryUnknown
	}
}

// IsRetryableError determines if an error should trigger a retry
func IsRetryableError(err *schemas.BifrostError) bool {
	if err == nil {
		return false
	}

	// Check status codes
	if err.StatusCode != nil {
		switch *err.StatusCode {
		case 429, 500, 502, 503, 504: // Rate limit and server errors
			return true
		case 400, 401, 403, 404: // Client errors (usually not retryable)
			return false
		}
	}

	// Check error message for retryable conditions
	message := strings.ToLower(err.Error.Message)
	retryableKeywords := []string{
		"timeout", "rate limit", "temporarily unavailable",
		"service unavailable", "internal server error",
		"connection", "network",
	}

	for _, keyword := range retryableKeywords {
		if strings.Contains(message, keyword) {
			return true
		}
	}

	return false
}

// GetRetryDelay suggests a retry delay based on the error type
func GetRetryDelay(err *schemas.BifrostError, attempt int) int {
	if err == nil {
		return 0
	}

	baseDelay := 1 // seconds

	// Adjust base delay by error type
	if err.StatusCode != nil {
		switch *err.StatusCode {
		case 429: // Rate limit
			baseDelay = 5
		case 500, 502, 503, 504: // Server errors
			baseDelay = 2
		}
	}

	// Exponential backoff
	delay := baseDelay * (1 << (attempt - 1)) // 2^(attempt-1)

	// Cap at reasonable maximum
	if delay > 30 {
		delay = 30
	}

	return delay
}
