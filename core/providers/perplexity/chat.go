package perplexity

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToPerplexityChatCompletionRequest converts a Bifrost request to Perplexity chat completion request
func ToPerplexityChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) *PerplexityChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	messages := bifrostReq.Input
	perplexityReq := &PerplexityChatRequest{
		Model:    bifrostReq.Model,
		Messages: messages,
	}

	// Map parameters if they exist
	if bifrostReq.Params != nil {
		// Core parameters
		perplexityReq.MaxTokens = bifrostReq.Params.MaxCompletionTokens
		perplexityReq.Temperature = bifrostReq.Params.Temperature
		perplexityReq.TopP = bifrostReq.Params.TopP
		perplexityReq.PresencePenalty = bifrostReq.Params.PresencePenalty
		perplexityReq.FrequencyPenalty = bifrostReq.Params.FrequencyPenalty
		perplexityReq.ResponseFormat = bifrostReq.Params.ResponseFormat

		// Handle reasoning effort mapping
		if bifrostReq.Params.Reasoning != nil && bifrostReq.Params.Reasoning.Effort != nil {
			if *bifrostReq.Params.Reasoning.Effort == "minimal" {
				perplexityReq.ReasoningEffort = schemas.Ptr("low")
			} else {
				perplexityReq.ReasoningEffort = bifrostReq.Params.Reasoning.Effort
			}
		}

		// Handle extra parameters for Perplexity-specific fields
		if bifrostReq.Params.ExtraParams != nil {
			// Search-related parameters
			if searchMode, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["search_mode"]); ok {
				perplexityReq.SearchMode = searchMode
			}

			if languagePreference, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["language_preference"]); ok {
				perplexityReq.LanguagePreference = languagePreference
			}

			if searchDomainFilter, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["search_domain_filter"]); ok {
				perplexityReq.SearchDomainFilter = searchDomainFilter
			}

			if returnImages, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["return_images"]); ok {
				perplexityReq.ReturnImages = returnImages
			}

			if returnRelatedQuestions, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["return_related_questions"]); ok {
				perplexityReq.ReturnRelatedQuestions = returnRelatedQuestions
			}

			if searchRecencyFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["search_recency_filter"]); ok {
				perplexityReq.SearchRecencyFilter = searchRecencyFilter
			}

			if searchAfterDateFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["search_after_date_filter"]); ok {
				perplexityReq.SearchAfterDateFilter = searchAfterDateFilter
			}

			if searchBeforeDateFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["search_before_date_filter"]); ok {
				perplexityReq.SearchBeforeDateFilter = searchBeforeDateFilter
			}

			if lastUpdatedAfterFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["last_updated_after_filter"]); ok {
				perplexityReq.LastUpdatedAfterFilter = lastUpdatedAfterFilter
			}

			if lastUpdatedBeforeFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["last_updated_before_filter"]); ok {
				perplexityReq.LastUpdatedBeforeFilter = lastUpdatedBeforeFilter
			}

			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				perplexityReq.TopK = topK
			}

			if stream, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["stream"]); ok {
				perplexityReq.Stream = stream
			}

			if disableSearch, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["disable_search"]); ok {
				perplexityReq.DisableSearch = disableSearch
			}

			if enableSearchClassifier, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enable_search_classifier"]); ok {
				perplexityReq.EnableSearchClassifier = enableSearchClassifier
			}

			// Handle web_search_options
			if webSearchOptionsParam, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "web_search_options"); ok {
				if webSearchOptionsSlice, ok := webSearchOptionsParam.([]interface{}); ok {
					var webSearchOptions []WebSearchOption
					for _, optionInterface := range webSearchOptionsSlice {
						if optionMap, ok := optionInterface.(map[string]interface{}); ok {
							option := WebSearchOption{}

							if searchContextSize, ok := schemas.SafeExtractStringPointer(optionMap["search_context_size"]); ok {
								option.SearchContextSize = searchContextSize
							}

							if imageSearchRelevanceEnhanced, ok := schemas.SafeExtractBoolPointer(optionMap["image_search_relevance_enhanced"]); ok {
								option.ImageSearchRelevanceEnhanced = imageSearchRelevanceEnhanced
							}

							// Handle user_location
							if userLocationParam, ok := schemas.SafeExtractFromMap(optionMap, "user_location"); ok {
								if userLocationMap, ok := userLocationParam.(map[string]interface{}); ok {
									userLocation := &WebSearchOptionUserLocation{}

									if latitude, ok := schemas.SafeExtractFloat64Pointer(userLocationMap["latitude"]); ok {
										userLocation.Latitude = latitude
									}
									if longitude, ok := schemas.SafeExtractFloat64Pointer(userLocationMap["longitude"]); ok {
										userLocation.Longitude = longitude
									}
									if city, ok := schemas.SafeExtractStringPointer(userLocationMap["city"]); ok {
										userLocation.City = city
									}
									if country, ok := schemas.SafeExtractStringPointer(userLocationMap["country"]); ok {
										userLocation.Country = country
									}
									if region, ok := schemas.SafeExtractStringPointer(userLocationMap["region"]); ok {
										userLocation.Region = region
									}

									option.UserLocation = userLocation
								}
							}

							webSearchOptions = append(webSearchOptions, option)
						}
					}
					perplexityReq.WebSearchOptions = webSearchOptions
				}
			}

			// Handle media_response
			if mediaResponseParam, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "media_response"); ok {
				if mediaResponseMap, ok := mediaResponseParam.(map[string]interface{}); ok {
					mediaResponse := &MediaResponse{}

					if overridesParam, ok := schemas.SafeExtractFromMap(mediaResponseMap, "overrides"); ok {
						if overridesMap, ok := overridesParam.(map[string]interface{}); ok {
							overrides := MediaResponseOverrides{}

							if returnVideos, ok := schemas.SafeExtractBoolPointer(overridesMap["return_videos"]); ok {
								overrides.ReturnVideos = returnVideos
							}
							if returnImages, ok := schemas.SafeExtractBoolPointer(overridesMap["return_images"]); ok {
								overrides.ReturnImages = returnImages
							}

							mediaResponse.Overrides = overrides
						}
					}

					perplexityReq.MediaResponse = mediaResponse
				}
			}
		}
	}

	return perplexityReq
}

