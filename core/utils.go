package bifrost

import (
	"math/rand"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func Ptr[T any](v T) *T {
	return &v
}

// providerRequiresKey returns true if the given provider requires an API key for authentication.
// Some providers like Vertex and Ollama are keyless and don't require API keys.
func providerRequiresKey(providerKey schemas.ModelProvider) bool {
	return providerKey != schemas.Vertex && providerKey != schemas.Ollama
}

// calculateBackoff implements exponential backoff with jitter for retry attempts.
func calculateBackoff(attempt int, config *schemas.ProviderConfig) time.Duration {
	// Calculate an exponential backoff: initial * 2^attempt
	backoff := min(config.NetworkConfig.RetryBackoffInitial*time.Duration(1<<uint(attempt)), config.NetworkConfig.RetryBackoffMax)

	// Add jitter (±20%)
	jitter := float64(backoff) * (0.8 + 0.4*rand.Float64())

	return time.Duration(jitter)
}

func validateRequest(req *schemas.BifrostRequest) *schemas.BifrostError {
	if req == nil {
		return newBifrostErrorFromMsg("bifrost request cannot be nil")
	}

	if req.Provider == "" {
		return newBifrostErrorFromMsg("provider is required")
	}

	if req.Model == "" {
		return newBifrostErrorFromMsg("model is required")
	}

	return nil
}

// newBifrostError wraps a standard error into a BifrostError with IsBifrostError set to false.
// This helper function reduces code duplication when handling non-Bifrost errors.
func newBifrostError(err error) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: schemas.ErrorField{
			Message: err.Error(),
			Error:   err,
		},
	}
}

// newBifrostErrorFromMsg creates a BifrostError with a custom message.
// This helper function is used for static error messages.
func newBifrostErrorFromMsg(message string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: schemas.ErrorField{
			Message: message,
		},
	}
}
