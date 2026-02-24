package gemini

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const defaultVideoContentType = "video/mp4"

// sizeToAspectRatio converts OpenAI-style size strings to Gemini aspect ratios.
// Gemini supports 16:9 and 9:16. Returns default value if no mapping exists.
func sizeToAspectRatio(size string) string {
	switch size {
	case "1280x720", "1792x1024":
		return "16:9"
	case "720x1280", "1024x1792":
		return "9:16"
	default:
		return "16:9"
	}
}

func addVideoURLOutput(uri, contentType string) *schemas.VideoOutput {
	if uri == "" {
		return nil
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = defaultVideoContentType
	}
	return &schemas.VideoOutput{
		Type:        schemas.VideoOutputTypeURL,
		URL:         schemas.Ptr(uri),
		ContentType: contentType,
	}
}

func addVideoBase64Output(base64Value, contentType string) *schemas.VideoOutput {
	if base64Value == "" {
		return nil
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = defaultVideoContentType
	}
	return &schemas.VideoOutput{
		Type:        schemas.VideoOutputTypeBase64,
		Base64Data:  schemas.Ptr(base64Value),
		ContentType: contentType,
	}
}

func parseVideoDataURL(data string) (mimeType string, base64Payload string, ok bool) {
	if !strings.HasPrefix(data, "data:") {
		return "", "", false
	}
	parts := strings.SplitN(data, ",", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	header := parts[0]
	payload := parts[1]
	if payload == "" {
		return "", "", false
	}
	header = strings.TrimPrefix(header, "data:")
	if before, _, found := strings.Cut(header, ";"); found {
		return before, payload, true
	}
	return header, payload, true
}

// ToGeminiVideoGenerationRequest converts a Bifrost video generation request to Gemini REST API format
// This creates the request body for POST /models/{model}:predictLongRunning
func ToGeminiVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*GeminiVideoGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, fmt.Errorf("bifrost request or input is nil")
	}

	// Create the instance with prompt
	instance := &GeminiVideoGenerationInstance{
		Prompt: bifrostReq.Input.Prompt,
	}

	// Handle input reference (image for image-to-video)
	if bifrostReq.Input.InputReference != nil && *bifrostReq.Input.InputReference != "" {
		// extract mime type and base64 string from input reference
		sanitizedURL, err := schemas.SanitizeImageURL(*bifrostReq.Input.InputReference)
		if err != nil {
			return nil, fmt.Errorf("invalid input reference: %w", err)
		}
		urlInfo := schemas.ExtractURLTypeInfo(sanitizedURL)

		image := &VideoImageData{}

		if urlInfo.DataURLWithoutPrefix != nil {
			image.BytesBase64Encoded = urlInfo.DataURLWithoutPrefix
		}
		image.MimeType = schemas.Ptr("image/png")
		if urlInfo.MediaType != nil {
			image.MimeType = urlInfo.MediaType
		}

		instance.Image = image
	}

	if bifrostReq.Params != nil && bifrostReq.Params.VideoURI != nil {
		instance.Video = &VideoGenerationVideoInput{
			URI: bifrostReq.Params.VideoURI,
		}
	}

	req := &GeminiVideoGenerationRequest{
		Instances: []GeminiVideoGenerationInstance{*instance},
	}

	// Map parameters if provided
	if bifrostReq.Params != nil {
		params := &VideoGenerationParameters{}

		// Extract all video generation parameters from ExtraParams
		if bifrostReq.Params.NegativePrompt != nil {
			params.NegativePrompt = bifrostReq.Params.NegativePrompt
		}

		if bifrostReq.Params.Seconds != nil {
			seconds, err := strconv.Atoi(*bifrostReq.Params.Seconds)
			if err != nil {
				return nil, fmt.Errorf("invalid seconds value: %w", err)
			}
			params.DurationSeconds = &seconds
		}

		if bifrostReq.Params.Seed != nil {
			params.Seed = bifrostReq.Params.Seed
		}

		if bifrostReq.Params.Audio != nil {
			params.GenerateAudio = bifrostReq.Params.Audio
		}

		if bifrostReq.Params.ExtraParams != nil {
			req.ExtraParams = bifrostReq.Params.ExtraParams
			if aspectRatio, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["aspectRatio"]); ok {
				params.AspectRatio = aspectRatio
			}
			if resolution, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["resolution"]); ok {
				params.Resolution = resolution
			}

			if sampleCount, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["sampleCount"]); ok {
				params.SampleCount = sampleCount
			}

			if personGeneration, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["personGeneration"]); ok {
				params.PersonGeneration = personGeneration
			}

			if numberOfVideos, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["numberOfVideos"]); ok {
				params.NumberOfVideos = numberOfVideos
			}
			if storageURI, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["storageURI"]); ok {
				params.StorageURI = storageURI
			}
			if compressionQuality, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["compressionQuality"]); ok {
				params.CompressionQuality = compressionQuality
			}
			if enhancePrompt, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enhancePrompt"]); ok {
				params.EnhancePrompt = enhancePrompt
			}
			if resizeMode, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["resizeMode"]); ok {
				params.ResizeMode = resizeMode
			}
			if referenceImages, ok := bifrostReq.Params.ExtraParams["referenceImages"]; ok {
				if referenceImages, ok := referenceImages.([]VideoReferenceImage); ok && referenceImages != nil {
					params.ReferenceImages = referenceImages
				} else if data, err := sonic.Marshal(referenceImages); err == nil {
					var referenceImages []VideoReferenceImage
					if sonic.Unmarshal(data, &referenceImages) == nil {
						params.ReferenceImages = referenceImages
					}
				}
			}
			if lastFrame, ok := bifrostReq.Params.ExtraParams["lastFrame"]; ok {
				if lastFrame, ok := lastFrame.(*VideoImageData); ok {
					params.LastFrame = lastFrame
				} else if data, err := sonic.Marshal(lastFrame); err == nil {
					var lastFrame VideoImageData
					if sonic.Unmarshal(data, &lastFrame) == nil {
						params.LastFrame = &lastFrame
					}
				}
			}
		}

		// Convert size to aspect ratio if size is provided and aspect ratio is not already set
		if params.AspectRatio == nil && bifrostReq.Params.Size != "" {
			aspectRatio := sizeToAspectRatio(bifrostReq.Params.Size)
			if aspectRatio != "" {
				params.AspectRatio = &aspectRatio
			}
		}

		req.Parameters = params
	}

	return req, nil
}

