package gemini

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// geminiDecisionsPath is the Gemini Developer API's decisions endpoint, under
// the provider's /v1beta base URL.
const geminiDecisionsPath = "/decisions"

// geminiDecisionMaxKeyLength is the longest question key the API accepts.
const geminiDecisionMaxKeyLength = 64

// geminiDecisionQuestionTypes maps the normalized question types to the ones
// the decisions API serves; its "noul" is the normalized predicate.
var geminiDecisionQuestionTypes = map[schemas.DecisionType]string{
	schemas.DecisionTypePredicate: "noul",
	schemas.DecisionTypeChoice:    "choice",
	schemas.DecisionTypeScore:     "score",
}

// geminiDecisionResolutions maps an image part's detail to the API's media
// resolution. "auto" and anything else leave the API's default.
var geminiDecisionResolutions = map[string]string{
	"low":      "low",
	"high":     "high",
	"original": "ultra_high",
}

// ToGeminiDecisionRequest converts a normalized decision request into the body
// of POST /v1beta/decisions.
//
// Questions are keyed by their names (see schemas.DecisionQuestionNames); a
// name the API does not accept as a key (1-64 of [A-Za-z0-9_-]) is sent under
// a positional key instead, and ToBifrostDecisionResponse maps it back.
// Predicate criteria become true_description/false_description, choices an
// option -> description map, and levels an ordered list of descriptions with
// their labels folded in. Text input is sent as a string, structured input as
// sorted JSON text, and messages as content blocks: text, output text,
// refusal and JSON parts as text, input images as image blocks.
//
// A request the API cannot serve is rejected with a 400 for this attempt, so a
// fallback can take it: an empty input, a tool call or tool output item, an
// image sent by reference, a part of another type, image instructions, and a
// question of another type (box) or without its choices or levels.
func ToGeminiDecisionRequest(request *schemas.BifrostDecisionRequest) (*GeminiDecisionRequest, error) {
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
		return nil, providerUtils.InvalidRequestErrorf("Gemini decisions require an input, and this request has none")
	}
	if request.Input.HasAssetParts() {
		return nil, providerUtils.InvalidRequestErrorf("decision input carries an image sent by reference, which Gemini decisions cannot read; send it inline")
	}
	input, err := toGeminiDecisionInput(request.Input)
	if err != nil {
		return nil, err
	}
	keys, err := geminiDecisionQuestionKeys(request.Questions)
	if err != nil {
		return nil, err
	}
	questions := make(map[string]GeminiDecisionQuestion, len(request.Questions))
	for i, question := range request.Questions {
		native, err := toGeminiDecisionQuestion(i, question)
		if err != nil {
			return nil, err
		}
		questions[keys[i]] = native
	}
	return &GeminiDecisionRequest{
		Model:       request.Model,
		Input:       input,
		Questions:   questions,
		ExtraParams: request.ExtraParams,
	}, nil
}

// geminiDecisionQuestionKeys returns the key each question is sent under: its
// name when the API accepts it as a key, otherwise "question_<position>" (from
// 1, as schemas.DecisionQuestionNames names unnamed questions), suffixed until
// it collides with no other key.
func geminiDecisionQuestionKeys(questions []schemas.DecisionQuestion) ([]string, error) {
	names, err := schemas.DecisionQuestionNames(questions)
	if err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err.Error())
	}
	keys := make([]string, len(names))
	used := make(map[string]bool, len(names))
	for i, name := range names {
		if isGeminiDecisionKey(name) {
			keys[i] = name
			used[name] = true
		}
	}
	for i := range names {
		if keys[i] != "" {
			continue
		}
		key := "question_" + strconv.Itoa(i+1)
		for suffix := 2; used[key]; suffix++ {
			key = "question_" + strconv.Itoa(i+1) + "_" + strconv.Itoa(suffix)
		}
		keys[i] = key
		used[key] = true
	}
	return keys, nil
}

