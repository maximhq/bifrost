package bedrock

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// GetExtraParams implements the RequestBodyWithExtraParams interface
func (r *BedrockInvokeRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *BedrockInvokeRequest) IsStreamingRequested() bool {
	return r.Stream
}

// Known fields for BedrockInvokeRequest — used by UnmarshalJSON to capture extras
var bedrockInvokeRequestKnownFields = map[string]bool{
	// Common
	"messages": true, "system": true, "prompt": true,
	"temperature": true, "top_p": true, "top_k": true,
	"max_tokens": true, "max_tokens_to_sample": true, "max_gen_len": true,
	"stop": true, "stop_sequences": true,
	// Anthropic
	"anthropic_version": true, "anthropic_beta": true,
	"tools": true, "tool_choice": true, "thinking": true,
	"output_config": true, "metadata": true,
	// Nova
	"schemaVersion": true, "inferenceConfig": true, "toolConfig": true,
	"additionalModelRequestFields": true,
	// Llama
	"images": true,
	// Cohere
	"p": true, "k": true, "return_likelihoods": true,
	"num_generations": true, "logit_bias": true, "truncate": true,
	"message": true, "chat_history": true,
	// AI21
	"n": true, "frequency_penalty": true, "presence_penalty": true,
	// Bedrock image gen / edit / variation (Titan/Nova Canvas)
	"taskType": true, "textToImageParams": true, "imageVariationParams": true,
	"inPaintingParams": true, "outPaintingParams": true, "backgroundRemovalParams": true,
	"imageGenerationConfig": true,
	// Stability AI image
	"image": true, "mask": true, "negative_prompt": true,
	"aspect_ratio": true, "output_format": true, "seed": true,
	// Embeddings
	"inputText": true, "texts": true, "input_type": true,
	"normalize": true, "dimensions": true,
	"embedding_types": true, "output_dimension": true, "inputs": true,
	// Internal
	"stream": true, "extra_params": true,
}

// UnmarshalJSON implements custom JSON unmarshalling for BedrockInvokeRequest.
// It captures unknown fields into ExtraParams and normalizes AI21 Jamba messages
// (where content may be a string instead of []BedrockContentBlock).
func (r *BedrockInvokeRequest) UnmarshalJSON(data []byte) error {
	// Create an alias to avoid infinite recursion
	type Alias BedrockInvokeRequest
	aux := &struct {
		*Alias
		// Override Messages to use raw JSON for AI21 normalization
		Messages json.RawMessage `json:"messages,omitempty"`
	}{
		Alias: (*Alias)(r),
	}

	if err := sonic.Unmarshal(data, aux); err != nil {
		return err
	}

	// Normalize messages: handle AI21 Jamba where content can be a plain string
	if len(aux.Messages) > 0 {
		r.Messages = nil // Clear before re-parsing

		// Try standard []BedrockMessage first
		var standardMsgs []BedrockMessage
		if err := sonic.Unmarshal(aux.Messages, &standardMsgs); err == nil {
			r.Messages = standardMsgs
		} else {
			// Try AI21 format where content is a string
			var rawMsgs []struct {
				Role    BedrockMessageRole `json:"role"`
				Content json.RawMessage    `json:"content"`
			}
			if err := sonic.Unmarshal(aux.Messages, &rawMsgs); err == nil {
				for _, rm := range rawMsgs {
					msg := BedrockMessage{Role: rm.Role}
					// Try as string first (AI21 format)
					var contentStr string
					if err := sonic.Unmarshal(rm.Content, &contentStr); err == nil {
						msg.Content = []BedrockContentBlock{{Text: &contentStr}}
					} else {
						// Fall back to standard content blocks
						var blocks []BedrockContentBlock
						if err := sonic.Unmarshal(rm.Content, &blocks); err == nil {
							msg.Content = blocks
						}
					}
					r.Messages = append(r.Messages, msg)
				}
			}
		}
	}

	// Parse raw JSON to extract unknown fields into ExtraParams
	var rawData map[string]json.RawMessage
	if err := sonic.Unmarshal(data, &rawData); err != nil {
		return err
	}

	if r.ExtraParams == nil {
		r.ExtraParams = make(map[string]interface{})
	}

	// Preserve nested key ordering for prompt caching.
	for key, value := range rawData {
		if !bedrockInvokeRequestKnownFields[key] {
			var buf bytes.Buffer
			if err := json.Compact(&buf, value); err == nil {
				r.ExtraParams[key] = json.RawMessage(buf.Bytes())
			} else {
				r.ExtraParams[key] = json.RawMessage(value)
			}
		}
	}

	return nil
}

// DetectInvokeRequestType determines the request type from raw JSON body and model ID
// without full deserialization, keeping detection logic colocated with conversion methods.
func DetectInvokeRequestType(body []byte, modelID string) schemas.RequestType {
	// Messages → chat/responses path
	if node, _ := sonic.Get(body, "messages"); node.Exists() {
		if raw, err := node.Raw(); err == nil && raw != "null" && raw != "[]" {
			return schemas.ResponsesRequest
		}
	}

	// Titan uses "inputText" for both embeddings and text generation.
	// Use the model ID to disambiguate: embedding models contain "embed".
	if node, _ := sonic.Get(body, "inputText"); node.Exists() {
		if strings.Contains(strings.ToLower(modelID), "embed") {
			return schemas.EmbeddingRequest
		}
		return schemas.TextCompletionRequest
	}

	// Cohere embedding: text-only (texts), image-only (images), or mixed (inputs).
	// Use model ID to identify embed models, then check for any non-empty payload field.
	if strings.Contains(strings.ToLower(modelID), "embed") {
		for _, field := range []string{"texts", "images", "inputs"} {
			if node, _ := sonic.Get(body, field); node.Exists() {
				if raw, err := node.Raw(); err == nil && raw != "null" && raw != "[]" {
					return schemas.EmbeddingRequest
				}
			}
		}
	}

	// taskType-based image routing
	if taskNode, _ := sonic.Get(body, "taskType"); taskNode.Exists() {
		taskType, _ := taskNode.String()
		switch taskType {
		case TaskTypeTextImage:
			return schemas.ImageGenerationRequest
		case TaskTypeImageVariation:
			return schemas.ImageVariationRequest
		case TaskTypeInpainting, TaskTypeOutpainting, TaskTypeBackgroundRemoval:
			return schemas.ImageEditRequest
		}
	}

	// URL-decode the model ID once for all model-name checks below
	decodedModelID := modelID
	if unescaped, err := url.PathUnescape(modelID); err == nil {
		decodedModelID = unescaped
	}

	// Stability AI: supports both generation (prompt-only) and edit (image+prompt)
	if isStabilityAIModel(decodedModelID) {
		if node, _ := sonic.Get(body, "image"); node.Exists() {
			return schemas.ImageEditRequest
		}
		return schemas.ImageGenerationRequest
	}

	// explicit image field -> edit request
	if node, _ := sonic.Get(body, "image"); node.Exists() {
		return schemas.ImageEditRequest
	}

	// Checked after all body-field and model-specific signals so it doesn't shadow known models.
	if isPromptOnlyImageGenerationModel(decodedModelID) {
		return schemas.ImageGenerationRequest
	}

	return schemas.TextCompletionRequest
}