// ToBifrostVideoGenerationResponse converts Gemini operation response to Bifrost format
func ToBifrostVideoGenerationResponse(operation *GenerateVideosOperation, model string) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if operation == nil {
		return nil, providerUtils.NewBifrostOperationError("operation is nil", nil, schemas.Gemini)
	}

	response := &schemas.BifrostVideoGenerationResponse{
		ID:        operation.Name,
		Object:    "video",
		CreatedAt: time.Now().Unix(),
	}
	if model != "" {
		response.Model = model
	}

	// Set status based on operation state
	if !operation.Done {
		response.Status = schemas.VideoStatusInProgress
		if operation.Metadata != nil {
			if progress, ok := operation.Metadata["progress"].(float64); ok {
				response.Progress = &progress
			}
		}
	} else if operation.Error != nil {
		response.Status = schemas.VideoStatusFailed
		response.Error = &schemas.VideoCreateError{
			Code:    "video_generation_failed",
			Message: fmt.Sprintf("%v", operation.Error),
		}
	} else if operation.Response != nil {
		// Check new response format with content filtering support
		if genVideoResp := operation.Response.GenerateVideoResponse; genVideoResp != nil {
			// Check for content filtering
			if genVideoResp.RAIMediaFilteredCount > 0 {
				response.Status = schemas.VideoStatusFailed
				response.ContentFilter = &schemas.ContentFilterInfo{
					FilteredCount: int(genVideoResp.RAIMediaFilteredCount),
					Reasons:       genVideoResp.RAIMediaFilteredReasons,
				}
				errorMsg := "Content filtered by safety policies"
				if len(genVideoResp.RAIMediaFilteredReasons) > 0 {
					errorMsg = genVideoResp.RAIMediaFilteredReasons[0]
				}
				response.Error = &schemas.VideoCreateError{
					Code:    "content_filtered",
					Message: errorMsg,
				}
			} else {
				response.Status = schemas.VideoStatusCompleted

				// Collect all generated videos from multiple possible locations.
				var videos []schemas.VideoOutput

				// Priority 1: GeneratedSamples
				if len(genVideoResp.GeneratedSamples) > 0 {
					for _, sample := range genVideoResp.GeneratedSamples {
						if sample == nil || sample.Video == nil {
							continue
						}

						if sample.Video.URI != "" {
							videoOutput := addVideoURLOutput(sample.Video.URI, sample.Video.MIMEType)
							if videoOutput != nil {
								videos = append(videos, *videoOutput)
							}
						}
						if len(sample.Video.VideoBytes) > 0 {
							videoOutput := addVideoBase64Output(
								base64.StdEncoding.EncodeToString(sample.Video.VideoBytes),
								sample.Video.MIMEType,
							)
							if videoOutput != nil {
								videos = append(videos, *videoOutput)
							}
						}
					}
				}

				if len(videos) > 0 {
					response.Videos = videos
				}
			}
		} else if len(operation.Response.GeneratedVideos) > 0 {
			// Backward compatibility for older response shapes
			response.Status = schemas.VideoStatusCompleted
			var videos []schemas.VideoOutput
			for _, genVideo := range operation.Response.GeneratedVideos {
				if genVideo == nil || genVideo.Video == nil {
					continue
				}
				if genVideo.Video.URI != "" {
					videoOutput := addVideoURLOutput(genVideo.Video.URI, genVideo.Video.MIMEType)
					if videoOutput != nil {
						videos = append(videos, *videoOutput)
					}
				}
				if len(genVideo.Video.VideoBytes) > 0 {
					videoOutput := addVideoBase64Output(
						base64.StdEncoding.EncodeToString(genVideo.Video.VideoBytes),
						genVideo.Video.MIMEType,
					)
					if videoOutput != nil {
						videos = append(videos, *videoOutput)
					}
				}
			}
			if len(videos) > 0 {
				response.Videos = videos
			}
		} else if len(operation.Response.Videos) > 0 {
			response.Status = schemas.VideoStatusCompleted
			var videos []schemas.VideoOutput
			for _, video := range operation.Response.Videos {
				if video.GCSURI != nil && *video.GCSURI != "" {
					mimeType := defaultVideoContentType
					if video.MIMEType != nil && *video.MIMEType != "" {
						mimeType = *video.MIMEType
					}
					videoOutput := addVideoURLOutput(*video.GCSURI, mimeType)
					if videoOutput != nil {
						videos = append(videos, *videoOutput)
					}
				} else if video.BytesBase64Encoded != nil && *video.BytesBase64Encoded != "" {
					mimeType := defaultVideoContentType
					if video.MIMEType != nil && *video.MIMEType != "" {
						mimeType = *video.MIMEType
					}
					videoOutput := addVideoBase64Output(*video.BytesBase64Encoded, mimeType)
					if videoOutput != nil {
						videos = append(videos, *videoOutput)
					}
				}
			}
			if len(videos) > 0 {
				response.Videos = videos
			}
		} else {
			response.Status = schemas.VideoStatusCompleted
		}
	} else {
		response.Status = schemas.VideoStatusCompleted
	}

	// Try to extract timestamps from metadata
	if operation.Metadata != nil {
		if createTime, ok := operation.Metadata["createTime"].(string); ok {
			if t, err := time.Parse(time.RFC3339, createTime); err == nil {
				response.CreatedAt = t.Unix()
			}
		}
		if updateTime, ok := operation.Metadata["updateTime"].(string); ok {
			if t, err := time.Parse(time.RFC3339, updateTime); err == nil && operation.Done {
				response.CompletedAt = schemas.Ptr(t.Unix())
			}
		}
	}

	return response, nil
}

