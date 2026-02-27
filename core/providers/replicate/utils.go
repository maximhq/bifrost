package replicate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// isTerminalStatus checks if a prediction status is terminal (completed/failed/canceled)
func isTerminalStatus(status ReplicatePredictionStatus) bool {
	return status == ReplicatePredictionStatusSucceeded ||
		status == ReplicatePredictionStatusFailed ||
		status == ReplicatePredictionStatusCanceled
}

// checkForErrorStatus returns an error if the prediction failed
func checkForErrorStatus(prediction *ReplicatePredictionResponse) *schemas.BifrostError {
	if prediction.Status == ReplicatePredictionStatusFailed {
		errorMsg := "prediction failed"
		if prediction.Error != nil && *prediction.Error != "" {
			errorMsg = *prediction.Error
		}
		return providerUtils.NewBifrostOperationError(
			"prediction failed",
			fmt.Errorf("%s", errorMsg),
			schemas.Replicate,
		)
	}

	if prediction.Status == ReplicatePredictionStatusCanceled {
		return providerUtils.NewBifrostOperationError(
			"prediction was canceled",
			fmt.Errorf("prediction was canceled"),
			schemas.Replicate,
		)
	}

	return nil
}

// parsePreferHeader parses the Prefer header to extract wait duration
// Examples: "wait", "wait=30", "wait=60"
// Returns the header value to use and whether sync mode should be enabled
func parsePreferHeader(extraHeaders map[string]string) bool {
	if preferValue, exists := extraHeaders["Prefer"]; exists {
		if strings.HasPrefix(preferValue, "wait") {
			return true
		}
		return false
	}
	return false
}

// Streaming requests should always be async and not wait for completion,
// so the Prefer header (which enables sync mode) must be excluded.
func stripPreferHeader(extraHeaders map[string]string) map[string]string {
	if extraHeaders == nil {
		return nil
	}

	// Check if Prefer header exists
	if _, exists := extraHeaders["Prefer"]; !exists {
		// No Prefer header, return original map
		return extraHeaders
	}

	// Create new map without Prefer header
	filtered := make(map[string]string, len(extraHeaders)-1)
	for key, value := range extraHeaders {
		if key != "Prefer" {
			filtered[key] = value
		}
	}

	return filtered
}

// listenToReplicateStreamURL listens to a Replicate stream URL and processes SSE events.
// This is a reusable utility for any Replicate streaming endpoint.
// It returns the response body stream (as io.Reader) and any error that occurred during connection.
func listenToReplicateStreamURL(
	client *fasthttp.Client,
	streamURL string,
	key schemas.Key,
) (io.Reader, *fasthttp.Response, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true

	// Set URL and headers
	req.SetRequestURI(streamURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Set authorization header
	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}

	// Make request
	err := client.Do(req, resp)
	fasthttp.ReleaseRequest(req)

	if err != nil {
		providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(err, context.Canceled) {
			return nil, nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, schemas.Replicate)
		}
		return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, schemas.Replicate)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, nil, parseReplicateError(resp.Body(), resp.StatusCode())
	}

	return resp.BodyStream(), resp, nil
}

// parseDataURIImage extracts the base64 data from a data URI
// Example: "data:image/webp;base64,UklGRmSu..." -> "UklGRmSu..."
func parseDataURIImage(dataURI string) (base64Data string, mimeType string) {
	// Format: data:image/webp;base64,<base64-data>
	if !strings.HasPrefix(dataURI, "data:") {
		return dataURI, "" // Return as-is if not a data URI
	}

	// Split by comma to separate metadata and data
	parts := strings.SplitN(dataURI[len("data:"):], ",", 2)
	if len(parts) != 2 {
		return dataURI, ""
	}

	// Parse MIME type from metadata (e.g., "image/webp;base64")
	metadata := parts[0]
	metaParts := strings.Split(metadata, ";")
	if len(metaParts) > 0 {
		mimeType = metaParts[0]
	}

	// Return the base64 data
	return parts[1], mimeType
}

// versionIDPattern matches a 64-character hexadecimal string (Replicate version ID format)
var versionIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// isVersionID checks if a string is a Replicate version ID (64-character hex string)
func isVersionID(s string) bool {
	return versionIDPattern.MatchString(s)
}

