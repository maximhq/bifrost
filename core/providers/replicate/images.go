package replicate

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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
func ToReplicateImageGenerationInput(bifrostReq *schemas.BifrostImageGenerationRequest) (*ReplicatePredictionRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, nil
	}

	input := &ReplicatePredictionRequestInput{
		Prompt: &bifrostReq.Input.Prompt,
	}

	// Map parameters if available
	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		// Map input images to the field name the target model expects
		if len(params.InputImages) > 0 {
			images := make([]string, 0, len(params.InputImages))
			for _, img := range params.InputImages {
				sanitizedURL, err := schemas.SanitizeImageURL(img)
				if err != nil {
					return nil, fmt.Errorf("invalid input image: %w", err)
				}
				images = append(images, sanitizedURL)
			}
			setInputImageField(input, bifrostReq.Model, images)
		}

		if bifrostReq.Params.N != nil {
			input.NumberOfImages = bifrostReq.Params.N
		}

		if params.AspectRatio != nil {
			input.AspectRatio = params.AspectRatio
		}

		if params.Size != nil {
			aspectRatio, imageSize := providerUtils.ConvertSizeToAspectRatioAndResolution(*params.Size)
			_, hasExplicitResolution := params.ExtraParams["resolution"]
			if params.AspectRatio == nil && aspectRatio != "" {
				input.AspectRatio = &aspectRatio
			}
			if imageSize != "" && !hasExplicitResolution {
				input.Resolution = &imageSize
			}
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

	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		if webhook, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["webhook"]); ok {
			request.Webhook = webhook
		}
		if webhookEventsFilter, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["webhook_events_filter"]); ok {
			request.WebhookEventsFilter = webhookEventsFilter
		}
	}

	return request, nil
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

	// Extract usage information from logs
	if prediction.Logs != nil {
		inputTokens, outputTokens, totalTokens, found := parseTokenUsageFromLogs(prediction.Logs, schemas.ImageGenerationRequest)
		if found {
			response.Usage = &schemas.ImageUsage{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				TotalTokens:  totalTokens,
			}
		}
	}

	return response, nil
}

// applyUpscaleOutputResolution backfills ImageGenerationResponseParameters.Size
// on an upscale-style response (e.g. prunaai/p-image-upscale) whose output
// resolution isn't otherwise knowable: these models take an input image plus
// a "target" (desired output megapixels) or "factor" (multiplier on the input
// image's dimensions) parameter instead of a plain size string, so neither
// the request's Params.Size nor the provider's own response carries any
// resolution info by default. Without this, resolution-tiered image pricing
// silently falls back to the base per-image rate regardless of actual output
// size. No-op (leaves Size untouched) when neither signal is present.
func applyUpscaleOutputResolution(request *schemas.BifrostImageGenerationRequest, prediction *ReplicatePredictionResponse, response *schemas.BifrostImageGenerationResponse) {
	if request == nil || response == nil {
		return
	}
	setUpscaleOutputSize(resolveUpscaleOutputPixels(request, prediction), response)
}

// applyUpscaleEditOutputResolution is applyUpscaleOutputResolution for the
// image edit path, which returns the same response shape and so feeds the same
// resolution-tiered pricing lookup.
func applyUpscaleEditOutputResolution(request *schemas.BifrostImageEditRequest, prediction *ReplicatePredictionResponse, response *schemas.BifrostImageGenerationResponse) {
	if request == nil || response == nil {
		return
	}
	setUpscaleOutputSize(resolveUpscaleEditOutputPixels(request, prediction), response)
}

// setUpscaleOutputSize writes a resolved pixel count onto a response as a size
// string, leaving a size the provider itself reported untouched.
func setUpscaleOutputSize(pixels int, response *schemas.BifrostImageGenerationResponse) {
	if pixels <= 0 {
		return
	}
	if response.ImageGenerationResponseParameters == nil {
		response.ImageGenerationResponseParameters = &schemas.ImageGenerationResponseParameters{}
	}
	if response.ImageGenerationResponseParameters.Size == "" {
		response.ImageGenerationResponseParameters.Size = formatSquarePixelSize(pixels)
	}
}