// IsMessagesRequest returns true when the request uses a messages-based format
// (Anthropic Messages API, Nova, or AI21 Jamba — has messages array)
func (r *BedrockInvokeRequest) IsMessagesRequest() bool {
	return len(r.Messages) > 0
}

// IsCohereCommandRRequest returns true for Cohere Command R/R+ native format
// (uses singular "message" field + "chat_history", not "messages" array or "prompt")
func (r *BedrockInvokeRequest) IsCohereCommandRRequest() bool {
	return r.Message != "" && r.Prompt == "" && len(r.Messages) == 0
}

// ToBedrockConverseRequest converts the invoke request to BedrockConverseRequest
// so we can reuse ToBifrostResponsesRequest() for messages-based requests.
func (r *BedrockInvokeRequest) ToBedrockConverseRequest() *BedrockConverseRequest {
	converseReq := &BedrockConverseRequest{
		ModelID:     r.ModelID,
		Messages:    r.Messages,
		Stream:      r.Stream,
		ExtraParams: r.ExtraParams,
	}

	// Convert system field: interface{} → []BedrockSystemMessage
	converseReq.System = r.parseSystemMessages()

	// Handle InferenceConfig: if Nova-style InferenceConfig is set, use it directly.
	// Otherwise, build from top-level fields.
	if r.InferenceConfig != nil {
		converseReq.InferenceConfig = r.InferenceConfig
	} else {
		inferenceConfig := &BedrockInferenceConfig{}
		hasConfig := false

		// MaxTokens: prefer max_tokens, fall back to max_tokens_to_sample
		if r.MaxTokens != nil {
			inferenceConfig.MaxTokens = r.MaxTokens
			hasConfig = true
		} else if r.MaxTokensToSample != nil {
			inferenceConfig.MaxTokens = r.MaxTokensToSample
			hasConfig = true
		}

		if r.Temperature != nil {
			inferenceConfig.Temperature = r.Temperature
			hasConfig = true
		}
		if r.TopP != nil {
			inferenceConfig.TopP = r.TopP
			hasConfig = true
		}

		// StopSequences: prefer stop_sequences, fall back to stop
		if len(r.StopSequences) > 0 {
			inferenceConfig.StopSequences = r.StopSequences
			hasConfig = true
		} else if len(r.Stop) > 0 {
			inferenceConfig.StopSequences = r.Stop
			hasConfig = true
		}

		if hasConfig {
			converseReq.InferenceConfig = inferenceConfig
		}
	}

	// Handle ToolConfig: if Nova-style ToolConfig is set, use it directly.
	// Otherwise, convert from Anthropic tool format if present.
	if r.ToolConfig != nil {
		converseReq.ToolConfig = r.ToolConfig
	} else if r.Tools != nil {
		converseReq.ToolConfig = r.convertAnthropicTools()
	}

	// Build AdditionalModelRequestFields for model-family-specific fields
	additionalFields := schemas.NewOrderedMap()
	hasAdditional := false

	// TopK → additionalModelRequestFields (not a standard Converse field)
	if r.TopK != nil {
		additionalFields.Set("top_k", *r.TopK)
		hasAdditional = true
	}

	// Anthropic thinking/reasoning
	if r.Thinking != nil {
		additionalFields.Set("thinking", r.Thinking)
		hasAdditional = true
	}

	// Anthropic output_config
	if r.OutputConfig != nil {
		additionalFields.Set("output_config", r.OutputConfig)
		hasAdditional = true
	}

	// AI21-specific fields
	if r.N != nil {
		additionalFields.Set("n", *r.N)
		hasAdditional = true
	}
	if r.FrequencyPenalty != nil {
		additionalFields.Set("frequency_penalty", *r.FrequencyPenalty)
		hasAdditional = true
	}
	if r.PresencePenalty != nil {
		additionalFields.Set("presence_penalty", *r.PresencePenalty)
		hasAdditional = true
	}

	// Nova AdditionalModelRequestFields (merge if present)
	if r.AdditionalModelRequestFields != nil {
		if amrf, ok := r.AdditionalModelRequestFields.(map[string]interface{}); ok {
			for k, v := range amrf {
				additionalFields.Set(k, v)
				hasAdditional = true
			}
		}
	}

	if hasAdditional {
		converseReq.AdditionalModelRequestFields = additionalFields
	}

	return converseReq
}

