package huggingface

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// According to https://huggingface.co/docs/inference-providers/en/tasks/chat-completion the
	// OpenAI-compatible router lives under the /v1 prefix, so we wire that in as the default base URL.
	defaultInferenceBaseURL = "https://router.huggingface.co"
	modelHubBaseURL         = "https://huggingface.co"

	//For custom deployments, HF offers inference endpoints under
	// inferenceBaseEndpointsEndpointBaseURL = "https://api.endpoints.huggingface.cloud/v2"
)

type inferenceProvider string

const (
	cerebras      inferenceProvider = "cerebras"
	cohere        inferenceProvider = "cohere"
	falAI         inferenceProvider = "fal-ai"
	featherlessAI inferenceProvider = "featherless-ai"
	fireworksAI   inferenceProvider = "fireworks-ai"
	groq          inferenceProvider = "groq"
	hfInference   inferenceProvider = "hf-inference"
	hyperbolic    inferenceProvider = "hyperbolic"
	nebius        inferenceProvider = "nebius"
	novita        inferenceProvider = "novita"
	nscale        inferenceProvider = "nscale"
	ovhcloud      inferenceProvider = "ovhcloud"
	publicai      inferenceProvider = "publicai"
	replicate     inferenceProvider = "replicate"
	sambanova     inferenceProvider = "sambanova"
	scaleway      inferenceProvider = "scaleway"
	together      inferenceProvider = "together"
	wavespeed     inferenceProvider = "wavespeed"
	zaiOrg        inferenceProvider = "zai-org"
	auto          inferenceProvider = "auto"
)

// List of supported inference providers (kept in sync with HF docs/JS SDK)
var INFERENCE_PROVIDERS = []inferenceProvider{
	cerebras,
	cohere,
	falAI,
	featherlessAI,
	fireworksAI,
	groq,
	hfInference,
	hyperbolic,
	nebius,
	novita,
	nscale,
	ovhcloud,
	publicai,
	replicate,
	sambanova,
	scaleway,
	together,
	wavespeed,
	zaiOrg,
}

// PROVIDERS_OR_POLICIES is the above list plus the special "auto" policy
var PROVIDERS_OR_POLICIES = func() []inferenceProvider {
	out := make([]inferenceProvider, 0, len(INFERENCE_PROVIDERS)+1)
	out = append(out, INFERENCE_PROVIDERS...)
	out = append(out, "auto")
	return out
}()

func (provider *HuggingFaceProvider) buildModelHubURL(request *schemas.BifrostListModelsRequest, inferenceProvider inferenceProvider) string {
	values := url.Values{}

	// Add inference_provider parameter to filter models served by Hugging Face's inference provider
	// According to https://huggingface.co/docs/inference-providers/hub-api
	limit := request.PageSize
	if limit <= 0 {
		limit = defaultModelFetchLimit
	}
	if limit > maxModelFetchLimit {
		limit = maxModelFetchLimit
	}
	values.Set("limit", strconv.Itoa(limit))
	values.Set("full", "1")
	values.Set("sort", "likes")
	values.Set("direction", "-1")
	values.Set("inference_provider", string(inferenceProvider))

	for key, value := range request.ExtraParams {
		switch typed := value.(type) {
		case string:
			if typed != "" {
				values.Set(key, typed)
			}
		case fmt.Stringer:
			values.Set(key, typed.String())
		case int:
			values.Set(key, strconv.Itoa(typed))
		case float64:
			values.Set(key, strconv.FormatFloat(typed, 'f', -1, 64))
		case bool:
			values.Set(key, strconv.FormatBool(typed))
		default:
			values.Set(key, fmt.Sprintf("%v", typed))
		}
	}

	return fmt.Sprintf("%s/api/models?%s", modelHubBaseURL, values.Encode())
}

func (provider *HuggingFaceProvider) buildModelInferenceProviderURL(modelName string) string {
	values := url.Values{}
	values.Set("expand[]", "pipeline_tag")
	values.Set("expand[]", "inferenceProviderMapping")
	return fmt.Sprintf("%s/api/models/%s?%s", modelHubBaseURL, modelName, values.Encode())
}

func splitIntoModelProvider(bifrostModelName string) (inferenceProvider, string, error) {
	// Extract provider and model name
	t := strings.Count(bifrostModelName, "/")
	if t == 0 {
		return "", "", fmt.Errorf("invalid model name format: %s", bifrostModelName)
	}
	var prov inferenceProvider
	var model string
	if t > 1 {
		before, after, _ := strings.Cut(bifrostModelName, "/")
		prov = inferenceProvider(before)
		model = after
	} else if t == 1 {
		prov = auto
		model = bifrostModelName
	}
	return prov, model, nil
}