func (request *GeminiVideoGenerationRequest) ToBifrostVideoGenerationRequest(ctx *schemas.BifrostContext) (*schemas.BifrostVideoGenerationRequest, error) {
	if request == nil || len(request.Instances) == 0 {
		return nil, fmt.Errorf("request is nil or has no instances")
	}

	// Use the first instance for the main input
	instance := request.Instances[0]

	provider, model := schemas.ParseModelString(request.Model, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Gemini))

	bifrostReq := &schemas.BifrostVideoGenerationRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.VideoGenerationInput{
			Prompt: instance.Prompt,
		},
	}

	// Handle image input for image-to-video
	if instance.Image != nil && instance.Image.BytesBase64Encoded != nil && *instance.Image.BytesBase64Encoded != "" {
		// attach mime type and base64 string to input reference
		mimeType := "image/png"
		if instance.Image.MimeType != nil && *instance.Image.MimeType != "" {
			mimeType = *instance.Image.MimeType
		}
		bifrostReq.Input.InputReference = schemas.Ptr(fmt.Sprintf("data:%s;base64,%s", mimeType, *instance.Image.BytesBase64Encoded))
	}

	// Helper to ensure params are initialized
	ensureParams := func() {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.VideoGenerationParameters{
				ExtraParams: make(map[string]any),
			}
		}
	}

	// Handle reference images
	if len(instance.ReferenceImages) > 0 {
		ensureParams()
		bifrostReq.Params.ExtraParams["referenceImages"] = instance.ReferenceImages
	}

	// Handle video URI
	if instance.Video != nil && instance.Video.URI != nil {
		ensureParams()
		bifrostReq.Params.VideoURI = instance.Video.URI
	}

	// Handle last frame
	if instance.LastFrame != nil {
		ensureParams()
		bifrostReq.Params.ExtraParams["lastFrame"] = instance.LastFrame
	}

	// Map parameters if provided
	if request.Parameters != nil {
		ensureParams()
		params := bifrostReq.Params

		if request.Parameters.NegativePrompt != nil {
			params.NegativePrompt = request.Parameters.NegativePrompt
		}
		if request.Parameters.DurationSeconds != nil {
			seconds := strconv.Itoa(*request.Parameters.DurationSeconds)
			params.Seconds = &seconds
		}
		if request.Parameters.Seed != nil {
			params.Seed = request.Parameters.Seed
		}
		if request.Parameters.GenerateAudio != nil {
			params.Audio = request.Parameters.GenerateAudio
		}
		if request.Parameters.AspectRatio != nil {
			params.ExtraParams["aspectRatio"] = *request.Parameters.AspectRatio
		}
		if request.Parameters.Resolution != nil {
			params.ExtraParams["resolution"] = *request.Parameters.Resolution
		}
		if request.Parameters.SampleCount != nil {
			params.ExtraParams["sampleCount"] = *request.Parameters.SampleCount
		}
		if request.Parameters.PersonGeneration != nil {
			params.ExtraParams["personGeneration"] = *request.Parameters.PersonGeneration
		}
		if request.Parameters.NumberOfVideos != nil {
			params.ExtraParams["numberOfVideos"] = *request.Parameters.NumberOfVideos
		}
		if request.Parameters.StorageURI != nil {
			params.ExtraParams["storageURI"] = *request.Parameters.StorageURI
		}
		if request.Parameters.CompressionQuality != nil {
			params.ExtraParams["compressionQuality"] = *request.Parameters.CompressionQuality
		}
		if request.Parameters.EnhancePrompt != nil {
			params.ExtraParams["enhancePrompt"] = *request.Parameters.EnhancePrompt
		}
		if request.Parameters.ResizeMode != nil {
			params.ExtraParams["resizeMode"] = *request.Parameters.ResizeMode
		}
	}

	return bifrostReq, nil
}

