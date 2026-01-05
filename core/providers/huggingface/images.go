package huggingface

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	nebiusProvider "github.com/maximhq/bifrost/core/providers/nebius"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToHuggingFaceImageGenerationRequest converts a Bifrost image generation request to provider-specific format
func ToHuggingFaceImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) (any, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, fmt.Errorf("bifrost request is nil or input is nil")
	}

	inferenceProvider, model, nameErr := splitIntoModelProvider(bifrostReq.Model)
	if nameErr != nil {
		return nil, nameErr
	}

	switch inferenceProvider {
	case nebius:
		req := &nebiusProvider.NebiusImageGenerationRequest{
			Model:  &model,
			Prompt: &bifrostReq.Input.Prompt,
		}

		if bifrostReq.Params != nil {
			if bifrostReq.Params.ResponseFormat != nil {
				req.ResponseFormat = bifrostReq.Params.ResponseFormat
			}

			if bifrostReq.Params.Size != nil && strings.ToLower(*bifrostReq.Params.Size) != "auto" {
				size := strings.Split(strings.ToLower(*bifrostReq.Params.Size), "x")
				if len(size) != 2 {
					return nil, fmt.Errorf("invalid size format: expected 'WIDTHxHEIGHT', got %q", *bifrostReq.Params.Size)
				}

				width, err := strconv.Atoi(size[0])
				if err != nil {
					return nil, fmt.Errorf("invalid width in size %q: %w", *bifrostReq.Params.Size, err)
				}

				height, err := strconv.Atoi(size[1])
				if err != nil {
					return nil, fmt.Errorf("invalid height in size %q: %w", *bifrostReq.Params.Size, err)
				}

				req.Width = &width
				req.Height = &height
			}
			if bifrostReq.Params.OutputFormat != nil {
				req.ResponseExtension = bifrostReq.Params.OutputFormat
			}

			// Handle nebius inconsistency - normalize ResponseExtension case-insensitively
			if req.ResponseExtension != nil && strings.ToLower(*req.ResponseExtension) == "jpeg" {
				req.ResponseExtension = schemas.Ptr("jpg")
			}

			// Map seed from direct field
			if bifrostReq.Params.Seed != nil {
				req.Seed = bifrostReq.Params.Seed
			}

			// Map negative_prompt from direct field
			if bifrostReq.Params.NegativePrompt != nil {
				req.NegativePrompt = bifrostReq.Params.NegativePrompt
			}

			// Handle extra params for nebius
			if bifrostReq.Params.ExtraParams != nil {
				// Map num_inference_steps
				if v, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_inference_steps"]); ok {
					req.NumInferenceSteps = v
				}

				// Map guidance_scale
				if v, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["guidance_scale"]); ok {
					req.GuidanceScale = v
				}

				// Map loras
				if lorasValue, exists := bifrostReq.Params.ExtraParams["loras"]; exists && lorasValue != nil {
					if lorasArray, ok := lorasValue.([]interface{}); ok {
						for _, item := range lorasArray {
							if loraMap, ok := item.(map[string]interface{}); ok {
								if url, ok := schemas.SafeExtractString(loraMap["url"]); ok {
									if scale, ok := schemas.SafeExtractInt(loraMap["scale"]); ok {
										req.Loras = append(req.Loras, nebiusProvider.NebiusLora{URL: url, Scale: scale})
									}
								}
							}
						}
					}
				}
			}
		}
		return req, nil

	case hfInference:
		return &HuggingFaceHFInferenceImageGenerationRequest{
			Inputs: bifrostReq.Input.Prompt,
		}, nil

	case falAI:
		req := &HuggingFaceFalAIImageGenerationRequest{
			Prompt: bifrostReq.Input.Prompt,
		}

		if bifrostReq.Params != nil {
			// Map n to num_images for fal-ai
			if bifrostReq.Params.N != nil {
				req.NumImages = bifrostReq.Params.N
			}

			// Pass through response_format
			if bifrostReq.Params.ResponseFormat != nil {
				req.ResponseFormat = bifrostReq.Params.ResponseFormat
			}

			// Pass through output_format
			if bifrostReq.Params.OutputFormat != nil {
				if strings.ToLower(*bifrostReq.Params.OutputFormat) == "jpg" {
					req.OutputFormat = schemas.Ptr("jpeg")
				} else {
					req.OutputFormat = bifrostReq.Params.OutputFormat
				}
			}

			// Convert size from "WxH" format to fal-ai's image_size object
			if bifrostReq.Params.Size != nil && strings.ToLower(*bifrostReq.Params.Size) != "auto" {
				size := strings.Split(*bifrostReq.Params.Size, "x")
				if len(size) == 2 {
					width, err := strconv.Atoi(size[0])
					if err == nil {
						height, err := strconv.Atoi(size[1])
						if err == nil {
							req.ImageSize = &HuggingFaceFalAISize{
								Width:  width,
								Height: height,
							}
						}
					}
				}
			}

			if bifrostReq.Params.ResponseFormat != nil && *bifrostReq.Params.ResponseFormat == "b64_json" {
				req.SyncMode = schemas.Ptr(true)
			}

			if bifrostReq.Params.Moderation != nil && *bifrostReq.Params.Moderation == "low" {
				req.EnableSafetyChecker = schemas.Ptr(false)
			}

			// Map seed from direct field
			if bifrostReq.Params.Seed != nil {
				req.Seed = bifrostReq.Params.Seed
			}

			// Map negative_prompt from direct field
			if bifrostReq.Params.NegativePrompt != nil {
				req.NegativePrompt = bifrostReq.Params.NegativePrompt
			}

			// Map num_inference_steps from direct field
			if bifrostReq.Params.NumInferenceSteps != nil {
				req.NumInferenceSteps = bifrostReq.Params.NumInferenceSteps
			}

			// Parse fal-ai specific params from ExtraParams
			if bifrostReq.Params.ExtraParams != nil {
				// Map guidance_scale
				if v, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["guidance_scale"]); ok {
					req.GuidanceScale = v
				}

				// Map acceleration
				if v, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["acceleration"]); ok {
					req.Acceleration = v
				}

				// Map enable_prompt_expansion
				if v, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enable_prompt_expansion"]); ok {
					req.EnablePromptExpansion = v
				}

				// Map enable_safety_checker
				if v, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enable_safety_checker"]); ok {
					req.EnableSafetyChecker = v
				}
			}
		}
		return req, nil

	case together:
		req := &HuggingFaceTogetherImageGenerationRequest{
			Prompt: bifrostReq.Input.Prompt,
			Model:  model,
		}

		if bifrostReq.Params != nil {
			if bifrostReq.Params.ResponseFormat != nil {
				req.ResponseFormat = bifrostReq.Params.ResponseFormat
			}

			if bifrostReq.Params.Size != nil {
				req.Size = bifrostReq.Params.Size
			}

			if bifrostReq.Params.N != nil {
				req.N = bifrostReq.Params.N
			}
			if bifrostReq.Params.ResponseFormat != nil && *bifrostReq.Params.ResponseFormat == "b64_json" {
				req.ResponseFormat = schemas.Ptr("base64")
			}
			if bifrostReq.Params.NumInferenceSteps != nil {
				req.Steps = bifrostReq.Params.NumInferenceSteps
			}
		}
		return req, nil

	default:
		return nil, fmt.Errorf("unsupported inference provider for image generation: %s", inferenceProvider)
	}
}