// ToBifrostChatResponse converts a Perplexity chat completion response to Bifrost format
func (response *PerplexityChatResponse) ToBifrostChatResponse(model string) *schemas.BifrostChatResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostChatResponse{
		ID:      response.ID,
		Model:   model,
		Object:  response.Object,
		Created: response.Created,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.Perplexity,
		},
		SearchResults: response.SearchResults,
		Videos:        response.Videos,
	}

	// Map all response fields
	if len(response.Choices) > 0 {
		bifrostResponse.Choices = response.Choices
	}

	// Convert usage information with all available fields
	if response.Usage != nil {
		usage := &schemas.BifrostLLMUsage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
		}

		// Map Perplexity-specific usage details to CompletionTokensDetails
		completionDetails := &schemas.ChatCompletionTokensDetails{}
		hasCompletionDetails := false

		if response.Usage.CitationTokens != nil {
			completionDetails.CitationTokens = response.Usage.CitationTokens
			hasCompletionDetails = true
		}

		if response.Usage.NumSearchQueries != nil {
			completionDetails.NumSearchQueries = response.Usage.NumSearchQueries
			hasCompletionDetails = true
		}

		if response.Usage.ReasoningTokens != nil {
			completionDetails.ReasoningTokens = *response.Usage.ReasoningTokens
			hasCompletionDetails = true
		}

		if hasCompletionDetails {
			usage.CompletionTokensDetails = completionDetails
		}

		if response.Usage.Cost != nil {
			usage.Cost = response.Usage.Cost
		}

		bifrostResponse.Usage = usage
	}

	return bifrostResponse
}