// resolveDeploymentModel checks if the model maps to a deployment.
// Returns the resolved model and whether it is a deployment.
func resolveDeploymentModel(model string, key schemas.Key) (string, bool) {
	if key.ReplicateKeyConfig == nil || key.ReplicateKeyConfig.Deployments == nil {
		return model, false
	}
	if deployment, ok := key.ReplicateKeyConfig.Deployments[model]; ok && deployment != "" {
		return deployment, true
	}
	return model, false
}

// buildPredictionURL builds the appropriate URL for creating a prediction
// Returns the URL for the appropriate prediction endpoint.
func buildPredictionURL(ctx *schemas.BifrostContext, baseURL, model string, customProviderConfig *schemas.CustomProviderConfig, requestType schemas.RequestType, isDeployment bool) string {
	var defaultPath string

	if isDeployment {
		defaultPath = "/v1/deployments/" + model + "/predictions"
	} else if isVersionID(model) {
		// If model is a version ID, use base predictions endpoint
		defaultPath = "/v1/predictions"
	} else {
		// If model is a name (owner/name), use model-specific endpoint
		defaultPath = "/v1/models/" + model + "/predictions"
	}

	path, isCompleteURL := providerUtils.GetRequestPath(ctx, defaultPath, customProviderConfig, requestType)
	if isCompleteURL {
		return path
	}
	return baseURL + path
}

// parseTokenUsageFromLogs extracts token counts from Replicate's logs field
// Handles multiple log formats with varying levels of detail
func parseTokenUsageFromLogs(logs *string, requestType schemas.RequestType) (inputTokens, outputTokens, totalTokens int, found bool) {
    if logs == nil || *logs == "" {
        return 0, 0, 0, false
    }
    
    logText := *logs
    foundAny := false
    
    // Pattern 1: Detailed format with input/output breakdown
    // "Input token count: 20"
    // "Input text token count: 15"
    inputPatterns := []string{
        `Input token count:\s*(\d+)`,
        `Input text token count:\s*(\d+)`,
    }
    for _, pattern := range inputPatterns {
        if matches := regexp.MustCompile(pattern).FindStringSubmatch(logText); len(matches) > 1 {
            if val, err := strconv.Atoi(matches[1]); err == nil {
                inputTokens = val
                foundAny = true
                break
            }
        }
    }
    
    // "Input image token count: 0" (for image generation)
    if matches := regexp.MustCompile(`Input image token count:\s*(\d+)`).FindStringSubmatch(logText); len(matches) > 1 {
        if val, err := strconv.Atoi(matches[1]); err == nil {
            inputTokens += val // Add to text input tokens
            foundAny = true
        }
    }
    
    // "Output token count: 28"
    if matches := regexp.MustCompile(`Output token count:\s*(\d+)`).FindStringSubmatch(logText); len(matches) > 1 {
        if val, err := strconv.Atoi(matches[1]); err == nil {
            outputTokens = val
            foundAny = true
        }
    }
    
    // "Total token count: 48"
    if matches := regexp.MustCompile(`Total token count:\s*(\d+)`).FindStringSubmatch(logText); len(matches) > 1 {
        if val, err := strconv.Atoi(matches[1]); err == nil {
            totalTokens = val
            foundAny = true
        }
    }
    
    // Pattern 2: Simple "Tokens: X" format (ambiguous - need heuristic)
    // Only use if detailed format not found
    if !foundAny {
        if matches := regexp.MustCompile(`Tokens:\s*(\d+)`).FindStringSubmatch(logText); len(matches) > 1 {
            if val, err := strconv.Atoi(matches[1]); err == nil {
                // Heuristic based on response type
                switch requestType {
                case schemas.ImageGenerationRequest:
                    // For image generation, "Tokens: X" typically means output tokens
                    outputTokens = val
                    totalTokens = val
                case schemas.TextCompletionRequest, schemas.ChatCompletionRequest, schemas.ResponsesRequest:
                    // For text, unclear - could be total or output
                    // Conservative approach: treat as total tokens
                    totalTokens = val
                default:
                    // Unknown type - treat as total
                    totalTokens = val
                }
                foundAny = true
            }
        }
    }
    
    // If we found input/output but not total, compute it
    if foundAny && totalTokens == 0 {
        totalTokens = inputTokens + outputTokens
    }
    
    return inputTokens, outputTokens, totalTokens, foundAny
}
