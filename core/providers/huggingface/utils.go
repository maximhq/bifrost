package huggingface

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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

	fmt.Printf("%s/api/models?%s\n", modelHubBaseURL, values.Encode())

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
		prov = "hf-inference"
		model = bifrostModelName
	}
	return prov, model, nil
}

// Defined for tasks given by https://huggingface.co/docs/inference-providers/en/index and makeURL logic at https://github.com/huggingface/huggingface.js/blob/c02dd89eff24593b304d72715247f7eef79b3b73/packages/inference/src/providers/providerHelper.ts#L111
func (provider *HuggingFaceProvider) getInferenceProviderRouteURL(ctx context.Context, inferenceProvider inferenceProvider, modelName string, requestType schemas.RequestType) (string, error) {
	defaultPath := ""
	switch inferenceProvider {
	case "fal-ai":
		defaultPath = fmt.Sprintf("/fal-ai/%s", modelName)
	case "hf-inference":
		defaultPath = fmt.Sprintf("/models/%s", modelName)
	case "nebius":
		if requestType == schemas.EmbeddingRequest {
			defaultPath = "/nebius/v1/embeddings"
		} else {
			return "", fmt.Errorf("nebius provider only supports embedding requests")
		}
	case "replicate":
		defaultPath = "/replicate/v1/prediction"
	case "sambanova":
		if requestType == schemas.EmbeddingRequest {
			defaultPath = "/sambanova/v1/embeddings"
		} else {
			return "", fmt.Errorf("sambanova provider only supports embedding requests")
		}
	case "scaleway":
		if requestType == schemas.EmbeddingRequest {
			defaultPath = "/scaleway/v1/embeddings"
		} else {
			return "", fmt.Errorf("scaleway provider only supports embedding requests")
		}

	default:
		return "", fmt.Errorf("unsupported inference provider: %s for action: %s", inferenceProvider, requestType)
	}
	return provider.networkConfig.BaseURL + providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType), nil
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
		bifrostErr.Error.Message = errorResp.Message
		return nil, bifrostErr
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var mappingResp HuggingFaceInferenceProviderMappingResponse
	if err := json.Unmarshal(body, &mappingResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	return convertToInferenceProviderMappings(&mappingResp), nil
}

// downloadAudioFromURL downloads audio data from a URL
func (provider *HuggingFaceProvider) downloadAudioFromURL(ctx context.Context, audioURL string) ([]byte, error) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(audioURL)
	req.Header.SetMethod(http.MethodGet)

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] downloading audio from URL: %s", audioURL))
	}

	err := provider.client.Do(req, resp)
	if err != nil {
		return nil, fmt.Errorf("failed to download audio: %w", err)
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

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] downloaded audio size: %d bytes", len(audioCopy)))
	}

	return audioCopy, nil
}

// extractIntFromInterface converts a variety of numeric types that may result
// from JSON unmarshaling into an int. Returns (value, true) on success.
func extractIntFromInterface(v interface{}) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int8:
		return int(t), true
	case int16:
		return int(t), true
	case int32:
		return int(t), true
	case int64:
		return int(t), true
	case uint:
		return int(t), true
	case uint8:
		return int(t), true
	case uint16:
		return int(t), true
	case uint32:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		return int(t), true
	case float32:
		return int(t), true
	case json.Number:
		if i64, err := t.Int64(); err == nil {
			return int(i64), true
		}
		if f64, err := t.Float64(); err == nil {
			return int(f64), true
		}
		return 0, false
	default:
		return 0, false
	}
}