// applyUpscaleStreamOutputResolution is setUpscaleOutputSize for a streaming
// completion chunk, which carries Size directly rather than nested under
// response parameters. Streaming callers can only supply the request-side
// signal: the SSE path never re-reads the finished prediction, so the
// metrics.resolution_target fallback that covers factor mode is unavailable
// there and a factor-mode stream is left without a size, as before.
func applyUpscaleStreamOutputResolution(pixels int, chunk *schemas.BifrostImageGenerationStreamResponse) {
	if chunk == nil || pixels <= 0 || chunk.Size != "" {
		return
	}
	chunk.Size = formatSquarePixelSize(pixels)
}

// resolveUpscaleOutputPixels estimates the total output pixel count for an
// upscale-style request, in priority order:
//  1. "target" mode: the request declares its desired output resolution in
//     megapixels directly (e.g. target: 16) — known before the call is made.
//  2. "factor" mode: output size depends on the input image's own resolution
//     (unknown to Bifrost), so we fall back to the megapixel band Replicate
//     itself reports post-hoc via metrics.resolution_target (e.g. "8-16MP").
//
// Returns 0 when neither signal is present.
func resolveUpscaleOutputPixels(request *schemas.BifrostImageGenerationRequest, prediction *ReplicatePredictionResponse) int {
	if request == nil || request.Params == nil || request.Params.ExtraParams == nil {
		return resolveUpscaleOutputPixelsFromMetrics(prediction)
	}
	if pixels := resolveUpscaleTargetPixelsFromExtraParams(request.Params.ExtraParams); pixels > 0 {
		return pixels
	}

	return resolveUpscaleOutputPixelsFromMetrics(prediction)
}

// resolveUpscaleTargetPixelsFromExtraParams reads the request-side "target"
// signal out of Replicate's own native parameter names, which callers can pass
// through verbatim on either the image generation or image edit path. Returns
// 0 in factor mode, where the output size follows the input image instead and
// only the provider can report it.
func resolveUpscaleTargetPixelsFromExtraParams(extraParams map[string]interface{}) int {
	if extraParams == nil {
		return 0
	}
	upscaleMode, _ := schemas.SafeExtractString(extraParams["upscale_mode"])
	if upscaleMode != "" && upscaleMode != "target" {
		return 0
	}
	if targetMP, ok := schemas.SafeExtractFloat64(extraParams["target"]); ok {
		return megapixelsToPixels(targetMP)
	}
	return 0
}

// resolveUpscaleEditOutputPixels is resolveUpscaleOutputPixels for the image
// edit path, where an upscale is expressed through the first-class
// TargetMegapixels / UpscaleFactor params rather than through Replicate's raw
// input names. The two are mutually exclusive: UpscaleFactor scales the input
// image, so its output size is unknowable up front and falls back to the
// band the provider reports post-hoc, exactly as factor mode does on the
// generation path.
func resolveUpscaleEditOutputPixels(request *schemas.BifrostImageEditRequest, prediction *ReplicatePredictionResponse) int {
	if request == nil || request.Params == nil {
		return resolveUpscaleOutputPixelsFromMetrics(prediction)
	}
	params := request.Params
	if params.UpscaleFactor == nil && params.TargetMegapixels != nil {
		// Converted as a float so an out-of-range value is rejected by the
		// shared bound check rather than wrapping through the multiply.
		if pixels := megapixelsToPixels(float64(*params.TargetMegapixels)); pixels > 0 {
			return pixels
		}
	}
	if params.UpscaleFactor == nil {
		if pixels := resolveUpscaleTargetPixelsFromExtraParams(params.ExtraParams); pixels > 0 {
			return pixels
		}
	}

	return resolveUpscaleOutputPixelsFromMetrics(prediction)
}

// resolveUpscaleOutputPixelsFromMetrics parses a megapixel band string like
// "8-16MP" or "16MP" from the prediction's metrics.resolution_target field.
// Uses the upper bound of the band as the billable pixel estimate — the
// conservative choice, since underestimating post-hoc would under-bill.
func resolveUpscaleOutputPixelsFromMetrics(prediction *ReplicatePredictionResponse) int {
	if prediction == nil || prediction.Metrics == nil || prediction.Metrics.ResolutionTarget == nil {
		return 0
	}
	band := strings.ToUpper(strings.TrimSpace(*prediction.Metrics.ResolutionTarget))
	band = strings.TrimSuffix(band, "MP")
	if band == "" {
		return 0
	}
	// A band is either a single value ("16MP") or an ascending pair
	// ("8-16MP"). Anything else is malformed, and since this value ends up
	// selecting a resolution pricing tier, a malformed band must resolve to
	// no size rather than to whichever number happens to parse last.
	parts := strings.Split(band, "-")
	if len(parts) > 2 {
		return 0
	}
	mp, err := strconv.ParseFloat(strings.TrimSpace(parts[len(parts)-1]), 64)
	if err != nil {
		return 0
	}
	if len(parts) == 2 {
		lower, lowerErr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		// Ordering is only meaningful between two finite bounds: every
		// comparison against NaN is false, so a non-finite lower bound would
		// pass an ordering check it never actually satisfied.
		if lowerErr != nil || math.IsNaN(lower) || math.IsInf(lower, 0) || lower <= 0 || lower > mp {
			return 0
		}
	}
	return megapixelsToPixels(mp)
}

