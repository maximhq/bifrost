package openai

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// openAIDecisionsPath is OpenAI's native decisions endpoint.
const openAIDecisionsPath = "/v1/decisions"

// ToOpenAIDecisionRequest converts a normalized decision request into the
// body of OpenAI's POST /v1/decisions. OpenAI takes text where the normalized
// shape also carries Typesafe's structured values, so a structured input,
// instruction, or description is sent as sorted JSON text, and a predicate's
// criteria are folded into its instructions, as emulation renders them for a
// chat model. Input messages, including inline images, pass through unchanged.
//
// A request OpenAI cannot serve is rejected with a 400 for this attempt, so a
// fallback can take it: an empty (null) input, which OpenAI requires, an input
// part other than text or an image, and a question of an unsupported type or
// without its choices or levels.
func ToOpenAIDecisionRequest(request *schemas.BifrostDecisionRequest) (*OpenAIDecisionRequest, error) {
	if request == nil {
		return nil, providerUtils.InvalidRequestErrorf("decision request is nil")
	}
	if len(request.Questions) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("decision request requires at least one question")
	}
	if err := request.Input.Validate(); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err.Error())
	}
	if request.Input.IsEmpty() {
		return nil, providerUtils.InvalidRequestErrorf("OpenAI decisions require an input, and this request has none")
	}
	if partType := unsupportedOpenAIPartType(request.Input); partType != "" {
		return nil, providerUtils.InvalidRequestErrorf("OpenAI decisions read text and image input parts, not %q", partType)
	}

	input := request.Input
	if input.Structured != nil {
		text, err := providerUtils.MarshalSorted(input.Structured)
		if err != nil {
			return nil, providerUtils.InvalidRequestErrorf("decision input could not be encoded as text: %v", err)
		}
		input = schemas.DecisionInput{Text: schemas.Ptr(string(text))}
	}

	questions := make([]OpenAIDecisionQuestion, len(request.Questions))
	for i, question := range request.Questions {
		converted, err := toOpenAIDecisionQuestion(i, question)
		if err != nil {
			return nil, err
		}
		questions[i] = converted
	}

	return &OpenAIDecisionRequest{
		Model:            request.Model,
		Input:            input,
		Questions:        questions,
		SafetyIdentifier: request.SafetyIdentifier,
		ExtraParams:      request.ExtraParams,
	}, nil
}

// unsupportedOpenAIPartType returns the type of the first input part that is
// neither text nor an image ("untyped" for a part sent without a type), or ""
// when every part is one of those. Only a part's type is decoded for another
// kind, so it cannot be sent on.
func unsupportedOpenAIPartType(input schemas.DecisionInput) string {
	for _, message := range input.Messages {
		for _, part := range message.Content.Parts {
			switch part.Type {
			case schemas.DecisionInputPartTypeText, schemas.DecisionInputPartTypeImage:
			case "":
				return "untyped"
			default:
				return part.Type
			}
		}
	}
	return ""
}