// Defined for tasks given by https://huggingface.co/docs/inference-providers/en/index and makeURL logic at https://github.com/huggingface/huggingface.js/blob/c02dd89eff24593b304d72715247f7eef79b3b73/packages/inference/src/providers/providerHelper.ts#L111
func (provider *HuggingFaceProvider) getInferenceProviderRouteURL(ctx *schemas.BifrostContext, inferenceProvider inferenceProvider, modelName string, requestType schemas.RequestType) (string, error) {
	defaultPath := ""
	switch inferenceProvider {
	case falAI:
		defaultPath = fmt.Sprintf("/fal-ai/%s", modelName)
	case hfInference:
		var pipeline string
		switch requestType {
		case schemas.EmbeddingRequest:
			pipeline = "feature-extraction"
		case schemas.SpeechRequest:
			pipeline = "text-to-speech"
		case schemas.ImageGenerationRequest:
			return provider.buildRequestURL(ctx, fmt.Sprintf("/hf-inference/models/%s", modelName), requestType), nil
		case schemas.TranscriptionRequest:
			return provider.buildRequestURL(ctx, fmt.Sprintf("/hf-inference/models/%s", modelName), requestType), nil
		default:
			pipeline = "chat-completion"
		}
		defaultPath = fmt.Sprintf("/hf-inference/models/%s/pipeline/%s", modelName, pipeline)
	case nebius:
		if requestType == schemas.EmbeddingRequest {
			defaultPath = "/nebius/v1/embeddings"
		} else if requestType == schemas.ImageGenerationRequest {
			defaultPath = "/nebius/v1/images/generations"
		} else {
			return "", fmt.Errorf("nebius provider only supports embedding and image generation requests")
		}
	case replicate:
		defaultPath = "/replicate/v1/prediction"
	case together:
		if requestType == schemas.ImageGenerationRequest {
			defaultPath = "/together/v1/images/generations"
		} else {
			return "", fmt.Errorf("together provider only supports image generation requests")
		}
	case sambanova:
		if requestType == schemas.EmbeddingRequest {
			defaultPath = "/sambanova/v1/embeddings"
		} else {
			return "", fmt.Errorf("sambanova provider only supports embedding requests")
		}
	case scaleway:
		if requestType == schemas.EmbeddingRequest {
			defaultPath = "/scaleway/v1/embeddings"
		} else {
			return "", fmt.Errorf("scaleway provider only supports embedding requests")
		}

	default:
		return "", fmt.Errorf("unsupported inference provider: %s for action: %s", inferenceProvider, requestType)
	}
	return provider.buildRequestURL(ctx, defaultPath, requestType), nil
}

// convertToInferenceProviderMappings converts HuggingFaceInferenceProviderMappingResponse to a map of HuggingFaceInferenceProviderMapping with ProviderName as key
func convertToInferenceProviderMappings(resp *HuggingFaceInferenceProviderMappingResponse) map[inferenceProvider]HuggingFaceInferenceProviderMapping {
	if resp == nil || resp.InferenceProviderMapping == nil {
		return nil
	}

	mappings := make(map[inferenceProvider]HuggingFaceInferenceProviderMapping, len(resp.InferenceProviderMapping))
	for providerKey, providerInfo := range resp.InferenceProviderMapping {
		providerName := inferenceProvider(providerKey)
		mappings[providerName] = HuggingFaceInferenceProviderMapping{
			ProviderTask:    providerInfo.Task,
			ProviderModelID: providerInfo.ProviderModelID,
		}
	}

	return mappings
}

func (provider *HuggingFaceProvider) getModelInferenceProviderMapping(ctx context.Context, huggingfaceModelName string) (map[inferenceProvider]HuggingFaceInferenceProviderMapping, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Check cache first
	if cached, ok := provider.modelProviderMappingCache.Load(huggingfaceModelName); ok {
		if mappings, ok := cached.(map[inferenceProvider]HuggingFaceInferenceProviderMapping); ok {

			return mappings, nil
		}
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(provider.buildModelInferenceProviderURL(huggingfaceModelName))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	_, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		var errorResp HuggingFaceHubError
		bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		if strings.TrimSpace(errorResp.Message) != "" {
			bifrostErr.Error.Message = errorResp.Message
		}
		return nil, bifrostErr
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var mappingResp HuggingFaceInferenceProviderMappingResponse
	if err := sonic.Unmarshal(body, &mappingResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	mappings := convertToInferenceProviderMappings(&mappingResp)

	// Store in cache
	if mappings != nil {
		provider.modelProviderMappingCache.Store(huggingfaceModelName, mappings)
	}

	return mappings, nil
}

// getValidatedProviderModelID fetches the inference provider mapping for a model
// and validates that the given inferenceProvider has a mapping with the expected task.
// On success it returns the provider-specific model id. On failure it returns a
// BifrostError indicating the operation isn't supported for the requested
// request type or provider.
func (provider *HuggingFaceProvider) getValidatedProviderModelID(ctx context.Context, inferenceProvider inferenceProvider, huggingfaceModelName string, requiredTask string, requestType schemas.RequestType) (string, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	providerMapping, bifrostErr := provider.getModelInferenceProviderMapping(ctx, huggingfaceModelName)
	if bifrostErr != nil {
		return "", bifrostErr
	}

	if providerMapping == nil {
		return "", providerUtils.NewUnsupportedOperationError(requestType, providerName)
	}

	mapping, ok := providerMapping[inferenceProvider]
	if !ok || mapping.ProviderModelID == "" || mapping.ProviderTask != requiredTask {
		return "", providerUtils.NewUnsupportedOperationError(requestType, providerName)
	}

	return mapping.ProviderModelID, nil
}

// downloadAudioFromURL downloads audio data from a URL
func (provider *HuggingFaceProvider) downloadAudioFromURL(ctx context.Context, audioURL string) ([]byte, error) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(audioURL)
	req.Header.SetMethod(http.MethodGet)

	_, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, fmt.Errorf("failed to download audio: %v", bifrostErr)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, fmt.Errorf("failed to download audio: status=%d", resp.StatusCode())
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio data: %w", err)
	}

	// Copy the body to avoid use-after-free
	audioCopy := append([]byte(nil), body...)

	return audioCopy, nil
}

func getMimeTypeForAudioType(audioType string) string {
	if audioType == "" {
		return "audio/mpeg"
	}

	// Lowercase for comparison and trim parameters if present (e.g.);
	t := strings.ToLower(strings.TrimSpace(audioType))

	// If it already starts with "audio/", normalise some known variants
	if strings.HasPrefix(t, "audio/") {
		switch t {
		case "audio/mp3":
			return "audio/mpeg"
		default:
			return t
		}
	}

	return "audio/mpeg"

}