// ToBifrostTextCompletionRequest handles ALL prompt-based families
// (Anthropic legacy, Mistral, Llama, Cohere Command, Cohere Command R).
func (r *BedrockInvokeRequest) ToBifrostTextCompletionRequest(ctx *schemas.BifrostContext) *schemas.BifrostTextCompletionRequest {
	// Normalize prompt: Cohere Command R uses "message" field, not "prompt"
	prompt := r.Prompt
	if prompt == "" && r.Message != "" {
		prompt = r.buildCohereCommandRPrompt()
	}

	// Normalize max tokens: Llama uses max_gen_len
	maxTokens := r.MaxTokens
	if maxTokens == nil && r.MaxGenLen != nil {
		maxTokens = r.MaxGenLen
	}

	// Normalize top_p/top_k: Cohere uses p/k
	topP := r.TopP
	if topP == nil && r.CohereP != nil {
		topP = r.CohereP
	}
	topK := r.TopK
	if topK == nil && r.CohereK != nil {
		topK = r.CohereK
	}

	// Build a BedrockTextCompletionRequest and delegate to its ToBifrostTextCompletionRequest
	textReq := &BedrockTextCompletionRequest{
		ModelID:           r.ModelID,
		Prompt:            prompt,
		MaxTokens:         maxTokens,
		MaxTokensToSample: r.MaxTokensToSample,
		Temperature:       r.Temperature,
		TopP:              topP,
		TopK:              topK,
		Stop:              r.Stop,
		StopSequences:     r.StopSequences,
		Messages:          r.Messages,
		System:            r.System,
		AnthropicVersion:  r.AnthropicVersion,
		Stream:            r.Stream,
		ExtraParams:       r.ExtraParams,
	}
	return textReq.ToBifrostTextCompletionRequest(ctx)
}

// ToBifrostEmbeddingRequest converts the invoke request to a BifrostEmbeddingRequest.
// Handles both Titan (inputText) and Cohere (texts) embedding formats.
func (r *BedrockInvokeRequest) ToBifrostEmbeddingRequest(ctx *schemas.BifrostContext) *schemas.BifrostEmbeddingRequest {
	modelID := r.ModelID
	if unescaped, err := url.PathUnescape(r.ModelID); err == nil {
		modelID = unescaped
	}
	provider, model := schemas.ParseModelString(modelID, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Bedrock))
	req := &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
	}

	if r.InputText != "" {
		req.Input = &schemas.EmbeddingInput{Text: &r.InputText}
	} else if len(r.Texts) > 0 {
		req.Input = &schemas.EmbeddingInput{Texts: r.Texts}
	}
	// image-only (r.Images) or mixed (r.Inputs): req.Input stays nil; data flows via ExtraParams

	extraParams := make(map[string]interface{})
	// Forward known embedding-only params into ExtraParams so the provider can pick them up
	if r.InputType != nil {
		extraParams["input_type"] = *r.InputType
	}
	if r.Normalize != nil {
		extraParams["normalize"] = *r.Normalize
	}
	if len(r.EmbeddingTypes) > 0 {
		extraParams["embedding_types"] = r.EmbeddingTypes
	}
	if r.Truncate != nil {
		extraParams["truncate"] = *r.Truncate
	}
	if len(r.Images) > 0 {
		extraParams["images"] = r.Images
	}
	if len(r.Inputs) > 0 {
		extraParams["inputs"] = r.Inputs
	}
	if r.MaxTokens != nil {
		extraParams["max_tokens"] = *r.MaxTokens
	}
	// Merge any remaining extra params from the request
	for k, v := range r.ExtraParams {
		extraParams[k] = v
	}

	// output_dimension maps to Dimensions; prefer OutputDimension over Dimensions
	dimensions := r.Dimensions
	if r.OutputDimension != nil {
		dimensions = r.OutputDimension
	}
	params := &schemas.EmbeddingParameters{
		Dimensions: dimensions,
	}
	if len(extraParams) > 0 {
		params.ExtraParams = extraParams
	}
	req.Params = params

	return req
}

// ToBifrostImageGenerationRequest converts the invoke request to a BifrostImageGenerationRequest.
// Handles Titan/Nova Canvas (taskType=TEXT_IMAGE with textToImageParams) and Stability AI (flat prompt fields).
func (r *BedrockInvokeRequest) ToBifrostImageGenerationRequest(ctx *schemas.BifrostContext) *schemas.BifrostImageGenerationRequest {
	modelID := r.ModelID
	if unescaped, err := url.PathUnescape(r.ModelID); err == nil {
		modelID = unescaped
	}
	provider, model := schemas.ParseModelString(modelID, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Bedrock))
	req := &schemas.BifrostImageGenerationRequest{
		Provider: provider,
		Model:    model,
	}

	params := &schemas.ImageGenerationParameters{
		NegativePrompt: r.NegativePrompt,
		AspectRatio:    r.AspectRatio,
		N:              r.N,
		OutputFormat:   r.OutputFormat,
		Seed:           r.Seed,
	}

	if r.TextToImageParams != nil {
		// Titan / Nova Canvas path
		req.Input = &schemas.ImageGenerationInput{Prompt: r.TextToImageParams.Text}
		if r.TextToImageParams.NegativeText != nil {
			params.NegativePrompt = r.TextToImageParams.NegativeText
		}
		if r.TextToImageParams.Style != nil {
			params.Style = r.TextToImageParams.Style
		}
		if cfg := r.ImageGenerationConfig; cfg != nil {
			params.N = cfg.NumberOfImages
			params.Seed = cfg.Seed
			params.Quality = cfg.Quality
			if cfg.Width != nil && cfg.Height != nil {
				size := fmt.Sprintf("%dx%d", *cfg.Width, *cfg.Height)
				params.Size = &size
			}
			if cfg.CfgScale != nil {
				if params.ExtraParams == nil {
					params.ExtraParams = make(map[string]interface{})
				}
				params.ExtraParams["cfgScale"] = *cfg.CfgScale
			}
		}
	} else {
		// Stability AI path — prompt comes from the top-level "prompt" field
		req.Input = &schemas.ImageGenerationInput{Prompt: r.Prompt}
	}

	// Forward any remaining ExtraParams
	if len(r.ExtraParams) > 0 {
		if params.ExtraParams == nil {
			params.ExtraParams = make(map[string]interface{})
		}
		for k, v := range r.ExtraParams {
			params.ExtraParams[k] = v
		}
	}

	req.Params = params
	return req
}