// ToHuggingFaceImageStreamRequest converts a Bifrost image generation request to fal-ai streaming format
func ToHuggingFaceImageStreamRequest(bifrostReq *schemas.BifrostImageGenerationRequest) (*HuggingFaceFalAIImageStreamRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, fmt.Errorf("bifrost request is nil or input is nil")
	}

	req := &HuggingFaceFalAIImageStreamRequest{
		Prompt: bifrostReq.Input.Prompt,
	}

	if bifrostReq.Params != nil {
		// Map n to num_images for fal-ai
		if bifrostReq.Params.N != nil {
			req.NumImages = bifrostReq.Params.N
		}

		// Pass through response_format
		if bifrostReq.Params.ResponseFormat != nil {
			req.ResponseFormat = bifrostReq.Params.ResponseFormat
		}

		// Pass through output_format
		// Convert "jpg" to "jpeg" for fal-ai (fal-ai only accepts "jpeg", "png", "webp")
		if bifrostReq.Params.OutputFormat != nil {
			if strings.ToLower(*bifrostReq.Params.OutputFormat) == "jpg" {
				req.OutputFormat = schemas.Ptr("jpeg")
			} else {
				req.OutputFormat = bifrostReq.Params.OutputFormat
			}
		}

		// Convert size from "WxH" format to fal-ai's image_size object
		if bifrostReq.Params.Size != nil && strings.ToLower(*bifrostReq.Params.Size) != "auto" {
			size := strings.Split(*bifrostReq.Params.Size, "x")
			if len(size) == 2 {
				width, err := strconv.Atoi(size[0])
				if err == nil {
					height, err := strconv.Atoi(size[1])
					if err == nil {
						req.ImageSize = &HuggingFaceFalAISize{
							Width:  width,
							Height: height,
						}
					}
				}
			}
		}
		if bifrostReq.Params.Seed != nil {
			req.Seed = bifrostReq.Params.Seed
		}
		if bifrostReq.Params.NumInferenceSteps != nil {
			req.NumInferenceSteps = bifrostReq.Params.NumInferenceSteps
		}
		if bifrostReq.Params.ResponseFormat != nil && *bifrostReq.Params.ResponseFormat == "b64_json" {
			req.SyncMode = schemas.Ptr(true)
		}
		if bifrostReq.Params.Moderation != nil && *bifrostReq.Params.Moderation == "low" {
			req.EnableSafetyChecker = schemas.Ptr(false)
		}

		// Parse fal-ai specific params from ExtraParams
		if bifrostReq.Params.ExtraParams != nil {
			if v, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["guidance_scale"]); ok {
				req.GuidanceScale = v
			}
			if v, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["acceleration"]); ok {
				req.Acceleration = v
			}
			if v, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enable_prompt_expansion"]); ok {
				req.EnablePromptExpansion = v
			}
			if v, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enable_safety_checker"]); ok {
				req.EnableSafetyChecker = v
			}
		}
	}

	return req, nil
}