func ToGeminiVideoGenerationResponse(response *schemas.BifrostVideoGenerationResponse) *GenerateVideosOperation {
	if response == nil {
		return nil
	}

	decodedID := response.ID
	if decoded, err := url.PathUnescape(decodedID); err == nil {
		decodedID = decoded
	}

	// if id is in gemini or vertex format, set name in format models/model/operations/operation_id:provider
	// else make the id in gemini format
	if !(strings.HasPrefix(decodedID, "models/") && strings.Contains(decodedID, response.Model) && strings.Contains(decodedID, "operations/")) {
		// url encode model
		encodedModel := url.PathEscape(response.Model)
		decodedID = "models/" + encodedModel + "/operations/" + decodedID
	}
	operation := &GenerateVideosOperation{
		Name: decodedID,
	}

	switch response.Status {
	case schemas.VideoStatusCompleted:
		operation.Done = true
		if len(response.Videos) > 0 {
			generatedSamples := make([]*GeneratedVideo, 0, len(response.Videos))
			for _, output := range response.Videos {
				var video *Video

				switch output.Type {
				case schemas.VideoOutputTypeURL:
					if output.URL == nil || *output.URL == "" {
						continue
					}
					video = &Video{
						URI: *output.URL,
					}
					if output.ContentType != "" {
						video.MIMEType = output.ContentType
					}
				case schemas.VideoOutputTypeBase64:
					if output.Base64Data == nil || *output.Base64Data == "" {
						continue
					}
					base64Payload := *output.Base64Data
					mimeType := output.ContentType
					if parsedMimeType, payload, ok := parseVideoDataURL(*output.Base64Data); ok {
						base64Payload = payload
						if mimeType == "" {
							mimeType = parsedMimeType
						}
					}
					decoded, err := base64.StdEncoding.DecodeString(base64Payload)
					if err != nil {
						continue
					}
					if mimeType == "" {
						mimeType = defaultVideoContentType
					}
					video = &Video{
						VideoBytes: decoded,
						MIMEType:   mimeType,
					}
				default:
					continue
				}

				if video == nil {
					continue
				}
				generatedSamples = append(generatedSamples, &GeneratedVideo{
					Video: video,
				})
			}
			if len(generatedSamples) > 0 {
				operation.Response = &GenerateVideosOperationResponse{
					GenerateVideoResponse: &GenerateVideoResponse{
						GeneratedSamples: generatedSamples,
					},
				}
			}
		}
	case schemas.VideoStatusFailed:
		operation.Done = true
		// Check if this is a content filtering case
		if response.ContentFilter != nil && response.ContentFilter.FilteredCount > 0 {
			operation.Response = &GenerateVideosOperationResponse{
				GenerateVideoResponse: &GenerateVideoResponse{
					RAIMediaFilteredCount:   int32(response.ContentFilter.FilteredCount),
					RAIMediaFilteredReasons: response.ContentFilter.Reasons,
				},
			}
		} else if response.Error != nil {
			operation.Error = map[string]any{
				"message": response.Error.Message,
				"code":    response.Error.Code,
			}
		}
	default:
		operation.Done = false
	}

	return operation
}