// ToBifrostImageEditRequest converts the invoke request to a BifrostImageEditRequest.
// Handles Titan/Nova Canvas (taskType in INPAINTING/OUTPAINTING/BACKGROUND_REMOVAL) and Stability AI (flat image/mask fields).
func (r *BedrockInvokeRequest) ToBifrostImageEditRequest(ctx *schemas.BifrostContext) (*schemas.BifrostImageEditRequest, error) {
	modelID := r.ModelID
	if unescaped, err := url.PathUnescape(r.ModelID); err == nil {
		modelID = unescaped
	}
	provider, model := schemas.ParseModelString(modelID, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Bedrock))
	req := &schemas.BifrostImageEditRequest{
		Provider: provider,
		Model:    model,
	}
	params := &schemas.ImageEditParameters{
		NegativePrompt: r.NegativePrompt,
		Seed:           r.Seed,
	}

	if r.TaskType != nil {
		// Titan / Nova Canvas path
		switch *r.TaskType {
		case TaskTypeInpainting:
			if r.InPaintingParams == nil {
				return nil, fmt.Errorf("inPaintingParams required for INPAINTING task")
			}
			imgBytes, err := base64.StdEncoding.DecodeString(r.InPaintingParams.Image)
			if err != nil {
				return nil, fmt.Errorf("failed to decode inpainting image: %w", err)
			}
			req.Input = &schemas.ImageEditInput{
				Images: []schemas.ImageInput{{Image: imgBytes}},
				Prompt: r.InPaintingParams.Text,
			}
			params.Type = schemas.Ptr("inpainting")
			if r.InPaintingParams.NegativeText != nil {
				params.NegativePrompt = r.InPaintingParams.NegativeText
			}
			if r.InPaintingParams.MaskImage != nil {
				maskBytes, err := base64.StdEncoding.DecodeString(*r.InPaintingParams.MaskImage)
				if err != nil {
					return nil, fmt.Errorf("failed to decode inpainting mask: %w", err)
				}
				params.Mask = maskBytes
			}
			if r.InPaintingParams.MaskPrompt != nil || r.InPaintingParams.ReturnMask != nil {
				if params.ExtraParams == nil {
					params.ExtraParams = make(map[string]interface{})
				}
				if r.InPaintingParams.MaskPrompt != nil {
					params.ExtraParams["mask_prompt"] = *r.InPaintingParams.MaskPrompt
				}
				if r.InPaintingParams.ReturnMask != nil {
					params.ExtraParams["return_mask"] = *r.InPaintingParams.ReturnMask
				}
			}

		case TaskTypeOutpainting:
			if r.OutPaintingParams == nil {
				return nil, fmt.Errorf("outPaintingParams required for OUTPAINTING task")
			}
			imgBytes, err := base64.StdEncoding.DecodeString(r.OutPaintingParams.Image)
			if err != nil {
				return nil, fmt.Errorf("failed to decode outpainting image: %w", err)
			}
			req.Input = &schemas.ImageEditInput{
				Images: []schemas.ImageInput{{Image: imgBytes}},
				Prompt: r.OutPaintingParams.Text,
			}
			params.Type = schemas.Ptr("outpainting")
			if r.OutPaintingParams.NegativeText != nil {
				params.NegativePrompt = r.OutPaintingParams.NegativeText
			}
			if r.OutPaintingParams.MaskImage != nil {
				maskBytes, err := base64.StdEncoding.DecodeString(*r.OutPaintingParams.MaskImage)
				if err != nil {
					return nil, fmt.Errorf("failed to decode outpainting mask: %w", err)
				}
				params.Mask = maskBytes
			}
			if r.OutPaintingParams.MaskPrompt != nil || r.OutPaintingParams.ReturnMask != nil || r.OutPaintingParams.OutPaintingMode != nil {
				if params.ExtraParams == nil {
					params.ExtraParams = make(map[string]interface{})
				}
				if r.OutPaintingParams.MaskPrompt != nil {
					params.ExtraParams["mask_prompt"] = *r.OutPaintingParams.MaskPrompt
				}
				if r.OutPaintingParams.ReturnMask != nil {
					params.ExtraParams["return_mask"] = *r.OutPaintingParams.ReturnMask
				}
				if r.OutPaintingParams.OutPaintingMode != nil {
					params.ExtraParams["outpainting_mode"] = *r.OutPaintingParams.OutPaintingMode
				}
			}

		case TaskTypeBackgroundRemoval:
			if r.BackgroundRemovalParams == nil {
				return nil, fmt.Errorf("backgroundRemovalParams required for BACKGROUND_REMOVAL task")
			}
			imgBytes, err := base64.StdEncoding.DecodeString(r.BackgroundRemovalParams.Image)
			if err != nil {
				return nil, fmt.Errorf("failed to decode background removal image: %w", err)
			}
			req.Input = &schemas.ImageEditInput{
				Images: []schemas.ImageInput{{Image: imgBytes}},
			}
			params.Type = schemas.Ptr("background_removal")

		default:
			return nil, fmt.Errorf("unsupported taskType for image edit: %s", *r.TaskType)
		}

		// Map imageGenerationConfig fields into edit params (Titan/Nova Canvas only)
		if cfg := r.ImageGenerationConfig; cfg != nil {
			params.N = cfg.NumberOfImages
			params.Seed = cfg.Seed
			params.Quality = cfg.Quality
			if cfg.Width != nil && cfg.Height != nil {
				size := fmt.Sprintf("%dx%d", *cfg.Width, *cfg.Height)
				params.Size = &size
			}
			if cfg.CfgScale != nil {
				if params.ExtraParams == nil {
					params.ExtraParams = make(map[string]interface{})
				}
				params.ExtraParams["cfgScale"] = *cfg.CfgScale
			}
		}
	} else {
		// Stability AI path
		if r.Image == nil {
			return nil, fmt.Errorf("image field is required for Stability AI image edit")
		}
		imgBytes, err := base64.StdEncoding.DecodeString(*r.Image)
		if err != nil {
			return nil, fmt.Errorf("failed to decode stability AI image: %w", err)
		}
		req.Input = &schemas.ImageEditInput{
			Images: []schemas.ImageInput{{Image: imgBytes}},
			Prompt: r.Prompt,
		}
		// Infer task type from model name
		taskType, err := getStabilityAIEditTaskType(r.ModelID)
		if err != nil {
			return nil, fmt.Errorf("cannot determine Stability AI edit task: %w", err)
		}
		params.Type = &taskType
		if r.Mask != nil {
			maskBytes, err := base64.StdEncoding.DecodeString(*r.Mask)
			if err != nil {
				return nil, fmt.Errorf("failed to decode stability AI mask: %w", err)
			}
			params.Mask = maskBytes
		}
	}

	if len(r.ExtraParams) > 0 {
		if params.ExtraParams == nil {
			params.ExtraParams = make(map[string]interface{}, len(r.ExtraParams))
		}
		for k, v := range r.ExtraParams {
			params.ExtraParams[k] = v
		}
	}
	req.Params = params
	return req, nil
}

