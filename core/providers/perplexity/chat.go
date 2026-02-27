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

		// Tool calling parameters
		perplexityReq.Tools = bifrostReq.Params.Tools
		perplexityReq.ToolChoice = bifrostReq.Params.ToolChoice
		perplexityReq.ParallelToolCalls = bifrostReq.Params.ParallelToolCalls

		// Standard parameters
		perplexityReq.Stop = bifrostReq.Params.Stop
		perplexityReq.LogProbs = bifrostReq.Params.LogProbs
		perplexityReq.TopLogProbs = bifrostReq.Params.TopLogProbs

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
			perplexityReq.ExtraParams = bifrostReq.Params.ExtraParams
			// Search-related parameters
			if searchMode, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["search_mode"]); ok {
				delete(perplexityReq.ExtraParams, "search_mode")
				perplexityReq.SearchMode = searchMode
			}

			if languagePreference, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["language_preference"]); ok {
				delete(perplexityReq.ExtraParams, "language_preference")
				perplexityReq.LanguagePreference = languagePreference
			}

			if searchDomainFilter, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["search_domain_filter"]); ok {
				delete(perplexityReq.ExtraParams, "search_domain_filter")
				perplexityReq.SearchDomainFilter = searchDomainFilter
			}

			if returnImages, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["return_images"]); ok {
				delete(perplexityReq.ExtraParams, "return_images")
				perplexityReq.ReturnImages = returnImages
			}

			if returnRelatedQuestions, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["return_related_questions"]); ok {
				delete(perplexityReq.ExtraParams, "return_related_questions")
				perplexityReq.ReturnRelatedQuestions = returnRelatedQuestions
			}

			if searchRecencyFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["search_recency_filter"]); ok {
				delete(perplexityReq.ExtraParams, "search_recency_filter")
				perplexityReq.SearchRecencyFilter = searchRecencyFilter
			}

			if searchAfterDateFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["search_after_date_filter"]); ok {
				delete(perplexityReq.ExtraParams, "search_after_date_filter")
				perplexityReq.SearchAfterDateFilter = searchAfterDateFilter
			}

			if searchBeforeDateFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["search_before_date_filter"]); ok {
				delete(perplexityReq.ExtraParams, "search_before_date_filter")
				perplexityReq.SearchBeforeDateFilter = searchBeforeDateFilter
			}

			if lastUpdatedAfterFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["last_updated_after_filter"]); ok {
				delete(perplexityReq.ExtraParams, "last_updated_after_filter")
				perplexityReq.LastUpdatedAfterFilter = lastUpdatedAfterFilter
			}

			if lastUpdatedBeforeFilter, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["last_updated_before_filter"]); ok {
				delete(perplexityReq.ExtraParams, "last_updated_before_filter")
				perplexityReq.LastUpdatedBeforeFilter = lastUpdatedBeforeFilter
			}

			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				delete(perplexityReq.ExtraParams, "top_k")
				perplexityReq.TopK = topK
			}

			if stream, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["stream"]); ok {
				delete(perplexityReq.ExtraParams, "stream")
				perplexityReq.Stream = stream
			}

			if disableSearch, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["disable_search"]); ok {
				delete(perplexityReq.ExtraParams, "disable_search")
				perplexityReq.DisableSearch = disableSearch
			}

			if enableSearchClassifier, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enable_search_classifier"]); ok {
				delete(perplexityReq.ExtraParams, "enable_search_classifier")
				perplexityReq.EnableSearchClassifier = enableSearchClassifier
			}

			// Perplexity-specific request fields
			if numSearchResults, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_search_results"]); ok {
				delete(perplexityReq.ExtraParams, "num_search_results")
				perplexityReq.NumSearchResults = numSearchResults
			}

			if numImages, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_images"]); ok {
				delete(perplexityReq.ExtraParams, "num_images")
				perplexityReq.NumImages = numImages
			}

			if searchLanguageFilter, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["search_language_filter"]); ok {
				delete(perplexityReq.ExtraParams, "search_language_filter")
				perplexityReq.SearchLanguageFilter = searchLanguageFilter
			}

			if imageFormatFilter, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["image_format_filter"]); ok {
				delete(perplexityReq.ExtraParams, "image_format_filter")
				perplexityReq.ImageFormatFilter = imageFormatFilter
			}

			if imageDomainFilter, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["image_domain_filter"]); ok {
				delete(perplexityReq.ExtraParams, "image_domain_filter")
				perplexityReq.ImageDomainFilter = imageDomainFilter
			}

			if safeSearch, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["safe_search"]); ok {
				delete(perplexityReq.ExtraParams, "safe_search")
				perplexityReq.SafeSearch = safeSearch
			}

			if streamMode, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["stream_mode"]); ok {
				delete(perplexityReq.ExtraParams, "stream_mode")
				perplexityReq.StreamMode = streamMode
			}

			// Handle web_search_options
			if webSearchOptionsParam, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "web_search_options"); ok {
				if webSearchOptionsSlice, ok := webSearchOptionsParam.([]interface{}); ok {
					var webSearchOptions []WebSearchOption
					updatedWebSearchOptionsSlice := make([]interface{}, 0, len(webSearchOptionsSlice))
					for _, optionInterface := range webSearchOptionsSlice {
						if optionMap, ok := optionInterface.(map[string]interface{}); ok {
							option := WebSearchOption{}

							if searchContextSize, ok := schemas.SafeExtractStringPointer(optionMap["search_context_size"]); ok {
								delete(optionMap, "search_context_size")
								option.SearchContextSize = searchContextSize
							}

							if imageResultsEnhancedRelevance, ok := schemas.SafeExtractBoolPointer(optionMap["image_results_enhanced_relevance"]); ok {
								delete(optionMap, "image_results_enhanced_relevance")
								option.ImageResultsEnhancedRelevance = imageResultsEnhancedRelevance
							}

							if searchType, ok := schemas.SafeExtractStringPointer(optionMap["search_type"]); ok {
								delete(optionMap, "search_type")
								option.SearchType = searchType
							}

							// Handle user_location
							if userLocationParam, ok := schemas.SafeExtractFromMap(optionMap, "user_location"); ok {
								if userLocationMap, ok := userLocationParam.(map[string]interface{}); ok {
									userLocation := &WebSearchOptionUserLocation{}

									if latitude, ok := schemas.SafeExtractFloat64Pointer(userLocationMap["latitude"]); ok {
										delete(userLocationMap, "latitude")
										userLocation.Latitude = latitude
									}
									if longitude, ok := schemas.SafeExtractFloat64Pointer(userLocationMap["longitude"]); ok {
										delete(userLocationMap, "longitude")
										userLocation.Longitude = longitude
									}
									if city, ok := schemas.SafeExtractStringPointer(userLocationMap["city"]); ok {
										delete(userLocationMap, "city")
										userLocation.City = city
									}
									if country, ok := schemas.SafeExtractStringPointer(userLocationMap["country"]); ok {
										delete(userLocationMap, "country")
										userLocation.Country = country
									}
									if region, ok := schemas.SafeExtractStringPointer(userLocationMap["region"]); ok {
										delete(userLocationMap, "region")
										userLocation.Region = region
									}
									if len(userLocationMap) == 0 {
										delete(optionMap, "user_location")
									} else {
										optionMap["user_location"] = userLocationMap
									}
									option.UserLocation = userLocation
								}
							}
							webSearchOptions = append(webSearchOptions, option)
							// Persist remaining custom fields from optionMap back to ExtraParams
							if len(optionMap) > 0 {
								updatedWebSearchOptionsSlice = append(updatedWebSearchOptionsSlice, optionMap)
							}
						} else {
							// Preserve non-map entries as-is
							updatedWebSearchOptionsSlice = append(updatedWebSearchOptionsSlice, optionInterface)
						}
					}
					perplexityReq.WebSearchOptions = webSearchOptions
					// Put remaining custom fields back into ExtraParams
					if len(updatedWebSearchOptionsSlice) > 0 {
						perplexityReq.ExtraParams["web_search_options"] = updatedWebSearchOptionsSlice
					} else {
						delete(perplexityReq.ExtraParams, "web_search_options")
					}
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
								delete(overridesMap, "return_videos")
								overrides.ReturnVideos = returnVideos
							}
							if returnImages, ok := schemas.SafeExtractBoolPointer(overridesMap["return_images"]); ok {
								delete(overridesMap, "return_images")
								overrides.ReturnImages = returnImages
							}
							// Put remaining overridesMap fields back into mediaResponseMap at correct nested location
							if len(overridesMap) > 0 {
								mediaResponseMap["overrides"] = overridesMap
							} else {
								delete(mediaResponseMap, "overrides")
							}
							mediaResponse.Overrides = overrides
						}
					}
					perplexityReq.ExtraParams["media_response"] = mediaResponseMap
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
		Citations:     response.Citations,
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
