package bifrost

import (
	"context"
	"math/rand"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Ptr returns a pointer to the given value.
func Ptr[T any](v T) *T {
	return &v
}

// providerRequiresKey returns true if the given provider requires an API key for authentication.
// Some providers like Ollama and SGL are keyless and don't require API keys.
func providerRequiresKey(providerKey schemas.ModelProvider) bool {
	return providerKey != schemas.Ollama && providerKey != schemas.SGL
}

// canProviderKeyValueBeEmpty returns true if the given provider allows the API key to be empty.
// Some providers like Vertex and Bedrock have their credentials in additional key configs..
func canProviderKeyValueBeEmpty(providerKey schemas.ModelProvider) bool {
	return providerKey == schemas.Vertex || providerKey == schemas.Bedrock
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
	provider, model, _ := req.GetRequestFields()
	if provider == "" {
		return newBifrostErrorFromMsg("provider is required")
	}
	if model == "" {
		return newBifrostErrorFromMsg("model is required")
	}

	return nil
}

// newBifrostError wraps a standard error into a BifrostError with IsBifrostError set to false.
// This helper function reduces code duplication when handling non-Bifrost errors.
func newBifrostError(err error) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
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
		Error: &schemas.ErrorField{
			Message: message,
		},
	}
}

// newBifrostMessageChan creates a channel that sends a bifrost response.
// It is used to send a bifrost response to the client.
func newBifrostMessageChan(message *schemas.BifrostResponse) chan *schemas.BifrostStream {
	ch := make(chan *schemas.BifrostStream)

	go func() {
		defer close(ch)
		ch <- &schemas.BifrostStream{
			BifrostTextCompletionResponse:      message.TextCompletionResponse,
			BifrostChatResponse:                message.ChatResponse,
			BifrostResponsesStreamResponse:     message.ResponsesStreamResponse,
			BifrostSpeechStreamResponse:        message.SpeechStreamResponse,
			BifrostTranscriptionStreamResponse: message.TranscriptionStreamResponse,
		}
	}()

	return ch
}

var supportedBaseProvidersSet = func() map[schemas.ModelProvider]struct{} {
	m := make(map[schemas.ModelProvider]struct{}, len(schemas.SupportedBaseProviders))
	for _, p := range schemas.SupportedBaseProviders {
		m[p] = struct{}{}
	}
	return m
}()

// IsSupportedBaseProvider reports whether providerKey is allowed as a base provider
// for custom providers.
func IsSupportedBaseProvider(providerKey schemas.ModelProvider) bool {
	_, ok := supportedBaseProvidersSet[providerKey]
	return ok
}

var standardProvidersSet = func() map[schemas.ModelProvider]struct{} {
	m := make(map[schemas.ModelProvider]struct{}, len(schemas.StandardProviders))
	for _, p := range schemas.StandardProviders {
		m[p] = struct{}{}
	}
	return m
}()

// IsStandardProvider reports whether providerKey is a built-in (non-custom) provider.
func IsStandardProvider(providerKey schemas.ModelProvider) bool {
	_, ok := standardProvidersSet[providerKey]
	return ok
}

// IsStreamRequestType returns true if the given request type is a stream request.
func IsStreamRequestType(reqType schemas.RequestType) bool {
	return reqType == schemas.TextCompletionStreamRequest || reqType == schemas.ChatCompletionStreamRequest || reqType == schemas.ResponsesStreamRequest || reqType == schemas.SpeechStreamRequest || reqType == schemas.TranscriptionStreamRequest
}

func IsFinalChunk(ctx *context.Context) bool {
	if ctx == nil {
		return false
	}

	isStreamEndIndicator := (*ctx).Value(schemas.BifrostContextKeyStreamEndIndicator)
	if isStreamEndIndicator == nil {
		return false
	}

	if f, ok := isStreamEndIndicator.(bool); ok {
		return f
	}

	return false
}

// GetResponseFields extracts the request type, provider, and model from the result or error
func GetResponseFields(result *schemas.BifrostResponse, err *schemas.BifrostError) (requestType schemas.RequestType, provider schemas.ModelProvider, model string) {
	if result != nil {
		extraFields := result.GetExtraFields()
		return extraFields.RequestType, extraFields.Provider, extraFields.ModelRequested
	}

	return err.ExtraFields.RequestType, err.ExtraFields.Provider, err.ExtraFields.ModelRequested
}
