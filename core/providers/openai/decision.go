package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToOpenAIDecisionRequest converts an ordered Bifrost decision request into the
// wire request for OpenAI's decisions endpoint. Malformed requests are rejected
// with a 400 rather than approximated; validation is never stricter than the
// official SDK types, so limits that are not documented are left to the
// endpoint.
func ToOpenAIDecisionRequest(request *schemas.BifrostDecisionRequest) (*OpenAIDecisionRequest, error) {
	if request == nil {
		return nil, providerUtils.InvalidRequestErrorf("request body is required")
	}
	if !request.UsesOrderedForm() {
		return nil, providerUtils.InvalidRequestErrorf("decision request must carry input and ordered questions")
	}
	if request.UsesMapForm() {
		return nil, providerUtils.InvalidRequestErrorf("decision request carries both questions and ordered questions; send exactly one")
	}
	if request.Input == nil || (request.Input.Text == nil && len(request.Input.Messages) == 0) {
		return nil, providerUtils.InvalidRequestErrorf("input is required")
	}
	if request.Input.Text != nil && len(request.Input.Messages) > 0 {
		return nil, providerUtils.InvalidRequestErrorf("input carries both text and messages; send exactly one")
	}
	if len(request.OrderedQuestions) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("at least one question is required")
	}
	if err := validateOrderedQuestions(request.OrderedQuestions); err != nil {
		return nil, err
	}

	return &OpenAIDecisionRequest{
		Model:            request.Model,
		Input:            request.Input,
		Questions:        request.OrderedQuestions,
		SafetyIdentifier: request.SafetyIdentifier,
		ExtraParams:      request.ExtraParams,
	}, nil
}

// validateOrderedQuestions checks each question's type and type-specific
// fields, and that question names are unique so answers can be told apart.
// A name is optional, so unnamed questions are never compared.
func validateOrderedQuestions(questions []schemas.DecisionOrderedQuestion) error {
	names := make(map[string]struct{}, len(questions))
	for i, question := range questions {
		if !question.Type.IsQuestionKind() {
			return providerUtils.InvalidRequestErrorf("question %d has unsupported type %q; expected predicate, choice, or score", i, question.Type)
		}
		if question.Name != nil {
			if _, seen := names[*question.Name]; seen {
				return providerUtils.InvalidRequestErrorf("question name %q is used more than once", *question.Name)
			}
			names[*question.Name] = struct{}{}
		}
		switch question.Type {
		case schemas.DecisionOrderedKindChoice:
			if len(question.Choices) == 0 {
				return providerUtils.InvalidRequestErrorf("choice question %d requires choices", i)
			}
			for j, choice := range question.Choices {
				if (choice.Value.Str == nil) == (choice.Value.Bool == nil) || choice.Value.Num != nil {
					return providerUtils.InvalidRequestErrorf("choice %d of question %d must have exactly one of a string or boolean value", j, i)
				}
			}
		case schemas.DecisionOrderedKindScore:
			if len(question.Levels) == 0 {
				return providerUtils.InvalidRequestErrorf("score question %d requires levels", i)
			}
		}
	}
	return nil
}

// ToBifrostDecisionResponse converts a native decisions response into the
// shared decision shape. Answers keep the order and variants the endpoint
// returned, including a refusal and any answer type this schema does not
// model.
func (response *OpenAIDecisionResponse) ToBifrostDecisionResponse() *schemas.BifrostDecisionResponse {
	if response == nil {
		return nil
	}
	return &schemas.BifrostDecisionResponse{
		ID:             response.ID,
		Model:          response.Model,
		OrderedAnswers: response.Answers,
		Usage:          response.Usage.ToBifrostLLMUsage(),
	}
}

// HandleOpenAIDecisionRequest sends an ordered decision request to OpenAI's
// decisions endpoint and converts the reply.
func HandleOpenAIDecisionRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostDecisionRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToOpenAIDecisionRequest(request)
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	// The reply is always buffered and parsed in-process, so response streaming is
	// not prepared on the client (as for rerank). A decision reply is a small list
	// of answers, and large-response passthrough would relay the upstream bytes to
	// the caller verbatim, which is only correct on a route that speaks OpenAI's
	// wire format.
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for k, v := range BearerAuthHeader(key) {
		req.Header.Set(k, v)
	}
	// A nil body means large-payload passthrough staged the request as a
	// stream; apply it instead of sending an empty body.
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, providerName) {
		req.SetBody(jsonData)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		logger.Debug(fmt.Sprintf("error from %s provider: status %d", providerName, resp.StatusCode()))
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	body, largeResponse, finalErr := finalizeOpenAIResponse(ctx, resp, latency, providerName, logger)
	respOwned = false
	if finalErr != nil {
		return nil, providerUtils.EnrichError(ctx, finalErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}
	// Not reachable with the buffered client above. If a streaming client ever
	// reaches this handler, fail with a clear error instead of parsing the empty
	// body that a streamed large response leaves behind.
	if largeResponse != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("decision response was streamed instead of buffered and cannot be parsed", nil), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	response := &OpenAIDecisionResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResponse := response.ToBifrostDecisionResponse()
	if bifrostResponse.Model == "" {
		bifrostResponse.Model = request.Model
	}
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}
	// Native drop-in routes relay this verbatim so fields outside the shared
	// shape survive.
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err == nil {
		bifrostResponse.NativeResponse = json.RawMessage(compact.Bytes())
	}
	return bifrostResponse, nil
}
