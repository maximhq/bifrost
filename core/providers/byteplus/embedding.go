package byteplus

import (
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// bytePlusEmbeddingConcurrency caps the in-flight /embeddings/multimodal calls one Embedding request fans out to.
const bytePlusEmbeddingConcurrency = 8

// BytePlusMultimodalEmbeddingInput is one part of a /embeddings/multimodal input.
type BytePlusMultimodalEmbeddingInput struct {
	Type     string                 `json:"type"` // "text", "image_url", "video_url"
	Text     *string                `json:"text,omitempty"`
	ImageURL *BytePlusVideoMediaURL `json:"image_url,omitempty"`
	VideoURL *BytePlusVideoMediaURL `json:"video_url,omitempty"`
}

// BytePlusMultimodalEmbeddingRequest is the body for POST /api/v3/embeddings/multimodal.
// Ark fuses every input part into a single vector, so one request carries one Bifrost input item.
type BytePlusMultimodalEmbeddingRequest struct {
	Model          string                             `json:"model"`
	Input          []BytePlusMultimodalEmbeddingInput `json:"input"`
	EncodingFormat *string                            `json:"encoding_format,omitempty"`
	Dimensions     *int                               `json:"dimensions,omitempty"`
	ExtraParams    map[string]interface{}             `json:"-"`
}

func (r *BytePlusMultimodalEmbeddingRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// BytePlusMultimodalEmbeddingResponse is the body returned by POST /api/v3/embeddings/multimodal.
type BytePlusMultimodalEmbeddingResponse struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Data  struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			TextTokens  int `json:"text_tokens"`
			ImageTokens int `json:"image_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// ToBytePlusMultimodalEmbeddingRequest converts one Bifrost embedding input item to an /embeddings/multimodal body.
// Item params override the request-level params, since every item is its own upstream call.
func ToBytePlusMultimodalEmbeddingRequest(model string, item schemas.EmbeddingInputItem, defaults *schemas.EmbeddingParameters) (*BytePlusMultimodalEmbeddingRequest, error) {
	if len(item.Content) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("embedding input item has no content")
	}
	req := &BytePlusMultimodalEmbeddingRequest{
		Model: model,
		Input: make([]BytePlusMultimodalEmbeddingInput, 0, len(item.Content)),
	}
	for _, part := range item.Content {
		switch part.Type {
		case schemas.EmbeddingContentPartTypeText:
			if part.Text == nil {
				return nil, providerUtils.InvalidRequestErrorf("text embedding part has no text")
			}
			req.Input = append(req.Input, BytePlusMultimodalEmbeddingInput{Type: "text", Text: part.Text})
		case schemas.EmbeddingContentPartTypeImage, schemas.EmbeddingContentPartTypeVideo:
			media := part.Image
			partType := "image_url"
			if part.Type == schemas.EmbeddingContentPartTypeVideo {
				media, partType = part.Video, "video_url"
			}
			if err := media.Validate(); err != nil {
				return nil, providerUtils.InvalidRequestErrorf("%s", err)
			}
			mediaURL := &BytePlusVideoMediaURL{URL: bytePlusMediaURL(media)}
			input := BytePlusMultimodalEmbeddingInput{Type: partType}
			if partType == "image_url" {
				input.ImageURL = mediaURL
			} else {
				input.VideoURL = mediaURL
			}
			req.Input = append(req.Input, input)
		default:
			return nil, providerUtils.InvalidRequestErrorf("byteplus embeddings do not support %s input parts", part.Type)
		}
	}

	params := defaults
	if item.Params != nil {
		params = item.Params
	}
	if params == nil {
		return req, nil
	}
	if params.EncodingFormat != nil && *params.EncodingFormat != schemas.EmbeddingEncodingFloat {
		return nil, providerUtils.InvalidRequestErrorf("byteplus embeddings only return float vectors, got encoding_format %q", *params.EncodingFormat)
	}
	req.Dimensions = params.Dimensions
	if len(params.ExtraParams) > 0 {
		req.ExtraParams = make(map[string]interface{}, len(params.ExtraParams))
		maps.Copy(req.ExtraParams, params.ExtraParams)
	}
	return req, nil
}

// bytePlusMediaURL returns the media part as a URL or a base64 data URI.
func bytePlusMediaURL(media *schemas.EmbeddingMediaPart) string {
	if media.URL != nil {
		return *media.URL
	}
	if media.MIMEType != nil && *media.MIMEType != "" && !strings.HasPrefix(*media.Data, "data:") {
		return "data:" + *media.MIMEType + ";base64," + *media.Data
	}
	return *media.Data
}

// Embedding embeds each input item through Ark's /embeddings/multimodal API. BytePlus serves no
// text-only /embeddings model, and the multimodal endpoint fuses a request's parts into one vector,
// so each item is sent as its own call (at most bytePlusEmbeddingConcurrency at a time) and the
// vectors come back in input order with usage summed across calls.
func (provider *BytePlusProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		return nil, providerUtils.NewBifrostBadRequestError("large payload passthrough is not supported for byteplus embeddings")
	}
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// A raw request body is one upstream call by definition.
	bodies := make([][]byte, 0, max(len(request.Input), 1))
	if rawBody, ok := providerUtils.CheckAndGetRawRequestBody(ctx, request); ok {
		bodies = append(bodies, rawBody)
	} else {
		if len(request.Input) == 0 {
			return nil, providerUtils.NewBifrostBadRequestError("no input provided for byteplus embedding")
		}
		for i := range request.Input {
			item := request.Input[i]
			jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
				ctx,
				request,
				func() (providerUtils.RequestBodyWithExtraParams, error) {
					return ToBytePlusMultimodalEmbeddingRequest(request.Model, item, request.Params)
				})
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			bodies = append(bodies, jsonData)
		}
	}

	type embeddingResult struct {
		resp        BytePlusMultimodalEmbeddingResponse
		rawRequest  interface{}
		rawResponse interface{}
		latency     time.Duration
		err         *schemas.BifrostError
	}
	results := make([]embeddingResult, len(bodies))
	var wg sync.WaitGroup
	sem := make(chan struct{}, bytePlusEmbeddingConcurrency)
	for i := range bodies {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			body, latency, bifrostErr := provider.doBytePlusRequest(ctx, key, http.MethodPost, "/api/v3/embeddings/multimodal", "", bodies[i], sendBackRawRequest, sendBackRawResponse)
			results[i].latency = latency
			if bifrostErr != nil {
				results[i].err = bifrostErr
				return
			}
			results[i].rawRequest, results[i].rawResponse, results[i].err = providerUtils.HandleProviderResponseCtx(ctx, body, &results[i].resp, bodies[i], sendBackRawRequest, sendBackRawResponse)
		}(i)
	}
	wg.Wait()

	bifrostResp := &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Model:  request.Model,
		Data:   make([]schemas.EmbeddingData, 0, len(results)),
		Usage:  &schemas.BifrostLLMUsage{},
	}
	var latency time.Duration
	rawRequests := make([]interface{}, 0, len(results))
	rawResponses := make([]interface{}, 0, len(results))
	for i := range results {
		result := &results[i]
		latency = max(latency, result.latency)
		if result.err != nil {
			return nil, providerUtils.SetErrorLatency(result.err, latency)
		}
		if len(result.resp.Data.Embedding) == 0 {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(fmt.Sprintf("byteplus returned no embedding for input %d", i), nil), latency)
		}
		if result.resp.Model != "" {
			bifrostResp.Model = result.resp.Model
		}
		bifrostResp.Data = append(bifrostResp.Data, schemas.EmbeddingData{
			Index:     i,
			Object:    "embedding",
			Embedding: schemas.EmbeddingStruct{EmbeddingArray: result.resp.Data.Embedding},
		})
		usage := result.resp.Usage
		bifrostResp.Usage.PromptTokens += usage.PromptTokens
		bifrostResp.Usage.TotalTokens += usage.TotalTokens
		if usage.PromptTokensDetails.TextTokens > 0 || usage.PromptTokensDetails.ImageTokens > 0 {
			if bifrostResp.Usage.PromptTokensDetails == nil {
				bifrostResp.Usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
			}
			bifrostResp.Usage.PromptTokensDetails.TextTokens += usage.PromptTokensDetails.TextTokens
			bifrostResp.Usage.PromptTokensDetails.ImageTokens += usage.PromptTokensDetails.ImageTokens
		}
		rawRequests = append(rawRequests, result.rawRequest)
		rawResponses = append(rawResponses, result.rawResponse)
	}

	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = singleOrList(rawRequests)
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = singleOrList(rawResponses)
	}
	return bifrostResp, nil
}

// singleOrList keeps the raw payload of a one-call request in its natural shape.
func singleOrList(raws []interface{}) interface{} {
	if len(raws) == 1 {
		return raws[0]
	}
	return raws
}