// ToBifrostImageVariationRequest converts the invoke request to a BifrostImageVariationRequest.
// Reads from imageVariationParams (Titan/Nova Canvas format).
func (r *BedrockInvokeRequest) ToBifrostImageVariationRequest(ctx *schemas.BifrostContext) (*schemas.BifrostImageVariationRequest, error) {
	if r.ImageVariationParams == nil || len(r.ImageVariationParams.Images) == 0 {
		return nil, fmt.Errorf("imageVariationParams.images is required for IMAGE_VARIATION")
	}

	primaryBytes, err := base64.StdEncoding.DecodeString(r.ImageVariationParams.Images[0])
	if err != nil {
		return nil, fmt.Errorf("failed to decode primary variation image: %w", err)
	}

	modelID := r.ModelID
	if unescaped, err := url.PathUnescape(r.ModelID); err == nil {
		modelID = unescaped
	}
	provider, model := schemas.ParseModelString(modelID, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Bedrock))
	req := &schemas.BifrostImageVariationRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.ImageVariationInput{
			Image: schemas.ImageInput{Image: primaryBytes},
		},
	}

	params := &schemas.ImageVariationParameters{}
	extraParams := make(map[string]interface{})

	// Additional images (index 1+) stored under "images" key for the provider
	if len(r.ImageVariationParams.Images) > 1 {
		additionalImages := make([][]byte, 0, len(r.ImageVariationParams.Images)-1)
		for _, imgB64 := range r.ImageVariationParams.Images[1:] {
			imgBytes, err := base64.StdEncoding.DecodeString(imgB64)
			if err != nil {
				return nil, fmt.Errorf("failed to decode additional variation image: %w", err)
			}
			additionalImages = append(additionalImages, imgBytes)
		}
		extraParams["images"] = additionalImages
	}

	// Text / negative text / similarity strength go to ExtraParams (provider reads them from there)
	if r.ImageVariationParams.Text != nil {
		extraParams["prompt"] = *r.ImageVariationParams.Text
	}
	if r.ImageVariationParams.NegativeText != nil {
		extraParams["negativeText"] = *r.ImageVariationParams.NegativeText
	}
	if r.ImageVariationParams.SimilarityStrength != nil {
		extraParams["similarityStrength"] = *r.ImageVariationParams.SimilarityStrength
	}

	// ImageGenerationConfig → N, Size, Seed, Quality, CfgScale
	if cfg := r.ImageGenerationConfig; cfg != nil {
		params.N = cfg.NumberOfImages
		if cfg.Width != nil && cfg.Height != nil {
			size := fmt.Sprintf("%dx%d", *cfg.Width, *cfg.Height)
			params.Size = &size
		}
		if cfg.Seed != nil {
			extraParams["seed"] = *cfg.Seed
		}
		if cfg.Quality != nil {
			extraParams["quality"] = *cfg.Quality
		}
		if cfg.CfgScale != nil {
			extraParams["cfgScale"] = *cfg.CfgScale
		}
	}

	// Forward any remaining ExtraParams from the request body
	for k, v := range r.ExtraParams {
		extraParams[k] = v
	}
	if len(extraParams) > 0 {
		params.ExtraParams = extraParams
	}

	req.Params = params
	return req, nil
}

// buildCohereCommandRPrompt converts Cohere Command R's message + chat_history into a text prompt.
func (r *BedrockInvokeRequest) buildCohereCommandRPrompt() string {
	var sb strings.Builder
	for _, hist := range r.ChatHistory {
		role := hist.Role
		if strings.EqualFold(role, "USER") {
			sb.WriteString("User: ")
		} else {
			sb.WriteString("Assistant: ")
		}
		sb.WriteString(hist.Message)
		sb.WriteString("\n")
	}
	sb.WriteString("User: ")
	sb.WriteString(r.Message)
	return sb.String()
}

// parseSystemMessages converts the polymorphic System field to []BedrockSystemMessage.
// System can be: string, []BedrockSystemMessage, or []map[string]interface{}.
func (r *BedrockInvokeRequest) parseSystemMessages() []BedrockSystemMessage {
	if r.System == nil {
		return nil
	}

	switch s := r.System.(type) {
	case string:
		if s == "" {
			return nil
		}
		return []BedrockSystemMessage{{Text: &s}}
	case []interface{}:
		var result []BedrockSystemMessage
		for _, item := range s {
			if m, ok := item.(map[string]interface{}); ok {
				// Re-marshal and unmarshal to capture all fields (text, guardContent, cachePoint)
				itemBytes, err := providerUtils.MarshalSorted(m)
				if err != nil {
					continue
				}
				var msg BedrockSystemMessage
				if err := sonic.Unmarshal(itemBytes, &msg); err != nil {
					continue
				}
				result = append(result, msg)
			}
		}
		return result
	}

	// Try direct type assertion for []BedrockSystemMessage (rare in JSON deserialization)
	if msgs, ok := r.System.([]BedrockSystemMessage); ok {
		return msgs
	}

	return nil
}

// convertAnthropicTools converts Anthropic-format tools to Bedrock ToolConfig.
// Anthropic tools are: [{"name": "...", "description": "...", "input_schema": {...}}]
func (r *BedrockInvokeRequest) convertAnthropicTools() *BedrockToolConfig {
	toolsSlice, ok := r.Tools.([]interface{})
	if !ok || len(toolsSlice) == 0 {
		return nil
	}

	var bedrockTools []BedrockTool
	for _, toolIface := range toolsSlice {
		toolMap, ok := toolIface.(map[string]interface{})
		if !ok {
			continue
		}

		spec := &BedrockToolSpec{}
		if name, ok := toolMap["name"].(string); ok {
			spec.Name = name
		}
		if desc, ok := toolMap["description"].(string); ok {
			spec.Description = &desc
		}
		if inputSchema, ok := toolMap["input_schema"]; ok {
			inputSchemaBytes, _ := providerUtils.MarshalSorted(inputSchema)
			spec.InputSchema = BedrockToolInputSchema{JSON: json.RawMessage(inputSchemaBytes)}
		}

		bedrockTools = append(bedrockTools, BedrockTool{ToolSpec: spec})
	}

	if len(bedrockTools) == 0 {
		return nil
	}

	toolConfig := &BedrockToolConfig{
		Tools: bedrockTools,
	}

	// Convert tool_choice
	if r.ToolChoice != nil {
		toolConfig.ToolChoice = convertAnthropicToolChoice(r.ToolChoice)
	}

	return toolConfig
}