// megapixelsToPixels converts a megapixel figure to a total pixel count,
// rejecting anything that cannot describe a real output size. ParseFloat
// accepts "NaN" and "Inf", and a non-finite value both slips past ordinary
// range checks and saturates the conversion to int, so it has to be rejected
// by name. Rounds up for the same reason formatSquarePixelSize does: a value
// sitting on a pricing tier's threshold must never be billed one tier down.
func megapixelsToPixels(mp float64) int {
	if math.IsNaN(mp) || math.IsInf(mp, 0) || mp <= 0 {
		return 0
	}
	pixels := math.Ceil(mp * 1_000_000)
	if pixels >= math.MaxInt64 {
		return 0
	}
	return int(pixels)
}

// formatSquarePixelSize formats a total pixel count as a "WxH" size string
// for ImageGenerationResponseParameters.Size, using a square approximation
// (side = ceil(sqrt(pixels))). Rounding up guarantees width*height never
// falls below the true pixel count, so a value sitting exactly on a pricing
// tier's threshold is never miscategorized into the tier below it.
func formatSquarePixelSize(pixels int) string {
	side := int(math.Ceil(math.Sqrt(float64(pixels))))
	return fmt.Sprintf("%dx%d", side, side)
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

// setInputImageField assigns images to the input field the model expects.
// Shared by the generation and edit paths so both stay in sync.
func setInputImageField(input *ReplicatePredictionRequestInput, model string, images []string) {
	switch getInputImageFieldName(model) {
	case "image_prompt":
		// For flux-1.1-pro variants: use first image as image_prompt
		input.ImagePrompt = &images[0]

	case "input_image":
		// For flux-kontext variants: use first image as input_image
		input.InputImage = &images[0]

	case "image":
		// For flux-dev variants: use first image as image field
		input.Image = &images[0]

	case "input_images":
		// For all other models: use input_images array (preserves multi-image support)
		input.InputImages = images
	}
}

// ToReplicateImageEditInput converts a Bifrost image edit request to Replicate prediction input
func ToReplicateImageEditInput(bifrostReq *schemas.BifrostImageEditRequest) *ReplicatePredictionRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	input := &ReplicatePredictionRequestInput{
		Prompt: &bifrostReq.Input.Prompt,
	}

	// Map image URLs - Replicate requires image URLs, not file bytes
	if len(bifrostReq.Input.Images) > 0 {
		images := make([]string, 0, len(bifrostReq.Input.Images))
		for _, img := range bifrostReq.Input.Images {
			if len(img.Image) > 0 {
				images = append(images, providerUtils.FileBytesToBase64DataURL(img.Image))
			}
		}

		if len(images) > 0 {
			setInputImageField(input, bifrostReq.Model, images)
		}
	}

	// Map parameters if available
	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.N != nil {
			input.NumberOfImages = params.N
		}

		if params.Size != nil {
			aspectRatio, imageSize := providerUtils.ConvertSizeToAspectRatioAndResolution(*params.Size)
			_, hasExplicitAspectRatio := params.ExtraParams["aspect_ratio"]
			_, hasExplicitResolution := params.ExtraParams["resolution"]
			if aspectRatio != "" && !hasExplicitAspectRatio {
				input.AspectRatio = &aspectRatio
			}
			if imageSize != "" && !hasExplicitResolution {
				input.Resolution = &imageSize
			}
		}

		if params.OutputFormat != nil {
			input.OutputFormat = params.OutputFormat
		}

		if params.Quality != nil {
			input.Quality = params.Quality
		}

		if params.Background != nil {
			input.Background = params.Background
		}

		if params.Seed != nil {
			input.Seed = params.Seed
		}

		if params.NegativePrompt != nil {
			input.NegativePrompt = params.NegativePrompt
		}

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