// isGeminiDecisionKey reports whether key is a valid question key: 1 to 64
// ASCII letters, digits, underscores, or hyphens.
func isGeminiDecisionKey(key string) bool {
	if key == "" || len(key) > geminiDecisionMaxKeyLength {
		return false
	}
	for i := range len(key) {
		c := key[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// toGeminiDecisionInput renders the input: text as a string, a structured
// value as sorted JSON text (the API reads JSON as text), and messages as one
// list of content blocks. The API concatenates adjacent text blocks with no
// separator, so each message after the first starts with a newline block.
// Roles are not sent: the API takes evidence, not a conversation.
func toGeminiDecisionInput(input schemas.DecisionInput) (interface{}, error) {
	switch {
	case input.Text != nil:
		return *input.Text, nil
	case input.Structured != nil:
		text, err := providerUtils.MarshalSorted(input.Structured)
		if err != nil {
			return nil, providerUtils.InvalidRequestErrorf("decision input could not be encoded as text: %v", err)
		}
		return string(text), nil
	}
	blocks := make([]GeminiDecisionContent, 0, len(input.Messages))
	for _, message := range input.Messages {
		if message.Type != nil && *message.Type != schemas.DecisionInputItemTypeMessage {
			return nil, providerUtils.InvalidRequestErrorf("Gemini decisions read messages, not a %q input item; send it as text", *message.Type)
		}
		// A message's first block is preceded by a newline once another
		// message has added content.
		separated := len(blocks) == 0
		if message.Content.Text != nil {
			if *message.Content.Text != "" {
				blocks = appendGeminiDecisionBlock(blocks, geminiDecisionTextBlock(*message.Content.Text), &separated)
			}
			continue
		}
		for _, part := range message.Content.Parts {
			block, ok, err := toGeminiDecisionBlock(part)
			if err != nil {
				return nil, err
			}
			if ok {
				blocks = appendGeminiDecisionBlock(blocks, block, &separated)
			}
		}
	}
	if len(blocks) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("Gemini decisions require an input, and this request's messages carry no content")
	}
	return blocks, nil
}

// appendGeminiDecisionBlock appends block, first adding a newline text block
// when the message it belongs to is not yet separated from the previous one.
func appendGeminiDecisionBlock(blocks []GeminiDecisionContent, block GeminiDecisionContent, separated *bool) []GeminiDecisionContent {
	if !*separated {
		blocks = append(blocks, geminiDecisionTextBlock("\n"))
		*separated = true
	}
	return append(blocks, block)
}

// geminiDecisionTextBlock returns a text content block.
func geminiDecisionTextBlock(text string) GeminiDecisionContent {
	return GeminiDecisionContent{Type: "text", Text: &text}
}

// toGeminiDecisionBlock converts one message part to a content block. ok is
// false for a textual part with no text, which adds nothing.
func toGeminiDecisionBlock(part schemas.DecisionInputPart) (GeminiDecisionContent, bool, error) {
	var text string
	switch part.Type {
	case schemas.DecisionInputPartTypeText, schemas.DecisionInputPartTypeOutputText:
		text = providerUtils.DecisionTextString(part.Text)
	case schemas.DecisionInputPartTypeRefusal:
		if part.Refusal != nil {
			text = *part.Refusal
		}
	case schemas.DecisionInputPartTypeJSON:
		if part.Data.Structured != nil {
			encoded, err := providerUtils.MarshalSorted(part.Data.Structured)
			if err != nil {
				return GeminiDecisionContent{}, false, providerUtils.InvalidRequestErrorf("decision input JSON part could not be encoded as text: %v", err)
			}
			text = string(encoded)
		}
	case schemas.DecisionInputPartTypeImage:
		block, err := toGeminiDecisionImage(part)
		return block, err == nil, err
	case "":
		return GeminiDecisionContent{}, false, providerUtils.InvalidRequestErrorf("decision input carries a part without a type, which Gemini decisions cannot read")
	default:
		return GeminiDecisionContent{}, false, providerUtils.InvalidRequestErrorf("Gemini decisions read text, JSON, image, output text, and refusal parts, not %q", part.Type)
	}
	if text == "" {
		return GeminiDecisionContent{}, false, nil
	}
	return geminiDecisionTextBlock(text), true, nil
}

// toGeminiDecisionImage converts an input image to an image block: a base64
// data URL as inline data with its media type, and an HTTP(S) URL as a URI
// whose type is read from its file extension, since the API requires one and
// never sniffs it. An image detail maps onto the media resolution.
func toGeminiDecisionImage(part schemas.DecisionInputPart) (GeminiDecisionContent, error) {
	if part.ImageURL == nil || *part.ImageURL == "" {
		return GeminiDecisionContent{}, providerUtils.InvalidRequestErrorf("decision input carries an input_image part without an image_url")
	}
	url := *part.ImageURL
	block := GeminiDecisionContent{Type: "image"}
	if part.Detail != nil {
		block.Resolution = geminiDecisionResolutions[strings.ToLower(*part.Detail)]
	}
	if strings.HasPrefix(url, "data:") {
		mediaType, isBase64, payload, ok := schemas.ParseDataURL(url)
		if !ok || !isBase64 {
			return GeminiDecisionContent{}, providerUtils.InvalidRequestErrorf("decision input image data URL must carry a media type and base64 data")
		}
		block.Data = payload
		block.MimeType = mediaType
	} else {
		block.URI = url
		block.MimeType = mimeTypeFromURI(url)
		if block.MimeType == "" {
			return GeminiDecisionContent{}, providerUtils.InvalidRequestErrorf("Gemini decisions need an image's MIME type, which the URL %q does not state by its file extension; send the image as a base64 data URL", url)
		}
	}
	if !strings.HasPrefix(block.MimeType, "image/") {
		return GeminiDecisionContent{}, providerUtils.InvalidRequestErrorf("decision input image has media type %q, not an image type", block.MimeType)
	}
	return block, nil
}

// toGeminiDecisionQuestion converts one question. index is its position, for
// errors.
func toGeminiDecisionQuestion(index int, question schemas.DecisionQuestion) (GeminiDecisionQuestion, error) {
	nativeType, ok := geminiDecisionQuestionTypes[question.Type]
	if !ok {
		return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("question %d has type %q, which Gemini decisions do not serve; expected predicate, choice, or score", index, question.Type)
	}
	instructions, err := geminiDecisionInstructions(index, question.Instructions)
	if err != nil {
		return GeminiDecisionQuestion{}, err
	}
	native := GeminiDecisionQuestion{Type: nativeType, Instructions: instructions}

	switch question.Type {
	case schemas.DecisionTypePredicate:
		if question.Criteria == nil {
			break
		}
		var criteria GeminiDecisionNoulCriteria
		if text := providerUtils.DecisionTextString(question.Criteria.True); text != "" {
			criteria.TrueDescription = &text
		}
		if text := providerUtils.DecisionTextString(question.Criteria.False); text != "" {
			criteria.FalseDescription = &text
		}
		if criteria.TrueDescription != nil || criteria.FalseDescription != nil {
			native.Criteria = criteria
		}
	case schemas.DecisionTypeChoice:
		if len(question.Choices) == 0 {
			return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("choice question %d requires choices", index)
		}
		options := make(map[string]string, len(question.Choices))
		for j, choice := range question.Choices {
			option, err := choice.Key()
			if err != nil {
				return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("choice %d of question %d: %s", j, index, err.Error())
			}
			if _, seen := options[option]; seen {
				return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("question %d has more than one choice %q", index, option)
			}
			options[option] = providerUtils.DecisionTextString(choice.Description)
		}
		native.Criteria = options
	case schemas.DecisionTypeScore:
		if len(question.Levels) == 0 {
			return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("score question %d requires levels", index)
		}
		levels := make([]string, len(question.Levels))
		for j, level := range question.Levels {
			levels[j] = geminiDecisionLevel(j, level)
		}
		native.Criteria = levels
	}
	return native, nil
}

// geminiDecisionLevel renders a score level as the non-empty text the API
// requires: its description with any label folded in, or its index when it
// has neither.
func geminiDecisionLevel(index int, level schemas.DecisionLevel) string {
	var text string
	switch description := providerUtils.DecisionLevelDescription(index, level).(type) {
	case nil:
	case string:
		text = description
	default:
		if encoded, err := providerUtils.MarshalSorted(description); err == nil {
			text = string(encoded)
		}
	}
	if text == "" {
		return strconv.Itoa(index)
	}
	return text
}

// geminiDecisionInstructions renders a question's instructions as the text the
// API takes. Instructions written as input_text and input_json parts are
// joined in order; a list carrying an input_image part anywhere is rejected,
// since the API's instructions are text only. Any other value is rendered as
// text.
func geminiDecisionInstructions(index int, instructions *schemas.DecisionText) (string, error) {
	if instructions != nil {
		if parts, ok := instructions.Structured.([]interface{}); ok && len(parts) > 0 {
			text, isParts, err := geminiDecisionInstructionParts(index, parts)
			if err != nil || isParts {
				return text, err
			}
		}
	}
	return providerUtils.DecisionTextString(instructions), nil
}

// geminiDecisionInstructionParts joins instructions written as parts. Every
// element is checked for an image first, so one after an unrecognized element
// is still rejected. isParts is false when an element is not a text or JSON
// part, so the value is rendered as structured text instead.
func geminiDecisionInstructionParts(index int, parts []interface{}) (string, bool, error) {
	isParts := true
	for _, part := range parts {
		object, ok := part.(map[string]interface{})
		if !ok {
			isParts = false
			continue
		}
		switch object["type"] {
		case schemas.DecisionInputPartTypeImage:
			return "", true, providerUtils.InvalidRequestErrorf("question %d instructions carry an image; Gemini decision instructions are text only", index)
		case schemas.DecisionInputPartTypeText, schemas.DecisionInputPartTypeJSON:
		default:
			isParts = false
		}
	}
	if !isParts {
		return "", false, nil
	}
	var out strings.Builder
	for _, part := range parts {
		object := part.(map[string]interface{})
		value := object["data"]
		if object["type"] == schemas.DecisionInputPartTypeText {
			if text, ok := object["text"].(string); ok {
				out.WriteString(text)
				continue
			}
			value = object["text"]
		}
		if value == nil {
			continue
		}
		encoded, err := providerUtils.MarshalSorted(value)
		if err != nil {
			return "", true, providerUtils.InvalidRequestErrorf("question %d instructions could not be encoded as text: %v", index, err)
		}
		out.Write(encoded)
	}
	return out.String(), true, nil
}

// ToBifrostDecisionResponse converts the API's answers into the normalized
// shape, in question order, matching each question to the key it was sent
// under (see geminiDecisionQuestionKeys). Every question must be answered with
// its own type. A choice answer takes its choice's value, probabilities follow
// the question's choice or level order, and a score answer keeps the API's
// legend, or the question's levels when it sends none. Usage counts input
// tokens by modality; output tokens are always 0.
func (response *GeminiDecisionResponse) ToBifrostDecisionResponse(request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, error) {
	keys, err := geminiDecisionQuestionKeys(request.Questions)
	if err != nil {
		return nil, err
	}
	answers := make([]schemas.DecisionAnswer, len(request.Questions))
	for i, question := range request.Questions {
		key := keys[i]
		native, ok := response.Answers[key]
		if !ok {
			return nil, fmt.Errorf("Gemini returned no decision answer for question %q", key)
		}
		if expected := geminiDecisionQuestionTypes[question.Type]; native.Type != expected {
			return nil, fmt.Errorf("Gemini answered decision question %q as %q; expected %q", key, native.Type, expected)
		}
		answer := schemas.DecisionAnswer{
			Type:          question.Type,
			Name:          question.Name,
			Confidence:    native.Confidence,
			Probabilities: providerUtils.DecisionProbabilitiesFromKeys(question, native.Probabilities),
		}
		switch question.Type {
		case schemas.DecisionTypePredicate:
			if native.Noul == nil {
				return nil, fmt.Errorf("Gemini noul answer for question %q carries no value", key)
			}
			answer.Probability = native.Noul
		case schemas.DecisionTypeChoice:
			if native.Choice == nil {
				return nil, fmt.Errorf("Gemini choice answer for question %q carries no value", key)
			}
			choice := providerUtils.DecisionChoiceForKey(question, *native.Choice)
			answer.Choice = &choice
		case schemas.DecisionTypeScore:
			if native.Score == nil {
				return nil, fmt.Errorf("Gemini score answer for question %q carries no value", key)
			}
			answer.Score = native.Score
			answer.Legend = native.Legend
			if answer.Legend == nil {
				answer.Legend = providerUtils.DecisionScoreLegend(question)
			}
		}
		answers[i] = answer
	}
	return &schemas.BifrostDecisionResponse{
		Model:   request.Model,
		Answers: answers,
		Usage:   response.Usage.toBifrostLLMUsage(),
	}, nil
}

// toBifrostLLMUsage converts the usage, or returns nil. Input tokens are
// prompt tokens, with the text, image, and audio counts as their details.
func (usage *GeminiDecisionUsage) toBifrostLLMUsage() *schemas.BifrostLLMUsage {
	if usage == nil {
		return nil
	}
	converted := &schemas.BifrostLLMUsage{
		PromptTokens:     usage.TotalInputTokens,
		CompletionTokens: usage.TotalOutputTokens,
		TotalTokens:      usage.TotalTokens,
	}
	if converted.TotalTokens == 0 {
		converted.TotalTokens = usage.TotalInputTokens + usage.TotalOutputTokens
	}
	var details schemas.ChatPromptTokensDetails
	for _, modality := range usage.InputTokensByModality {
		switch strings.ToLower(modality.Modality) {
		case "text":
			details.TextTokens += modality.Tokens
		case "image":
			details.ImageTokens += modality.Tokens
		case "audio":
			details.AudioTokens += modality.Tokens
		}
	}
	if details.TextTokens != 0 || details.ImageTokens != 0 || details.AudioTokens != 0 {
		converted.PromptTokensDetails = &details
	}
	return converted
}

// parseGeminiDecisionError builds the error from a decisions error reply. A
// safety block carries a string code ("safety", "prohibited_content", ...)
// that Google's usual error shape, with its numeric code, cannot read; it keeps
// the API's message and code. Any other error is parsed as usual.
func parseGeminiDecisionError(resp *fasthttp.Response) *schemas.BifrostError {
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil || gjson.GetBytes(body, "error.code").Type != gjson.String {
		return parseGeminiError(resp)
	}
	var errorResp GeminiDecisionError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = errorResp.Error.Message
		if errorResp.Error.Code != "" {
			bifrostErr.Error.Code = schemas.Ptr(errorResp.Error.Code)
		}
	}
	return bifrostErr
}

// Decision answers a decision request on the Gemini Developer API's
// POST /v1beta/decisions (Gemini Decision 1 models).
func (provider *GeminiProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.DecisionRequest); err != nil {
		return nil, err
	}
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*GeminiDecisionRequest, error) {
			return ToGeminiDecisionRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, geminiDecisionsPath))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	setGeminiAuthHeader(req, key.Value.GetValue())
	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBody(ctx, req)
	if !usedLargePayloadBody {
		req.SetBody(jsonData)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: status %d", provider.GetProviderKey(), resp.StatusCode()))
		return nil, providerUtils.EnrichError(ctx, parseGeminiDecisionError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var decisionResp GeminiDecisionResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &decisionResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp, err := decisionResp.ToBifrostDecisionResponse(request)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(err.Error(), nil), jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResp, nil
}