// convertAnthropicToolChoice converts Anthropic-format tool_choice to Bedrock ToolChoice.
// Anthropic format: {"type": "auto"} | {"type": "any"} | {"type": "tool", "name": "..."}
func convertAnthropicToolChoice(choice interface{}) *BedrockToolChoice {
	choiceMap, ok := choice.(map[string]interface{})
	if !ok {
		return nil
	}

	typeStr, ok := choiceMap["type"].(string)
	if !ok {
		return nil
	}

	switch typeStr {
	case "auto":
		return &BedrockToolChoice{Auto: &BedrockToolChoiceAuto{}}
	case "any":
		return &BedrockToolChoice{Any: &BedrockToolChoiceAny{}}
	case "tool":
		if name, ok := choiceMap["name"].(string); ok {
			return &BedrockToolChoice{Tool: &BedrockToolChoiceTool{Name: name}}
		}
	}

	return nil
}

// ToBedrockInvokeMessagesResponse converts a BifrostResponsesResponse to the model-family-specific
// InvokeModel response format. Switches on model family to produce the correct JSON structure.
func ToBedrockInvokeMessagesResponse(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
	if resp == nil {
		return nil, fmt.Errorf("bifrost response is nil")
	}

	model := ""
	if resp.Model != "" {
		model = resp.Model
	} else {
		extraFields := resp.ExtraFields
		if extraFields.ResolvedModelUsed != "" {
			model = extraFields.ResolvedModelUsed
		} else if extraFields.OriginalModelRequested != "" {
			model = extraFields.OriginalModelRequested
		}
	}

	// Nova models: delegate to existing ToBedrockConverseResponse (Nova InvokeModel matches Converse format)
	if schemas.IsNovaModel(model) {
		return ToBedrockConverseResponse(resp)
	}

	// AI21 models
	if strings.Contains(model, "ai21") {
		return toBedrockInvokeAI21Response(resp), nil
	}

	// Default: Anthropic Messages API format (most common InvokeModel + messages use case)
	return toBedrockInvokeAnthropicResponse(resp, model), nil
}

func ToBedrockInvokeImagesResponse(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationResponse) (interface{}, error) {
	if resp == nil {
		return nil, fmt.Errorf("bifrost response is nil")
	}

	// If the provider stored the raw Bedrock response, return it verbatim (preserves seeds, finish_reasons, etc.)
	if resp.ExtraFields.RawResponse != nil {
		return resp.ExtraFields.RawResponse, nil
	}

	model := resp.Model
	if model == "" {
		if resp.ExtraFields.ResolvedModelUsed != "" {
			model = resp.ExtraFields.ResolvedModelUsed
		} else if resp.ExtraFields.OriginalModelRequested != "" {
			model = resp.ExtraFields.OriginalModelRequested
		}
	}

	// Stability AI models use the same BedrockImageGenerationResponse format as Titan/Nova Canvas
	if isStabilityAIModel(model) {
		return ToStabilityAIImageGenerationResponse(resp)
	}

	// Default: Titan Image Generator v1/v2, Nova Canvas — reconstruct from Bifrost data
	result := &BedrockImageGenerationResponse{}
	for _, d := range resp.Data {
		result.Images = append(result.Images, d.B64JSON)
	}
	return result, nil
}

// ToBedrockEmbeddingInvokeResponse converts a BifrostEmbeddingResponse back to the native
// Bedrock invoke API response format.
// Single-embedding (Titan) responses use: {"embedding": [...], "inputTextTokenCount": N}
// Multi-embedding (Cohere) responses use:  {"embeddings": [[...],[...]], "response_type": "embeddings_floats"}
func ToBedrockEmbeddingInvokeResponse(resp *schemas.BifrostEmbeddingResponse) (interface{}, error) {
	if resp == nil {
		return nil, fmt.Errorf("bifrost embedding response is nil")
	}

	// If the provider stored the raw Bedrock response, return it verbatim
	if resp.ExtraFields.RawResponse != nil {
		return resp.ExtraFields.RawResponse, nil
	}

	tokenCount := 0
	if resp.Usage != nil {
		tokenCount = resp.Usage.PromptTokens
	}

	if len(resp.Data) == 0 {
		return &BedrockInvokeEmbeddingResp{InputTextTokenCount: tokenCount}, nil
	}

	// Use model name to distinguish Cohere from Titan — not batch size.
	// A single-input Cohere request must still return the Cohere envelope format.
	model := resp.Model
	if model == "" {
		if resp.ExtraFields.ResolvedModelUsed != "" {
			model = resp.ExtraFields.ResolvedModelUsed
		} else if resp.ExtraFields.OriginalModelRequested != "" {
			model = resp.ExtraFields.OriginalModelRequested
		}
	}

	if strings.Contains(strings.ToLower(model), "cohere") {
		floats := make([][]float32, 0, len(resp.Data))
		for _, d := range resp.Data {
			float32Emb := make([]float32, len(d.Embedding.EmbeddingArray))
			for i, v := range d.Embedding.EmbeddingArray {
				float32Emb[i] = float32(v)
			}
			floats = append(floats, float32Emb)
		}
		return &BedrockInvokeCohereEmbeddingResp{
			Embeddings:   floats,
			ResponseType: "embeddings_floats",
		}, nil
	}

	// Titan format
	if resp.Data[0].Embedding.EmbeddingArray == nil {
		return &BedrockInvokeEmbeddingResp{InputTextTokenCount: tokenCount}, nil
	}
	float32Emb := make([]float32, len(resp.Data[0].Embedding.EmbeddingArray))
	for i, v := range resp.Data[0].Embedding.EmbeddingArray {
		float32Emb[i] = float32(v)
	}
	return &BedrockInvokeEmbeddingResp{
		Embedding:           float32Emb,
		InputTextTokenCount: tokenCount,
	}, nil
}

