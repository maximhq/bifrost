package replicate

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// modelInputImageFieldMap maps model identifiers to their input image field names.
var modelInputImageFieldMap = map[string]string{
	// image_prompt models
	"black-forest-labs/flux-1.1-pro":                 "image_prompt",
	"black-forest-labs/flux-1.1-pro-ultra":           "image_prompt",
	"black-forest-labs/flux-pro":                     "image_prompt",
	"black-forest-labs/flux-1.1-pro-ultra-finetuned": "image_prompt",

	// input_image models (kontext variants)
	"black-forest-labs/flux-kontext-pro": "input_image",
	"black-forest-labs/flux-kontext-max": "input_image",
	"black-forest-labs/flux-kontext-dev": "input_image",

	// image models
	"black-forest-labs/flux-dev":      "image",
	"black-forest-labs/flux-fill-pro": "image",
	"black-forest-labs/flux-dev-lora": "image",
	"black-forest-labs/flux-krea-dev": "image",
}

// ToReplicateImageGenerationInput converts a Bifrost image generation request to Replicate prediction input
func ToReplicateImageGenerationInput(bifrostReq *schemas.BifrostImageGenerationRequest) *ReplicatePredictionRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	input := &ReplicatePredictionRequestInput{
		Prompt: &bifrostReq.Input.Prompt,
	}

	// Map parameters if available
	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		// Map InputImages to the appropriate field based on model
		if len(params.InputImages) > 0 {
			fieldName := getInputImageFieldName(bifrostReq.Model)

			switch fieldName {
			case "image_prompt":
				// For flux-1.1-pro variants: use first image as image_prompt
				input.ImagePrompt = &params.InputImages[0]

			case "input_image":
				// For flux-kontext variants: add to ExtraParams as input_image
				input.InputImage = &params.InputImages[0]

			case "image":
				// For flux-dev variants: use first image as image field
				input.Image = &params.InputImages[0]

			case "input_images":
				// For all other models: use input_images array
				input.InputImages = params.InputImages
			}
		}

		if bifrostReq.Params.N != nil {
			input.NumberOfImages = bifrostReq.Params.N
		}

		if params.AspectRatio != nil {
			input.AspectRatio = params.AspectRatio
		}

		if params.Resolution != nil {
			input.Resolution = params.Resolution
		}

		// Map OutputFormat
		if params.OutputFormat != nil {
			input.OutputFormat = params.OutputFormat
		}

		if params.Quality != nil {
			input.Quality = params.Quality
		}

		if params.Background != nil {
			input.Background = params.Background
		}

		// Map Seed
		if params.Seed != nil {
			input.Seed = params.Seed
		}

		// Map NegativePrompt
		if params.NegativePrompt != nil {
			input.NegativePrompt = params.NegativePrompt
		}

		// Map NumInferenceSteps
		if params.NumInferenceSteps != nil {
			input.NumInferenceStep = params.NumInferenceSteps
		}

		if params.ExtraParams != nil {
			input.ExtraParams = params.ExtraParams
		}
	}

	request := &ReplicatePredictionRequest{
		Input: input,
	}

	// Check if model is a version ID and set version field accordingly
	if isVersionID(bifrostReq.Model) {
		request.Version = &bifrostReq.Model
	}

	return request
}

// ToBifrostImageGenerationResponse converts a Replicate prediction response to Bifrost format
func ToBifrostImageGenerationResponse(
	prediction *ReplicatePredictionResponse,
) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if prediction == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "prediction response is nil",
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider: schemas.Replicate,
			},
		}
	}

	response := &schemas.BifrostImageGenerationResponse{
		ID:      prediction.ID,
		Created: ParseReplicateTimestamp(prediction.CreatedAt),
		Model:   prediction.Model,
		Data:    []schemas.ImageData{},
	}

	// Convert output to ImageData
	// Replicate output can be either a string (single URL) or array of strings
	if prediction.Output != nil {
		if prediction.Output.OutputStr != nil && *prediction.Output.OutputStr != "" {
			response.Data = append(response.Data, schemas.ImageData{
				URL:   *prediction.Output.OutputStr,
				Index: 0,
			})
		} else if len(prediction.Output.OutputArray) > 0 {
			for i, url := range prediction.Output.OutputArray {
				response.Data = append(response.Data, schemas.ImageData{
					URL:   url,
					Index: i,
				})
			}
		}
	}

	// Set usage if metrics are available
	if prediction.Metrics != nil {
		usage := &schemas.ImageUsage{}

		var inputTokens, outputTokens int
		if prediction.Metrics.InputTokenCount != nil {
			inputTokens = *prediction.Metrics.InputTokenCount
			usage.InputTokens = inputTokens
		}

		if prediction.Metrics.OutputTokenCount != nil {
			outputTokens = *prediction.Metrics.OutputTokenCount
			usage.OutputTokens = outputTokens
		}

		// Compute total whenever any token count exists
		if prediction.Metrics.InputTokenCount != nil || prediction.Metrics.OutputTokenCount != nil {
			usage.TotalTokens = inputTokens + outputTokens
		}

		response.Usage = usage
	}

	return response, nil
}

// getInputImageFieldName returns the appropriate input image field name based on the model.
// Uses O(1) map lookup for high RPS performance.
func getInputImageFieldName(model string) string {
	// Normalize model name to lowercase for comparison
	modelLower := strings.ToLower(model)

	// Extract model identifier (handle both "owner/name" and "owner/name:version" formats)
	modelIdentifier := modelLower
	if before, _, ok := strings.Cut(modelLower, ":"); ok {
		modelIdentifier = before
	}

	if fieldName, exists := modelInputImageFieldMap[modelIdentifier]; exists {
		return fieldName
	}

	// Default to input_images for all other models
	return "input_images"
}
