package runway

import (
	"fmt"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToRunwayImageGenerationRequest converts a Bifrost image generation request to Runway's text_to_image format.
func ToRunwayImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) (*RunwayImageGenerationRequest, error) {
	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("input is required")
	}

	// Field support varies by model; only attach what the target model accepts.
	caps := runwayImageModelCapabilities(bifrostReq.Model)

	request := &RunwayImageGenerationRequest{
		Model:      bifrostReq.Model,
		PromptText: bifrostReq.Input.Prompt,
		Ratio:      defaultRunwayImageRatio(bifrostReq.Model),
	}

	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.AspectRatio != nil && *params.AspectRatio != "" {
			request.Ratio = *params.AspectRatio
		} else if params.Size != nil && *params.Size != "" {
			// convert 1920x1080 to 1920:1080
			request.Ratio = strings.Replace(*params.Size, "x", ":", 1)
		}

		if caps.seed && params.Seed != nil {
			request.Seed = params.Seed
		}
		if caps.outputCount && params.N != nil {
			request.OutputCount = params.N
		}
		if caps.quality && params.Quality != nil {
			request.Quality = params.Quality
		}
		if caps.background && params.Background != nil {
			request.Background = params.Background
		}
		if caps.contentModeration && params.Moderation != nil && *params.Moderation != "" {
			request.ContentModeration = &ContentModeration{PublicFigureThreshold: params.Moderation}
		}

		// Map input images to reference images (no tag unless provided via extra params)
		for _, img := range params.InputImages {
			sanitizedURL, err := schemas.SanitizeImageURL(img)
			if err != nil {
				return nil, fmt.Errorf("invalid input image: %w", err)
			}
			request.ReferenceImages = append(request.ReferenceImages, ReferenceImage{URI: sanitizedURL})
		}

		if params.ExtraParams != nil {
			request.ExtraParams = params.ExtraParams

			// Explicit reference images (with tags for at-mention syntax) override input images
			if refImagesVal := params.ExtraParams["reference_images"]; refImagesVal != nil {
				if refImages, ok := refImagesVal.([]ReferenceImage); ok && refImages != nil {
					delete(request.ExtraParams, "reference_images")
					request.ReferenceImages = refImages
				} else if refImages, err := schemas.ConvertViaJSON[[]ReferenceImage](refImagesVal); err == nil {
					delete(request.ExtraParams, "reference_images")
					request.ReferenceImages = refImages
				}
			}

			// Content moderation: always remove from extra params (avoid leaking the
			// snake_case key into the body), but only attach for models that support it.
			if cmVal := params.ExtraParams["content_moderation"]; cmVal != nil {
				delete(request.ExtraParams, "content_moderation")
				if caps.contentModeration {
					if cm, ok := cmVal.(*ContentModeration); ok && cm != nil {
						request.ContentModeration = cm
					} else if cm, err := schemas.ConvertViaJSON[ContentModeration](cmVal); err == nil {
						request.ContentModeration = &cm
					}
				}
			}
		}
	}

	// Drop the per-reference subject for models that don't support character/object consistency.
	if !caps.referenceSubject {
		for i := range request.ReferenceImages {
			request.ReferenceImages[i].Subject = ""
		}
	}

	return request, nil
}

// ToRunwayImageEditRequest converts a Bifrost image edit request to Runway's text_to_image format.
// Runway has no dedicated edit endpoint, so the input images are sent as reference images.
func ToRunwayImageEditRequest(bifrostReq *schemas.BifrostImageEditRequest) (*RunwayImageGenerationRequest, error) {
	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("input is required")
	}

	// Field support varies by model; only attach what the target model accepts.
	caps := runwayImageModelCapabilities(bifrostReq.Model)

	request := &RunwayImageGenerationRequest{
		Model:      bifrostReq.Model,
		PromptText: bifrostReq.Input.Prompt,
		Ratio:      defaultRunwayImageRatio(bifrostReq.Model),
	}

	// Map edit input images (raw bytes) to reference images as data URIs.
	for _, img := range bifrostReq.Input.Images {
		if len(img.Image) == 0 {
			continue
		}
		request.ReferenceImages = append(request.ReferenceImages, ReferenceImage{URI: providerUtils.FileBytesToBase64DataURL(img.Image)})
	}

	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.Size != nil && *params.Size != "" {
			// convert 1920x1080 to 1920:1080
			request.Ratio = strings.Replace(*params.Size, "x", ":", 1)
		}

		if caps.seed && params.Seed != nil {
			request.Seed = params.Seed
		}
		if caps.outputCount && params.N != nil {
			request.OutputCount = params.N
		}
		if caps.quality && params.Quality != nil {
			request.Quality = params.Quality
		}
		if caps.background && params.Background != nil {
			request.Background = params.Background
		}

		if params.ExtraParams != nil {
			request.ExtraParams = params.ExtraParams

			// Explicit reference images (with tags/subject) are appended after the edit images.
			if refImagesVal := params.ExtraParams["reference_images"]; refImagesVal != nil {
				if refImages, ok := refImagesVal.([]ReferenceImage); ok && refImages != nil {
					delete(request.ExtraParams, "reference_images")
					request.ReferenceImages = append(request.ReferenceImages, refImages...)
				} else if refImages, err := schemas.ConvertViaJSON[[]ReferenceImage](refImagesVal); err == nil {
					delete(request.ExtraParams, "reference_images")
					request.ReferenceImages = append(request.ReferenceImages, refImages...)
				}
			}

			// Content moderation: always remove from extra params (avoid leaking the
			// snake_case key into the body), but only attach for models that support it.
			if cmVal := params.ExtraParams["content_moderation"]; cmVal != nil {
				delete(request.ExtraParams, "content_moderation")
				if caps.contentModeration {
					if cm, ok := cmVal.(*ContentModeration); ok && cm != nil {
						request.ContentModeration = cm
					} else if cm, err := schemas.ConvertViaJSON[ContentModeration](cmVal); err == nil {
						request.ContentModeration = &cm
					}
				}
			}
		}
	}

	// Drop the per-reference subject for models that don't support character/object consistency.
	if !caps.referenceSubject {
		for i := range request.ReferenceImages {
			request.ReferenceImages[i].Subject = ""
		}
	}

	return request, nil
}

// ToBifrostImageGenerationResponse converts Runway task details to Bifrost image generation response format.
func ToBifrostImageGenerationResponse(taskDetails *RunwayTaskDetailsResponse) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if taskDetails == nil {
		return nil, providerUtils.NewBifrostOperationError("task details is nil", nil)
	}

	response := &schemas.BifrostImageGenerationResponse{
		ID:   taskDetails.ID,
		Data: []schemas.ImageData{},
	}

	if taskDetails.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, taskDetails.CreatedAt); err == nil {
			response.Created = t.Unix()
		}
	}

	for i, url := range taskDetails.Output {
		response.Data = append(response.Data, schemas.ImageData{
			URL:   url,
			Index: i,
		})
	}

	return response, nil
}