// toBedrockInvokeAnthropicResponse converts BifrostResponsesResponse to Anthropic Messages API format.
func toBedrockInvokeAnthropicResponse(resp *schemas.BifrostResponsesResponse, model string) *BedrockInvokeMessagesResponse {
	result := &BedrockInvokeMessagesResponse{
		Type: "message",
		Role: "assistant",
	}

	if resp.ID != nil {
		result.ID = *resp.ID
	} else {
		result.ID = "msg_" + uuid.New().String()
	}

	if resp.Model != "" {
		result.Model = resp.Model
	} else {
		result.Model = model
	}

	// Convert output items to Anthropic content blocks
	for _, item := range resp.Output {
		// Text content from message items
		if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeMessage && item.Content != nil {
			for _, contentPart := range item.Content.ContentBlocks {
				if contentPart.Text != nil {
					result.Content = append(result.Content, BedrockInvokeMessagesContentBlock{
						Type: "text",
						Text: *contentPart.Text,
					})
				}
			}
		}
		// Tool use content
		if item.ResponsesToolMessage != nil {
			var input interface{}
			if item.ResponsesToolMessage.Arguments != nil {
				// Try to parse arguments as JSON
				var parsed interface{}
				if err := sonic.UnmarshalString(*item.ResponsesToolMessage.Arguments, &parsed); err == nil {
					input = parsed
				} else {
					input = *item.ResponsesToolMessage.Arguments
				}
			}
			block := BedrockInvokeMessagesContentBlock{
				Type:  "tool_use",
				Input: input,
			}
			if item.ResponsesToolMessage.Name != nil {
				block.Name = *item.ResponsesToolMessage.Name
			}
			if item.ResponsesToolMessage.CallID != nil {
				block.ID = *item.ResponsesToolMessage.CallID
			}
			result.Content = append(result.Content, block)
		}
		// Reasoning content
		if item.ResponsesReasoning != nil {
			for _, summary := range item.ResponsesReasoning.Summary {
				if summary.Text != "" {
					result.Content = append(result.Content, BedrockInvokeMessagesContentBlock{
						Type:     "thinking",
						Thinking: summary.Text,
					})
				}
			}
		}
	}

	// Stop reason
	stopReason := "end_turn"
	if resp.IncompleteDetails != nil {
		stopReason = resp.IncompleteDetails.Reason
	} else {
		// Check for tool_use
		for _, block := range result.Content {
			if block.Type == "tool_use" {
				stopReason = "tool_use"
				break
			}
		}
	}
	result.StopReason = stopReason

	// Usage
	if resp.Usage != nil {
		result.Usage = &BedrockInvokeMessagesUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		}
	}

	return result
}

// toBedrockInvokeAI21Response converts BifrostResponsesResponse to AI21 Jamba format.
func toBedrockInvokeAI21Response(resp *schemas.BifrostResponsesResponse) *BedrockInvokeAI21Response {
	result := &BedrockInvokeAI21Response{}

	if resp.ID != nil {
		result.ID = *resp.ID
	} else {
		result.ID = uuid.New().String()
	}

	// Build a single choice from the output
	var contentParts []string
	for _, item := range resp.Output {
		if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeMessage && item.Content != nil {
			for _, contentPart := range item.Content.ContentBlocks {
				if contentPart.Text != nil {
					contentParts = append(contentParts, *contentPart.Text)
				}
			}
		}
	}

	finishReason := "stop"
	if resp.IncompleteDetails != nil {
		finishReason = resp.IncompleteDetails.Reason
	}

	result.Choices = []BedrockInvokeAI21Choice{
		{
			Index: 0,
			Message: BedrockInvokeAI21Message{
				Role:    "assistant",
				Content: strings.Join(contentParts, ""),
			},
			FinishReason: finishReason,
		},
	}

	if resp.Usage != nil {
		result.Usage = &BedrockInvokeAI21Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}

	return result
}

// ToBedrockInvokeMessagesStreamResponse converts a Bifrost Responses stream event to
// a BedrockStreamEvent with InvokeModelRawChunk for the invoke-with-response-stream endpoint.
func ToBedrockInvokeMessagesStreamResponse(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
	if resp == nil {
		return "", nil, fmt.Errorf("bifrost stream response is nil")
	}

	// Get model from the stream chunk's ExtraFields first (set on every chunk by the
	// streaming handler), then fall back to resp.Response fields (only present on the
	// final Completed event). Without checking resp.ExtraFields, early chunks would
	// have model="" and Nova streams would be mis-routed through the Anthropic path.
	model := ""
	if resp.Response != nil {
		if resp.Response.Model != "" {
			model = resp.Response.Model
		} else {
			extraFields := resp.Response.ExtraFields
			if extraFields.ResolvedModelUsed != "" {
				model = extraFields.ResolvedModelUsed
			} else if extraFields.OriginalModelRequested != "" {
				model = extraFields.OriginalModelRequested
			}
		}
	}

	// Nova models: delegate to existing converse stream response (same format)
	if schemas.IsNovaModel(model) {
		bedrockEvent, err := ToBedrockConverseStreamResponse(resp)
		if err != nil {
			return "", nil, err
		}
		if bedrockEvent == nil {
			return "", nil, nil
		}
		return "", bedrockEvent, nil
	}

	// For Anthropic models (and default): serialize as Anthropic Messages API SSE events,
	// then wrap in InvokeModelRawChunks. Some Bifrost events map to multiple Anthropic events
	// (e.g., Completed → message_delta + message_stop).
	rawChunks, err := toAnthropicInvokeStreamBytes(resp)
	if err != nil {
		return "", nil, err
	}
	if len(rawChunks) == 0 {
		return "", nil, nil
	}

	bedrockEvent := &BedrockStreamEvent{
		InvokeModelRawChunks: rawChunks,
	}

	return "", bedrockEvent, nil
}