// toOpenAIDecisionQuestion converts one question to OpenAI's text-only
// question. A level without a label is labelled by its index, the position
// that already identifies it (the map form defines its levels that way).
func toOpenAIDecisionQuestion(index int, question schemas.DecisionQuestion) (OpenAIDecisionQuestion, error) {
	if !question.Type.IsQuestionType() {
		return OpenAIDecisionQuestion{}, providerUtils.InvalidRequestErrorf("question %d has unsupported type %q; expected predicate, choice, or score", index, question.Type)
	}
	converted := OpenAIDecisionQuestion{
		Type:         question.Type,
		Name:         question.Name,
		Instructions: strings.TrimSpace(providerUtils.DecisionTextString(question.Instructions) + providerUtils.DecisionCriteriaText(question.Criteria)),
	}

	switch question.Type {
	case schemas.DecisionTypeChoice:
		if len(question.Choices) == 0 {
			return OpenAIDecisionQuestion{}, providerUtils.InvalidRequestErrorf("choice question %d requires choices", index)
		}
		converted.Choices = make([]OpenAIDecisionChoice, len(question.Choices))
		for j, choice := range question.Choices {
			if _, err := choice.Key(); err != nil {
				return OpenAIDecisionQuestion{}, providerUtils.InvalidRequestErrorf("choice %d of question %d: %s", j, index, err.Error())
			}
			converted.Choices[j] = OpenAIDecisionChoice{Value: choice.Value, Description: openAIText(choice.Description)}
		}
	case schemas.DecisionTypeScore:
		if len(question.Levels) == 0 {
			return OpenAIDecisionQuestion{}, providerUtils.InvalidRequestErrorf("score question %d requires levels", index)
		}
		converted.Levels = make([]OpenAIDecisionLevel, len(question.Levels))
		for j, level := range question.Levels {
			label := level.Label
			if label == "" {
				label = strconv.Itoa(j)
			}
			converted.Levels[j] = OpenAIDecisionLevel{Label: label, Description: openAIText(level.Description)}
		}
	}
	return converted, nil
}

// openAIText returns decision text as OpenAI takes it: nil for none, text
// unchanged, and a structured value as sorted JSON text.
func openAIText(text *schemas.DecisionText) *string {
	if text == nil {
		return nil
	}
	if text.Structured == nil {
		return text.Text
	}
	return schemas.Ptr(providerUtils.DecisionTextString(text))
}

// ToBifrostDecisionResponse converts OpenAI's decisions response into the
// normalized shape. OpenAI answers the questions in order, so each answer is
// matched to the question at its position; one naming a different question, or
// answering with a different type, is an error so a fallback can take the
// request. A refusal is kept as it is, an answer of any other type than its
// question's is an error, and an answer without a name takes its question's.
func (response *OpenAIDecisionResponse) ToBifrostDecisionResponse(request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, error) {
	if len(response.Answers) != len(request.Questions) {
		return nil, fmt.Errorf("OpenAI returned %d decision answers for %d questions", len(response.Answers), len(request.Questions))
	}
	answers := make([]schemas.DecisionAnswer, len(response.Answers))
	for i, answer := range response.Answers {
		question := request.Questions[i]
		if answer.Name != nil && question.Name != nil && *answer.Name != *question.Name {
			return nil, fmt.Errorf("OpenAI decision answer %d is named %q; expected %q", i, *answer.Name, *question.Name)
		}
		if answer.Type != schemas.DecisionTypeRefusal && answer.Type != question.Type {
			return nil, fmt.Errorf("OpenAI answered decision question %d as %q; expected %q", i, answer.Type, question.Type)
		}
		if answer.Name == nil {
			answer.Name = question.Name
		}
		// OpenAI sends no legend; it is rebuilt from the question's levels so a
		// score answer reads the same whichever provider served it.
		if answer.Type == schemas.DecisionTypeScore && answer.Legend == nil {
			answer.Legend = providerUtils.DecisionScoreLegend(question)
		}
		answers[i] = answer
	}
	return &schemas.BifrostDecisionResponse{
		Model:   response.Model,
		Answers: answers,
		Usage:   response.Usage.ToBifrostLLMUsage(),
	}, nil
}

// HandleOpenAIDecisionRequest sends a decision request to an OpenAI-compatible
// POST /v1/decisions and converts the reply. The reply is buffered and parsed
// in-process, as for rerank: it is a short list of answers, and large-response
// passthrough would relay upstream bytes verbatim to routes that do not speak
// OpenAI's shape.
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

	body, _, finalErr := finalizeOpenAIResponse(ctx, resp, latency, providerName, logger)
	respOwned = false
	if finalErr != nil {
		return nil, providerUtils.EnrichError(ctx, finalErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	response := &OpenAIDecisionResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResponse, err := response.ToBifrostDecisionResponse(request)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(err.Error(), nil), jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
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
	return bifrostResponse, nil
}
