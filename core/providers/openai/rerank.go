package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type openAIRerankRequest struct {
	Model           string                   `json:"model"`
	Query           string                   `json:"query"`
	Documents       []schemas.RerankDocument `json:"documents"`
	TopN            *int                     `json:"top_n,omitempty"`
	MaxTokensPerDoc *int                     `json:"max_tokens_per_doc,omitempty"`
	Priority        *int                     `json:"priority,omitempty"`
	ExtraParams     map[string]interface{}   `json:"-"`
}

func (r *openAIRerankRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

func toOpenAIRerankRequest(request *schemas.BifrostRerankRequest) *openAIRerankRequest {
	if request == nil {
		return nil
	}

	converted := &openAIRerankRequest{
		Model:     request.Model,
		Query:     request.Query,
		Documents: request.Documents,
	}
	if request.Params != nil {
		converted.TopN = request.Params.TopN
		converted.MaxTokensPerDoc = request.Params.MaxTokensPerDoc
		converted.Priority = request.Params.Priority
		converted.ExtraParams = request.Params.ExtraParams
	}
	return converted
}

type openAIRerankResponse struct {
	ID      string                       `json:"id"`
	Results []openAIRerankResponseResult `json:"results"`
	Meta    *openAIRerankMeta            `json:"meta,omitempty"`
	Usage   *schemas.BifrostLLMUsage     `json:"usage,omitempty"`
}

type openAIRerankResponseResult struct {
	Index          int             `json:"index"`
	RelevanceScore float64         `json:"relevance_score"`
	Document       json.RawMessage `json:"document,omitempty"`
}

type openAIRerankMeta struct {
	BilledUnits *openAIRerankTokenUsage `json:"billed_units,omitempty"`
	Tokens      *openAIRerankTokenUsage `json:"tokens,omitempty"`
}

type openAIRerankTokenUsage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
}

func (response *openAIRerankResponse) toBifrostRerankResponse(documents []schemas.RerankDocument, returnDocuments bool) *schemas.BifrostRerankResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostRerankResponse{
		ID: response.ID,
	}
	for _, result := range response.Results {
		rerankResult := schemas.RerankResult{
			Index:          result.Index,
			RelevanceScore: result.RelevanceScore,
		}
		if doc := parseOpenAIRerankDocument(result.Document); doc != nil {
			rerankResult.Document = doc
		}
		bifrostResponse.Results = append(bifrostResponse.Results, rerankResult)
	}
	sort.SliceStable(bifrostResponse.Results, func(i, j int) bool {
		if bifrostResponse.Results[i].RelevanceScore == bifrostResponse.Results[j].RelevanceScore {
			return bifrostResponse.Results[i].Index < bifrostResponse.Results[j].Index
		}
		return bifrostResponse.Results[i].RelevanceScore > bifrostResponse.Results[j].RelevanceScore
	})
	if returnDocuments {
		for i := range bifrostResponse.Results {
			// Preserve any document the upstream already returned; only backfill from the request otherwise.
			if bifrostResponse.Results[i].Document != nil {
				continue
			}
			resultIndex := bifrostResponse.Results[i].Index
			if resultIndex >= 0 && resultIndex < len(documents) {
				bifrostResponse.Results[i].Document = schemas.Ptr(documents[resultIndex])
			}
		}
	}
	if response.Usage != nil {
		bifrostResponse.Usage = response.Usage
	} else if response.Meta != nil {
		bifrostResponse.Usage = openAIRerankUsage(response.Meta.Tokens)
		if bifrostResponse.Usage == nil {
			bifrostResponse.Usage = openAIRerankUsage(response.Meta.BilledUnits)
		}
	}
	return bifrostResponse
}

func parseOpenAIRerankDocument(raw json.RawMessage) *schemas.RerankDocument {
	if len(raw) == 0 {
		return nil
	}
	var text string
	if err := sonic.Unmarshal(raw, &text); err == nil {
		return &schemas.RerankDocument{Text: text}
	}

	var docMap map[string]interface{}
	if err := sonic.Unmarshal(raw, &docMap); err != nil {
		return nil
	}
	doc := &schemas.RerankDocument{}
	populated := false
	if text, ok := docMap["text"].(string); ok {
		doc.Text = text
		populated = true
	}
	if id, ok := docMap["id"].(string); ok {
		doc.ID = &id
		populated = true
	}
	meta := make(map[string]interface{})
	if rawMeta, ok := docMap["metadata"].(map[string]interface{}); ok {
		for k, v := range rawMeta {
			meta[k] = v
		}
	} else if rawMeta, ok := docMap["meta"].(map[string]interface{}); ok {
		for k, v := range rawMeta {
			meta[k] = v
		}
	}
	for k, v := range docMap {
		if k != "text" && k != "id" && k != "metadata" && k != "meta" {
			meta[k] = v
		}
	}
	if len(meta) > 0 {
		doc.Meta = meta
		populated = true
	}
	if !populated {
		return nil
	}
	return doc
}

func openAIRerankUsage(tokens *openAIRerankTokenUsage) *schemas.BifrostLLMUsage {
	if tokens == nil {
		return nil
	}
	promptTokens := 0
	completionTokens := 0
	hasUsage := false
	if tokens.InputTokens != nil {
		promptTokens = int(*tokens.InputTokens)
		hasUsage = true
	}
	if tokens.OutputTokens != nil {
		completionTokens = int(*tokens.OutputTokens)
		hasUsage = true
	}
	if !hasUsage {
		return nil
	}
	return &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

// HandleOpenAIRerankRequest handles rerank requests for custom OpenAI-compatible APIs.
func HandleOpenAIRerankRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostRerankRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()
	// Rerank JSON is always parsed in-process (no transport streaming). Skip
	// PrepareResponseStreaming so large-response threshold mode never leaves the body
	// on a stream-only path that finalizeOpenAIResponse would treat as unsupported here.
	activeClient := client

	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for k, v := range BearerAuthHeader(key) {
		req.Header.Set(k, v)
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return toOpenAIRerankRequest(request), nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	req.SetBody(jsonData)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	body, _, finalErr := finalizeOpenAIResponse(ctx, resp, latency, providerName, logger)
	respOwned = false
	if finalErr != nil {
		return nil, providerUtils.EnrichError(ctx, finalErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	response := &openAIRerankResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	returnDocuments := request.Params != nil && request.Params.ReturnDocuments != nil && *request.Params.ReturnDocuments
	bifrostResponse := response.toBifrostRerankResponse(request.Documents, returnDocuments)
	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResponse, nil
}