// toAnthropicInvokeStreamBytes converts a Bifrost stream event into raw bytes representing
// the Anthropic Messages API streaming event JSON, suitable for wrapping in InvokeModelRawChunks.
// Returns a slice of byte slices since some Bifrost events map to multiple Anthropic events
// (e.g., Completed → message_delta + message_stop).
func toAnthropicInvokeStreamBytes(resp *schemas.BifrostResponsesStreamResponse) ([][]byte, error) {
	var event interface{}

	switch resp.Type {
	case schemas.ResponsesStreamResponseTypeCreated:
		// message_start — prefer resolved model for accurate family detection on early chunks
		model := resp.ExtraFields.ResolvedModelUsed
		if model == "" {
			model = resp.ExtraFields.OriginalModelRequested
		}
		msgStart := map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":      "msg_" + uuid.New().String(),
				"type":    "message",
				"role":    "assistant",
				"content": []interface{}{},
				"model":   model,
			},
		}
		if resp.Response != nil {
			if resp.Response.Model != "" {
				msgStart["message"].(map[string]interface{})["model"] = resp.Response.Model
			}
			if resp.Response.ID != nil {
				msgStart["message"].(map[string]interface{})["id"] = *resp.Response.ID
			}
		}
		event = msgStart

	case schemas.ResponsesStreamResponseTypeInProgress:
		// Skip — no Anthropic equivalent
		return nil, nil

	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		if resp.Item != nil && resp.Item.ResponsesToolMessage != nil {
			// content_block_start for tool_use
			idx := 0
			if resp.ContentIndex != nil {
				idx = *resp.ContentIndex
			}
			block := map[string]interface{}{
				"type":  "tool_use",
				"id":    "",
				"name":  "",
				"input": map[string]interface{}{},
			}
			if resp.Item.ResponsesToolMessage.CallID != nil {
				block["id"] = *resp.Item.ResponsesToolMessage.CallID
			}
			if resp.Item.ResponsesToolMessage.Name != nil {
				block["name"] = *resp.Item.ResponsesToolMessage.Name
			}
			event = map[string]interface{}{
				"type":          "content_block_start",
				"index":         idx,
				"content_block": block,
			}
		} else {
			// Skip — content_block_start is emitted on ContentPartAdded where we know the block type
			return nil, nil
		}

	case schemas.ResponsesStreamResponseTypeContentPartAdded:
		// Emit content_block_start here, where we know if it's thinking or text
		idx := 0
		if resp.ContentIndex != nil {
			idx = *resp.ContentIndex
		}
		// Check if this is a reasoning/thinking block
		if resp.Part != nil && resp.Part.Type == schemas.ResponsesOutputMessageContentTypeReasoning {
			event = map[string]interface{}{
				"type":  "content_block_start",
				"index": idx,
				"content_block": map[string]interface{}{
					"type":     "thinking",
					"thinking": "",
				},
			}
		} else {
			// text content block
			event = map[string]interface{}{
				"type":  "content_block_start",
				"index": idx,
				"content_block": map[string]interface{}{
					"type": "text",
					"text": "",
				},
			}
		}

	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		if resp.Delta != nil && *resp.Delta != "" {
			idx := 0
			if resp.ContentIndex != nil {
				idx = *resp.ContentIndex
			}
			event = map[string]interface{}{
				"type":  "content_block_delta",
				"index": idx,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": *resp.Delta,
				},
			}
		} else {
			return nil, nil
		}

	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		if resp.Delta != nil {
			idx := 0
			if resp.ContentIndex != nil {
				idx = *resp.ContentIndex
			}
			event = map[string]interface{}{
				"type":  "content_block_delta",
				"index": idx,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": *resp.Delta,
				},
			}
		} else {
			return nil, nil
		}

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
		idx := 0
		if resp.ContentIndex != nil {
			idx = *resp.ContentIndex
		}
		if resp.Signature != nil {
			event = map[string]interface{}{
				"type":  "content_block_delta",
				"index": idx,
				"delta": map[string]interface{}{
					"type":      "thinking_delta",
					"thinking":  "",
					"signature": *resp.Signature,
				},
			}
		} else if resp.Delta != nil && *resp.Delta != "" {
			event = map[string]interface{}{
				"type":  "content_block_delta",
				"index": idx,
				"delta": map[string]interface{}{
					"type":     "thinking_delta",
					"thinking": *resp.Delta,
				},
			}
		} else {
			return nil, nil
		}

	case schemas.ResponsesStreamResponseTypeOutputItemDone:
		idx := 0
		if resp.ContentIndex != nil {
			idx = *resp.ContentIndex
		}
		event = map[string]interface{}{
			"type":  "content_block_stop",
			"index": idx,
		}

	case schemas.ResponsesStreamResponseTypeContentPartDone,
		schemas.ResponsesStreamResponseTypeOutputTextDone,
		schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone:
		// Skip — the content_block_stop is emitted on OutputItemDone
		return nil, nil

	case schemas.ResponsesStreamResponseTypeCompleted:
		// Emit message_delta + message_stop as two separate events
		stopReason := "end_turn"
		if resp.Response != nil && resp.Response.IncompleteDetails != nil {
			stopReason = resp.Response.IncompleteDetails.Reason
		}

		// Build message_delta event
		messageDelta := map[string]interface{}{
			"type": "message_delta",
			"delta": map[string]interface{}{
				"stop_reason":   stopReason,
				"stop_sequence": nil,
			},
		}
		if resp.Response != nil && resp.Response.Usage != nil {
			messageDelta["usage"] = map[string]interface{}{
				"output_tokens": resp.Response.Usage.OutputTokens,
			}
		}

		// Build message_stop event
		messageStop := map[string]interface{}{
			"type": "message_stop",
		}

		// Marshal both events
		deltaBytes, err := providerUtils.MarshalSorted(messageDelta)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal message_delta event: %w", err)
		}
		stopBytes, err := providerUtils.MarshalSorted(messageStop)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal message_stop event: %w", err)
		}
		return [][]byte{deltaBytes, stopBytes}, nil

	default:
		return nil, nil
	}

	if event == nil {
		return nil, nil
	}

	eventBytes, err := providerUtils.MarshalSorted(event)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal invoke stream event: %w", err)
	}
	return [][]byte{eventBytes}, nil
}