// UnmarshalHuggingFaceImageGenerationResponse unmarshals HuggingFace image generation response to Bifrost format
func UnmarshalHuggingFaceImageGenerationResponse(data []byte, model string) (*schemas.BifrostImageGenerationResponse, error) {
	if data == nil {
		return nil, fmt.Errorf("response data is nil")
	}
	inferenceProvider, _, err := splitIntoModelProvider(model)
	if err != nil {
		return nil, err
	}

	switch inferenceProvider {
	case nebius:
		// Unmarshal into Nebius response format
		var nebiusResponse nebiusProvider.NebiusImageGenerationResponse
		if err := sonic.Unmarshal(data, &nebiusResponse); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Nebius response: %w", err)
		}

		// Convert to Bifrost format using Nebius converter
		bifrostResponse := nebiusProvider.ToBifrostImageResponse(&nebiusResponse)
		if bifrostResponse == nil {
			return nil, fmt.Errorf("failed to convert Nebius response to Bifrost format")
		}

		// Set model field (Nebius converter doesn't set it, similar to embeddings pattern)
		if bifrostResponse.Model == "" {
			bifrostResponse.Model = model
		}

		return bifrostResponse, nil

	case hfInference:
		// Handle raw byte data - encode to base64
		b64Data := base64.StdEncoding.EncodeToString(data)
		return &schemas.BifrostImageGenerationResponse{
			Model: model,
			Data: []schemas.ImageData{
				{
					B64JSON: b64Data,
					Index:   0,
				},
			},
		}, nil

	case falAI:
		// Handle fal-ai JSON response
		var falResponse HuggingFaceFalAIImageGenerationResponse
		if err := sonic.Unmarshal(data, &falResponse); err != nil {
			return nil, fmt.Errorf("failed to unmarshal fal-ai response: %w", err)
		}

		imageData := make([]schemas.ImageData, len(falResponse.Images))
		for i, img := range falResponse.Images {
			// Handle both URL and base64 responses
			imageData[i] = schemas.ImageData{
				URL:     img.URL,
				B64JSON: img.B64JSON,
				Index:   i,
			}
		}

		return &schemas.BifrostImageGenerationResponse{
			Model: model,
			Data:  imageData,
		}, nil

	case together:
		// Handle together JSON response
		var togetherResponse HuggingFaceTogetherImageGenerationResponse
		if err := sonic.Unmarshal(data, &togetherResponse); err != nil {
			return nil, fmt.Errorf("failed to unmarshal together response: %w", err)
		}

		imageData := make([]schemas.ImageData, len(togetherResponse.Data))
		for i, img := range togetherResponse.Data {
			imageData[i] = schemas.ImageData{
				B64JSON: img.B64JSON,
				URL:     img.URL,
				Index:   i,
			}
		}

		return &schemas.BifrostImageGenerationResponse{
			Model: model,
			Data:  imageData,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported inference provider: %s", inferenceProvider)
	}
}
