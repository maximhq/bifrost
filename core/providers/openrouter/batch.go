package openrouter

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const openRouterBatchesPath = "/v1/batches"

// ToOpenRouterBatchRequest converts inline items without uploading an input file.
func ToOpenRouterBatchRequest(request *schemas.BifrostBatchCreateRequest) (*OpenRouterBatchRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("batch create request is nil")
	}
	if request.Model == nil || strings.TrimSpace(*request.Model) == "" {
		return nil, fmt.Errorf("model is required for OpenRouter batches")
	}
	switch request.Endpoint {
	case schemas.BatchEndpointChatCompletions, schemas.BatchEndpointResponses, schemas.BatchEndpointMessages, schemas.BatchEndpointEmbeddings:
	default:
		return nil, fmt.Errorf("unsupported OpenRouter batch endpoint %q", request.Endpoint)
	}
	if request.InputFileID != "" || request.InputBlob != nil || request.OutputFolder != nil {
		return nil, fmt.Errorf("OpenRouter batches require inline requests, not input files or blobs")
	}
	if len(request.Requests) == 0 {
		return nil, fmt.Errorf("requests must not be empty")
	}
	if len(request.Metadata) != 0 || request.DisplayName != nil || request.OutputExpiresAfter != nil {
		return nil, fmt.Errorf("OpenRouter batches do not support metadata, display_name, or output_expires_after")
	}
	window := request.CompletionWindow
	if window == "" {
		window = "24h"
	}
	if window != "24h" {
		return nil, fmt.Errorf("OpenRouter batch completion_window must be 24h")
	}
	native := &OpenRouterBatchRequest{Endpoint: request.Endpoint, Model: *request.Model, CompletionWindow: window, Requests: make([]schemas.BatchRequestItem, len(request.Requests))}
	seen := make(map[string]struct{}, len(request.Requests))
	for i, item := range request.Requests {
		if strings.TrimSpace(item.CustomID) == "" {
			return nil, fmt.Errorf("requests[%d].custom_id must not be empty", i)
		}
		if _, exists := seen[item.CustomID]; exists {
			return nil, fmt.Errorf("duplicate custom_id %q", item.CustomID)
		}
		seen[item.CustomID] = struct{}{}
		if item.Method != "" && item.Method != http.MethodPost {
			return nil, fmt.Errorf("request %q must use POST", item.CustomID)
		}
		if item.URL != "" && item.URL != string(request.Endpoint) {
			return nil, fmt.Errorf("request %q must use the batch endpoint", item.CustomID)
		}
		body := item.Body
		if body == nil {
			body = item.Params
		} else if item.Params != nil {
			return nil, fmt.Errorf("request %q must not specify both body and params", item.CustomID)
		}
		if len(body) == 0 {
			return nil, fmt.Errorf("request %q requires a body", item.CustomID)
		}
		if model, exists := body["model"]; exists && model != native.Model {
			return nil, fmt.Errorf("request %q model must match the batch model", item.CustomID)
		}
		native.Requests[i] = schemas.BatchRequestItem{CustomID: item.CustomID, Body: body}
	}
	for name, value := range request.ExtraParams {
		switch name {
		case "provider":
			options, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s must be a provider options object", name)
			}
			if request.Endpoint == schemas.BatchEndpointEmbeddings {
				return nil, fmt.Errorf("OpenRouter embeddings batches do not support provider options")
			}
			for option, setting := range options {
				switch option {
				case "only":
					switch slugs := setting.(type) {
					case []string:
						if len(slugs) == 0 {
							return nil, fmt.Errorf("provider.only must not be empty")
						}
						for _, slug := range slugs {
							if strings.TrimSpace(slug) == "" {
								return nil, fmt.Errorf("provider.only requires nonempty strings")
							}
						}
					case []any:
						if len(slugs) == 0 {
							return nil, fmt.Errorf("provider.only must not be empty")
						}
						for _, slug := range slugs {
							if s, ok := slug.(string); !ok || strings.TrimSpace(s) == "" {
								return nil, fmt.Errorf("provider.only requires nonempty strings")
							}
						}
					default:
						return nil, fmt.Errorf("provider.only must be an array of provider slugs")
					}
				case "allow_fallbacks":
					if _, ok := setting.(bool); !ok {
						return nil, fmt.Errorf("provider.allow_fallbacks must be a boolean")
					}
				default:
					return nil, fmt.Errorf("unsupported OpenRouter batch provider option %q", option)
				}
			}
			native.Provider = options
		case "callback_url":
			callback, ok := value.(string)
			parsed, err := url.Parse(callback)
			if !ok || err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				return nil, fmt.Errorf("callback_url must be an HTTPS URL")
			}
			native.CallbackURL = callback
		default:
			return nil, fmt.Errorf("unsupported OpenRouter batch parameter %q", name)
		}
	}
	return native, nil
}
