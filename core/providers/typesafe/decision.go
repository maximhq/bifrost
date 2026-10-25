package typesafe

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// typesafeMaxChoiceOptions is Typesafe's documented cap on choice options.
const typesafeMaxChoiceOptions = 255

// typesafeQuestionTypes maps the normalized question types to the native
// ones Typesafe serves; its "noul" is the normalized predicate.
var typesafeQuestionTypes = map[schemas.DecisionType]string{
	schemas.DecisionTypePredicate: TypesafeQuestionTypeNoul,
	schemas.DecisionTypeChoice:    TypesafeQuestionTypeChoice,
	schemas.DecisionTypeScore:     TypesafeQuestionTypeScore,
	schemas.DecisionTypeBox:       TypesafeQuestionTypeBox,
}

// validStructuredValue reports whether a state or instructions value
// serializes to one of the documented JSON shapes: string, object, or array.
// Marshaling the value answers that exactly: pointers dereference, typed
// maps/slices/structs normalize, custom marshalers speak for themselves, and
// unmarshalable values (func, chan) fail closed with a local 400 instead of a
// request-marshal 500 later. Only the value is encoded for inspection; the
// request body is serialized once, unchanged, on the send path.
func validStructuredValue(value interface{}) bool {
	data, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return false
	}
	for _, c := range data {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c == '"' || c == '{' || c == '['
	}
	return false
}

// ToTypesafeDecisionRequest converts a normalized decision request into
// Typesafe's native systemone shape: the input becomes the state, and each
// question is keyed by its name (see schemas.DecisionQuestionNames). Choices
// become an option -> description map, a boolean choice keyed "true" or
// "false", and levels an ordered array of descriptions with their labels
// folded in. Image parts with an ID become the request's assets (see
// typesafeAssets), for each endpoint that receives this shape to accept or
// reject. Unsupported types and malformed
// criteria are rejected rather than silently approximated, and so is an inline
// image in the input, which a systemone endpoint would read as text. Validation
// is never stricter than the official SDKs' types: null state, null or absent
// instructions, and null descriptions are forwarded for the endpoint to judge.
func ToTypesafeDecisionRequest(request *schemas.BifrostDecisionRequest) (*TypesafeDecisionRequest, error) {
	if len(request.Questions) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("decision request requires at least one question")
	}
	input, assets, err := typesafeAssets(request.Input)
	if err != nil {
		return nil, err
	}
	if partType := input.NonTextPartType(); partType != "" {
		return nil, providerUtils.InvalidRequestErrorf("decision input carries an %q part, which this provider cannot read; route the request to a provider that accepts it", partType)
	}
	state := typesafeState(input)
	if !isJSONNull(state) && !validStructuredValue(state) {
		return nil, providerUtils.InvalidRequestErrorf("state must be a string, object, or array")
	}
	names, err := schemas.DecisionQuestionNames(request.Questions)
	if err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err.Error())
	}

	questions := make(map[string]TypesafeQuestion, len(request.Questions))
	for i, question := range request.Questions {
		native, err := toTypesafeQuestion(names[i], question)
		if err != nil {
			return nil, err
		}
		questions[names[i]] = *native
	}

	return &TypesafeDecisionRequest{
		State:       state,
		Model:       request.Model,
		Questions:   questions,
		Assets:      assets,
		ExtraParams: request.ExtraParams,
	}, nil
}

// typesafeAssets moves the input's asset parts into Typesafe's assets map, keyed
// by their IDs. A single message left with a single text part, the shape the
// systemone route builds from a state, gives that text back as the state; other
// messages stay messages, without their asset parts.
func typesafeAssets(input schemas.DecisionInput) (schemas.DecisionInput, map[string]TypesafeAsset, error) {
	if !input.HasAssetParts() {
		return input, nil, nil
	}
	assets := make(map[string]TypesafeAsset)
	messages := make([]schemas.DecisionInputMessage, 0, len(input.Messages))
	for _, message := range input.Messages {
		if message.Content.Parts == nil {
			messages = append(messages, message)
			continue
		}
		parts := make([]schemas.DecisionInputPart, 0, len(message.Content.Parts))
		for _, part := range message.Content.Parts {
			if !part.IsAsset() {
				parts = append(parts, part)
				continue
			}
			if _, seen := assets[*part.ID]; seen {
				return schemas.DecisionInput{}, nil, providerUtils.InvalidRequestErrorf("asset id %q is used by more than one input_image part", *part.ID)
			}
			asset, err := typesafeAsset(part)
			if err != nil {
				return schemas.DecisionInput{}, nil, err
			}
			assets[*part.ID] = asset
		}
		if len(parts) > 0 {
			message.Content.Parts = parts
			messages = append(messages, message)
		}
	}
	switch {
	case len(messages) == 0:
		return schemas.DecisionInput{}, assets, nil
	case len(messages) == 1 && len(messages[0].Content.Parts) == 1 && messages[0].Content.Parts[0].Type == schemas.DecisionInputPartTypeText:
		return ToBifrostDecisionInput(messages[0].Content.Parts[0].Text.Value()), assets, nil
	}
	return schemas.DecisionInput{Messages: messages}, assets, nil
}

// typesafeAsset converts an asset part. One that arrived as a multipart file part
// is sent as that part again; any other is sent as data_base64, with the format
// read from its bytes.
func typesafeAsset(part schemas.DecisionInputPart) (TypesafeAsset, error) {
	if part.Data == nil {
		return TypesafeAsset{}, providerUtils.InvalidRequestErrorf("asset %q has no data; send the image file base64-encoded in data, or as a multipart file part its name points at", *part.ID)
	}
	if part.Name != nil {
		return TypesafeAsset{Type: "image", Part: *part.Name}, nil
	}
	format := strings.TrimPrefix(typesafeImageType(part.Data), "image/")
	return TypesafeAsset{Type: "image", Format: format, DataBase64: base64.StdEncoding.EncodeToString(part.Data)}, nil
}

// typesafeImageType reads an image file's media type from its bytes, as Typesafe
// itself does: image/png or image/jpeg for the images it accepts. Any other type
// is sent on for Typesafe to reject.
func typesafeImageType(data []byte) string {
	mediaType, _, _ := strings.Cut(http.DetectContentType(data), ";")
	return mediaType
}

// usesTypesafeImages reports whether the request needs typesafeImagesHeader: it
// carries assets or asks a box question.
func usesTypesafeImages(request *schemas.BifrostDecisionRequest) bool {
	if request.Input.HasAssetParts() {
		return true
	}
	for _, question := range request.Questions {
		if question.Type == schemas.DecisionTypeBox {
			return true
		}
	}
	return false
}

// typesafeState renders the input as a Typesafe state: text as a string, a
// structured input unchanged, messages as an array of {role, content}
// objects, and an empty input as null.
func typesafeState(input schemas.DecisionInput) interface{} {
	switch {
	case input.Text != nil:
		return *input.Text
	case input.Messages != nil:
		return input.Messages
	}
	return input.Structured
}

// toTypesafeQuestion converts one normalized question to the native shape and
// validates it.
func toTypesafeQuestion(name string, question schemas.DecisionQuestion) (*TypesafeQuestion, error) {
	nativeType, ok := typesafeQuestionTypes[question.Type]
	if !ok {
		return nil, providerUtils.InvalidRequestErrorf("question %q has unsupported type %q; expected predicate, choice, score, or box", name, question.Type)
	}
	// Instructions are optional in the SDK types (noul() defaults them to
	// null); whether criteria alone suffice is the endpoint's rule, not ours.
	native := TypesafeQuestion{Type: nativeType}
	if instructions := question.Instructions.Value(); !isJSONNull(instructions) {
		if !validStructuredValue(instructions) {
			return nil, providerUtils.InvalidRequestErrorf("question %q instructions must be a string, object, or array", name)
		}
		native.Instructions = instructions
	}

	switch question.Type {
	case schemas.DecisionTypePredicate:
		var outcomes interface{}
		if question.Criteria != nil {
			sides := make(map[string]any, 2)
			if question.Criteria.True != nil {
				sides["true"] = question.Criteria.True.Value()
			}
			if question.Criteria.False != nil {
				sides["false"] = question.Criteria.False.Value()
			}
			outcomes = sides
		}
		criteria, err := noulCriteria(name, outcomes)
		if err != nil {
			return nil, err
		}
		native.Criteria = criteria
	case schemas.DecisionTypeChoice:
		var options interface{}
		if question.Choices != nil {
			descriptions := make(map[string]any, len(question.Choices))
			for i, choice := range question.Choices {
				option, err := choice.Key()
				if err != nil {
					return nil, providerUtils.InvalidRequestErrorf("question %q choice %d: %s", name, i, err.Error())
				}
				if _, seen := descriptions[option]; seen {
					return nil, providerUtils.InvalidRequestErrorf("question %q has more than one choice %q", name, option)
				}
				descriptions[option] = choice.Description.Value()
			}
			options = descriptions
		}
		criteria, err := choiceCriteria(name, options)
		if err != nil {
			return nil, err
		}
		native.Criteria = criteria
	case schemas.DecisionTypeScore:
		var levels interface{}
		if question.Levels != nil {
			descriptions := make([]any, len(question.Levels))
			for i, level := range question.Levels {
				descriptions[i] = providerUtils.DecisionLevelDescription(i, level)
			}
			levels = descriptions
		}
		criteria, err := scoreCriteria(name, levels)
		if err != nil {
			return nil, err
		}
		native.Criteria = criteria
	}

	return &native, nil
}

// noulCriteria validates optional noul criteria: a map whose only keys are
// "true" and "false", each described by a string, object, or array, or null
// when a side needs no description. Passed through losslessly.
func noulCriteria(name string, criteria interface{}) (interface{}, error) {
	if isJSONNull(criteria) {
		return nil, nil
	}
	m, err := criteriaAsMap(name, criteria, true)
	if err != nil {
		return nil, err
	}
	for key := range m {
		if key != "true" && key != "false" {
			return nil, providerUtils.InvalidRequestErrorf("question %q noul criteria allows only \"true\" and \"false\" keys, got %q", name, key)
		}
	}
	return m, nil
}

// choiceCriteria validates the required option -> description map. A
// description is a string, object, or array, or null when an option needs no
// extra detail.
func choiceCriteria(name string, criteria interface{}) (map[string]any, error) {
	if criteria == nil {
		return nil, providerUtils.InvalidRequestErrorf("question %q has kind choice and requires criteria options", name)
	}
	m, err := criteriaAsMap(name, criteria, true)
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("question %q has kind choice and requires criteria options", name)
	}
	if len(m) > typesafeMaxChoiceOptions {
		return nil, providerUtils.InvalidRequestErrorf("question %q has %d choice options; Typesafe allows at most %d", name, len(m), typesafeMaxChoiceOptions)
	}
	return m, nil
}

// scoreCriteria validates the ordered array of level descriptions, each a
// string, object, or array, or null for an undescribed level, preserved
// losslessly. Go SDK callers supply []string; HTTP JSON decoding supplies
// []interface{} - both are the same ordered array on the wire. The number of
// levels is the endpoint's rule, not ours: Typesafe accepts 2-10, Perplexity
// 1-10, and each rejects the rest with its own error.
func scoreCriteria(name string, criteria interface{}) (interface{}, error) {
	var levels []any
	switch typed := criteria.(type) {
	case []any:
		levels = typed
	case []string:
		levels = make([]any, len(typed))
		for i, level := range typed {
			levels[i] = level
		}
	default:
		// Go SDK callers pass typed slices ([]Rubric, [][]string, named slice
		// types). Any slice or array is an ordered list of level descriptions;
		// elements are validated by serialized shape below, exactly like the
		// untyped forms, and carried losslessly.
		value := reflect.ValueOf(criteria)
		if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) {
			return nil, providerUtils.InvalidRequestErrorf("question %q has kind score and requires criteria as an ordered array of level descriptions", name)
		}
		levels = make([]any, value.Len())
		for i := range levels {
			levels[i] = value.Index(i).Interface()
		}
	}
	for i, level := range levels {
		if !isJSONNull(level) && !validStructuredValue(level) {
			return nil, providerUtils.InvalidRequestErrorf("question %q score criteria level %d must be a string, object, or array", name, i)
		}
	}
	return levels, nil
}

// criteriaAsMap normalizes a criteria value into map[string]any, rejecting
// non-map shapes. Each description must serialize to a string, object, or
// array; null is additionally allowed when allowNull is set (a label that
// needs no extra detail). Descriptions are carried losslessly.
func criteriaAsMap(name string, criteria interface{}, allowNull bool) (map[string]any, error) {
	var m map[string]any
	switch typed := criteria.(type) {
	case map[string]string:
		m = make(map[string]any, len(typed))
		for key, value := range typed {
			m[key] = value
		}
	case map[string]any:
		m = typed
	default:
		// Go SDK callers pass typed maps (map[string]Rubric, map[string][]string,
		// ...). Any map with string keys is a map of named descriptions; its
		// values are validated by serialized shape below, exactly like the
		// untyped forms, and carried losslessly.
		value := reflect.ValueOf(criteria)
		if !value.IsValid() || value.Kind() != reflect.Map || value.Type().Key().Kind() != reflect.String {
			return nil, providerUtils.InvalidRequestErrorf("question %q criteria must be a map of descriptions", name)
		}
		m = make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			m[iter.Key().String()] = iter.Value().Interface()
		}
	}
	for key, value := range m {
		if isJSONNull(value) {
			if allowNull {
				continue
			}
			return nil, providerUtils.InvalidRequestErrorf("question %q criteria description for %q must be a string, object, or array", name, key)
		}
		if !validStructuredValue(value) {
			return nil, providerUtils.InvalidRequestErrorf("question %q criteria description for %q must be a string, object, or array", name, key)
		}
	}
	return m, nil
}

// isJSONNull reports whether a description serializes to JSON null. Typed nil
// pointers from Go SDK callers (map[string]*Rubric{"other": nil}) are non-nil
// interfaces wrapping nil pointers - value == nil misses them, but on the wire
// they are null exactly like an untyped nil, so both follow the null rules.
func isJSONNull(value any) bool {
	if value == nil {
		return true
	}
	data, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return false
	}
	return gjson.ParseBytes(data).Type == gjson.Null
}

// ToBifrostDecisionResponse converts a native systemone response back into
// the normalized shape, matching each answer to its question by the name the
// request was sent under and keeping the questions' order. Every requested
// question must produce an answer of its declared type. A boolean choice is
// answered with its boolean, and probabilities follow the question's choice or
// level order.
func ToBifrostDecisionResponse(resp *TypesafeDecisionResponse, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	names, err := schemas.DecisionQuestionNames(request.Questions)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(err.Error(), nil)
	}
	answers := make([]schemas.DecisionAnswer, 0, len(request.Questions))
	for i, question := range request.Questions {
		name := names[i]
		native, ok := resp.Answers[name]
		if !ok {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe returned no answer for question %q", name), nil)
		}
		if expected := typesafeQuestionTypes[question.Type]; native.Type != expected {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe answered question %q as %q; expected %q", name, native.Type, expected), nil)
		}

		answer := schemas.DecisionAnswer{
			Type:                question.Type,
			Name:                question.Name,
			Confidence:          native.Confidence,
			Probabilities:       providerUtils.DecisionProbabilitiesFromKeys(question, native.Probabilities),
			Legend:              native.Legend,
			AnswerConfidence:    native.AnswerConfidence,
			Action:              native.Action,
			Abstention:          native.Abstention,
			AbstentionThreshold: native.AbstentionThreshold,
			LowConfidence:       native.LowConfidence,
		}
		switch question.Type {
		case schemas.DecisionTypePredicate:
			if native.Noul == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe noul answer for question %q carries no value", name), nil)
			}
			if *native.Noul < 0 || *native.Noul > 1 {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe noul answer for question %q is outside [0,1]: %v", name, *native.Noul), nil)
			}
			answer.Probability = native.Noul
		case schemas.DecisionTypeChoice:
			if native.Choice == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe choice answer for question %q carries no value", name), nil)
			}
			choice := providerUtils.DecisionChoiceForKey(question, *native.Choice)
			answer.Choice = &choice
		case schemas.DecisionTypeScore:
			if native.Score == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe score answer for question %q carries no value", name), nil)
			}
			answer.Score = native.Score
		case schemas.DecisionTypeBox:
			if native.Box == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe box answer for question %q carries no box", name), nil)
			}
			answer.Box = &schemas.DecisionBox{Top: native.Box.Top, Bottom: native.Box.Bottom, Left: native.Box.Left, Right: native.Box.Right}
		}
		answers = append(answers, answer)
	}

	response := &schemas.BifrostDecisionResponse{
		Model:   resp.Model,
		Answers: answers,
		Routing: resp.Routing,
	}
	if resp.Usage != nil {
		response.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:       resp.Usage.InputTokens,
			CompletionTokens:   resp.Usage.OutputTokens,
			TotalTokens:        resp.Usage.InputTokens + resp.Usage.OutputTokens,
			StateTokens:        resp.Usage.StateTokens,
			StateTokensDropped: resp.Usage.StateTokensDropped,
			Truncated:          resp.Usage.Truncated,
			TruncatedQuestions: resp.Usage.TruncatedQuestions,
		}
	}
	return response, nil
}

// unwrapResultEnvelope returns the systemone body inside a Cloudflare Workers AI
// REST envelope ({"result": {...}, "success": true, ...}), which serves
// Jev-compatible models such as Clef. failed is true when the envelope declares
// success:false, even on HTTP 200, so the caller can surface errors[] instead of
// parsing an empty or partial result. A body that already carries "answers" is
// returned as is and never failed, so plain systemone endpoints are unaffected.
func unwrapResultEnvelope(body []byte) (inner []byte, failed bool) {
	if gjson.GetBytes(body, "answers").Exists() {
		return body, false
	}
	if gjson.GetBytes(body, "success").Type == gjson.False {
		return body, true
	}
	if result := gjson.GetBytes(body, "result"); result.IsObject() {
		return []byte(result.Raw), false
	}
	return body, false
}

// ToBifrostDecisionRequest converts a native systemone request into the
// normalized shape: the state becomes the input and the questions are
// normalized by ToBifrostDecisionQuestions. An explicit null state is
// SDK-valid and forwarded; only an absent state is rejected locally. A model
// without a provider prefix is left for routing to resolve, since the route
// fixes the wire shape, not the provider.
func (req *TypesafeDecisionRequest) ToBifrostDecisionRequest(ctx *schemas.BifrostContext) (*schemas.BifrostDecisionRequest, error) {
	if req == nil {
		return nil, providerUtils.InvalidRequestErrorf("request body is required")
	}
	if req.State == nil && !req.stateSet {
		return nil, providerUtils.InvalidRequestErrorf("state is required")
	}
	if len(req.Questions) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("at least one question is required")
	}
	questions, err := ToBifrostDecisionQuestions(req.Questions)
	if err != nil {
		return nil, err
	}

	input := ToBifrostDecisionInput(req.State)
	if len(req.Assets) > 0 {
		if input, err = req.toBifrostDecisionInputWithAssets(); err != nil {
			return nil, err
		}
	}

	provider, model := schemas.ParseModelString(req.Model, "")
	return &schemas.BifrostDecisionRequest{
		Provider:    provider,
		Model:       model,
		Input:       input,
		Questions:   questions,
		ExtraParams: req.ExtraParams,
	}, nil
}

// toBifrostDecisionInputWithAssets normalizes a state sent with assets: one user
// message whose text part is the state as is, followed by an input_image part per
// asset, sorted by ID, holding the asset's bytes; a multipart asset also keeps its
// part name. The format is read from the bytes again when the request is sent.
// Only what the normalized shape cannot carry is rejected here: an asset of another
// type, base64 that does not decode, and a part named without a multipart request.
func (req *TypesafeDecisionRequest) toBifrostDecisionInputWithAssets() (schemas.DecisionInput, error) {
	refs := make([]string, 0, len(req.Assets))
	for ref := range req.Assets {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	state := &schemas.DecisionText{Structured: req.State}
	if text, ok := req.State.(string); ok {
		state = schemas.NewDecisionText(text)
	}
	parts := make([]schemas.DecisionInputPart, 0, len(refs)+1)
	parts = append(parts, schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeText, Text: state})
	for _, ref := range refs {
		asset := req.Assets[ref]
		if asset.Type != "" && asset.Type != "image" {
			return schemas.DecisionInput{}, providerUtils.InvalidRequestErrorf("asset %q has type %q; assets are images", ref, asset.Type)
		}
		part := schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr(ref)}
		if asset.Part != "" {
			if asset.Data == nil {
				return schemas.DecisionInput{}, providerUtils.InvalidRequestErrorf("asset %q names part %q, which needs a multipart request", ref, asset.Part)
			}
			part.Name = schemas.Ptr(asset.Part)
			part.Data = asset.Data
		} else {
			data, err := base64.StdEncoding.DecodeString(asset.DataBase64)
			if err != nil {
				return schemas.DecisionInput{}, providerUtils.InvalidRequestErrorf("asset %q data_base64 is not valid base64", ref)
			}
			part.Data = data
		}
		parts = append(parts, part)
	}
	return schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{
		Role:    string(schemas.ChatMessageRoleUser),
		Content: schemas.DecisionInputContent{Parts: parts},
	}}}, nil
}

// ToBifrostDecisionInput normalizes a Typesafe state: a string becomes the
// input text, null an empty input, and anything else a structured input that
// map-shaped providers receive unchanged.
func ToBifrostDecisionInput(state interface{}) schemas.DecisionInput {
	if state == nil {
		return schemas.DecisionInput{}
	}
	if text, ok := state.(string); ok {
		return schemas.DecisionInput{Text: &text}
	}
	return schemas.DecisionInput{Structured: state}
}

// ToBifrostDecisionQuestions normalizes Typesafe's named questions into a list
// sorted by name, each keeping its name: noul becomes predicate and keeps its
// criteria, choice options become choices sorted by option, and score levels
// become levels labelled by their index. Only a structural problem is
// rejected here (an unknown type, or criteria of the wrong shape); the
// provider that serves the request validates the rest.
func ToBifrostDecisionQuestions(questions map[string]TypesafeQuestion) ([]schemas.DecisionQuestion, error) {
	names := make([]string, 0, len(questions))
	for name := range questions {
		names = append(names, name)
	}
	sort.Strings(names)

	normalized := make([]schemas.DecisionQuestion, 0, len(names))
	for _, name := range names {
		question := questions[name]
		instructions, err := toDecisionText(question.Instructions, "question %q instructions must be a string, object, or array", name)
		if err != nil {
			return nil, err
		}
		converted := schemas.DecisionQuestion{Name: schemas.Ptr(name), Instructions: instructions}
		switch question.Type {
		case TypesafeQuestionTypeNoul:
			converted.Type = schemas.DecisionTypePredicate
			criteria, err := toDecisionCriteria(name, question.Criteria)
			if err != nil {
				return nil, err
			}
			converted.Criteria = criteria
		case TypesafeQuestionTypeChoice:
			converted.Type = schemas.DecisionTypeChoice
			if question.Criteria != nil {
				options, ok := criteriaEntries(question.Criteria)
				if !ok {
					return nil, providerUtils.InvalidRequestErrorf("question %q criteria must be a map of descriptions", name)
				}
				keys := make([]string, 0, len(options))
				for option := range options {
					keys = append(keys, option)
				}
				sort.Strings(keys)
				converted.Choices = make([]schemas.DecisionChoice, 0, len(keys))
				for _, option := range keys {
					description, err := toDecisionText(options[option], "question %q criteria description for %q must be a string, object, or array", name, option)
					if err != nil {
						return nil, err
					}
					converted.Choices = append(converted.Choices, schemas.DecisionChoice{
						Value:       schemas.DecisionScalar{Str: schemas.Ptr(option)},
						Description: description,
					})
				}
			}
		case TypesafeQuestionTypeScore:
			converted.Type = schemas.DecisionTypeScore
			if question.Criteria != nil {
				levels, ok := criteriaLevels(question.Criteria)
				if !ok {
					return nil, providerUtils.InvalidRequestErrorf("question %q has kind score and requires criteria as an ordered array of level descriptions", name)
				}
				converted.Levels = make([]schemas.DecisionLevel, len(levels))
				for i, level := range levels {
					description, err := toDecisionText(level, "question %q score criteria level %d must be a string, object, or array", name, i)
					if err != nil {
						return nil, err
					}
					converted.Levels[i] = schemas.DecisionLevel{Label: strconv.Itoa(i), Description: description}
				}
			}
		case TypesafeQuestionTypeBox:
			// Box takes no criteria; Typesafe ignores any sent, so they are dropped.
			converted.Type = schemas.DecisionTypeBox
		default:
			return nil, providerUtils.InvalidRequestErrorf("question %q has unsupported kind %q; expected noul, choice, score, or box", name, question.Type)
		}
		normalized = append(normalized, converted)
	}
	return normalized, nil
}

// toDecisionText reads a native instructions or description value: null as
// none, a string as text, and an object or array as a structured value. Any
// other value is rejected with the given message.
func toDecisionText(value interface{}, format string, args ...any) (*schemas.DecisionText, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		return schemas.NewDecisionText(text), nil
	}
	if !validStructuredValue(value) {
		return nil, providerUtils.InvalidRequestErrorf(format, args...)
	}
	return &schemas.DecisionText{Structured: value}, nil
}

// toDecisionCriteria reads native noul criteria: an optional map whose only
// keys are "true" and "false", each a description or null.
func toDecisionCriteria(name string, criteria interface{}) (*schemas.DecisionCriteria, error) {
	if isJSONNull(criteria) {
		return nil, nil
	}
	sides, ok := criteriaEntries(criteria)
	if !ok {
		return nil, providerUtils.InvalidRequestErrorf("question %q criteria must be a map of descriptions", name)
	}
	converted := &schemas.DecisionCriteria{}
	for key := range sides {
		if key != "true" && key != "false" {
			return nil, providerUtils.InvalidRequestErrorf("question %q noul criteria allows only \"true\" and \"false\" keys, got %q", name, key)
		}
	}
	for key, value := range sides {
		description, err := toDecisionText(value, "question %q criteria description for %q must be a string, object, or array", name, key)
		if err != nil {
			return nil, err
		}
		// An explicit null side is SDK-valid and forwarded as null, so it is
		// kept as an empty description rather than dropped.
		if description == nil {
			description = &schemas.DecisionText{}
		}
		if key == "true" {
			converted.True = description
		} else {
			converted.False = description
		}
	}
	return converted, nil
}

// criteriaEntries reads choice criteria as option -> description. JSON
// decoding gives map[string]any; any other map keyed by strings is read
// through reflection, its values carried unchanged.
func criteriaEntries(criteria interface{}) (map[string]any, bool) {
	if typed, ok := criteria.(map[string]any); ok {
		return typed, true
	}
	value := reflect.ValueOf(criteria)
	if value.Kind() != reflect.Map || value.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	entries := make(map[string]any, value.Len())
	iter := value.MapRange()
	for iter.Next() {
		entries[iter.Key().String()] = iter.Value().Interface()
	}
	return entries, true
}

// criteriaLevels reads score criteria as the ordered level descriptions. JSON
// decoding gives []any; any other slice or array is read through reflection,
// its elements carried unchanged.
func criteriaLevels(criteria interface{}) ([]any, bool) {
	if typed, ok := criteria.([]any); ok {
		return typed, true
	}
	value := reflect.ValueOf(criteria)
	if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return nil, false
	}
	levels := make([]any, value.Len())
	for i := range levels {
		levels[i] = value.Index(i).Interface()
	}
	return levels, true
}

// ToTypesafeNativeDecisionResponse converts a normalized decision response
// back into Typesafe's native model, answers, and usage shape.
func ToTypesafeNativeDecisionResponse(resp *schemas.BifrostDecisionResponse) (*TypesafeDecisionResponse, error) {
	if resp == nil {
		return nil, fmt.Errorf("decision response is nil")
	}
	native := &TypesafeDecisionResponse{
		Model:   resp.Model,
		Answers: ToTypesafeAnswers(resp.Answers),
		Routing: resp.Routing,
	}
	if resp.Usage != nil {
		native.Usage = &TypesafeUsage{
			InputTokens:        resp.Usage.PromptTokens,
			OutputTokens:       resp.Usage.CompletionTokens,
			StateTokens:        resp.Usage.StateTokens,
			StateTokensDropped: resp.Usage.StateTokensDropped,
			Truncated:          resp.Usage.Truncated,
			TruncatedQuestions: resp.Usage.TruncatedQuestions,
		}
	}
	return native, nil
}

// ToTypesafeAnswers keys normalized answers by name (see
// schemas.DecisionAnswerNames) in Typesafe's native answer shape: predicate as
// noul, a choice as its option (a boolean as "true" or "false"), and
// probabilities keyed by option or level index. A refusal keeps its type with
// no value, so it is not dropped nor given one.
func ToTypesafeAnswers(answers []schemas.DecisionAnswer) map[string]TypesafeAnswer {
	names := schemas.DecisionAnswerNames(answers)
	native := make(map[string]TypesafeAnswer, len(answers))
	for i, answer := range answers {
		converted := TypesafeAnswer{
			Confidence:          answer.Confidence,
			Probabilities:       providerUtils.DecisionKeyedProbabilities(answer.Probabilities),
			Legend:              answer.Legend,
			AnswerConfidence:    answer.AnswerConfidence,
			Action:              answer.Action,
			Abstention:          answer.Abstention,
			AbstentionThreshold: answer.AbstentionThreshold,
			LowConfidence:       answer.LowConfidence,
		}
		switch answer.Type {
		case schemas.DecisionTypePredicate:
			converted.Type = TypesafeQuestionTypeNoul
			converted.Noul = answer.Probability
		case schemas.DecisionTypeChoice:
			converted.Type = TypesafeQuestionTypeChoice
			if answer.Choice != nil {
				if option, ok := answer.Choice.Key(); ok {
					converted.Choice = &option
				}
			}
		case schemas.DecisionTypeScore:
			converted.Type = TypesafeQuestionTypeScore
			converted.Score = answer.Score
		case schemas.DecisionTypeBox:
			converted.Type = TypesafeQuestionTypeBox
			if answer.Box != nil {
				converted.Box = &TypesafeBox{Top: answer.Box.Top, Bottom: answer.Box.Bottom, Left: answer.Box.Left, Right: answer.Box.Right}
			}
		default:
			converted.Type = string(answer.Type)
		}
		native[names[i]] = converted
	}
	return native
}

// HandleTypesafeDecisionRequest sends a decision to a systemone endpoint, one whose
// request and response are Typesafe's (Typesafe, Azure's Microsoft-Decision-1,
// Perplexity's deciders), and converts the reply. toRequest builds the body, so a
// provider can adapt the input (Perplexity keeps images); the caller resolves the
// URL and auth headers, and parseError reads the provider's error body. A reply in a
// {result, success} envelope is unwrapped. keepNativeResponse copies the reply into
// NativeResponse for the native /typesafe route, which relays it only for Typesafe.
func HandleTypesafeDecisionRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostDecisionRequest,
	toRequest func(*schemas.BifrostDecisionRequest) (*TypesafeDecisionRequest, error),
	authHeaders map[string]string,
	extraHeaders map[string]schemas.SecretVar,
	providerName schemas.ModelProvider,
	parseError func(*fasthttp.Response) *schemas.BifrostError,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	keepNativeResponse bool,
) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return toRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for k, v := range authHeaders {
		req.Header.Set(k, v)
	}
	// A nil body means large-payload passthrough staged the request as a
	// stream; apply it instead of sending an empty body. Assets that arrived as
	// multipart file parts are sent the same way, with the JSON as "request".
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, providerName) {
		if files := typesafeFileParts(request.Input); len(files) == 0 {
			req.SetBody(jsonData)
		} else {
			body, contentType, err := typesafeMultipartBody(jsonData, files)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
			}
			req.SetBody(body)
			req.Header.SetContentType(contentType)
		}
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Captured before the status check so error responses forward them too
	// (the SDKs read request ids and Retry-After off every reply).
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	ft, fh := providerUtils.StartPhaseSpan(ctx, "response-finalize")
	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if ft != nil {
		if err != nil {
			ft.EndSpan(fh, schemas.SpanStatusError, err.Error())
		} else {
			ft.EndSpan(fh, schemas.SpanStatusOk, "")
		}
	}
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}
	respBody, envelopeFailed := unwrapResultEnvelope(respBody)
	if envelopeFailed {
		return nil, providerUtils.EnrichError(ctx, parseTypesafeEnvelopeFailure(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var decisionResp TypesafeDecisionResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &decisionResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp, bifrostErr := ToBifrostDecisionResponse(&decisionResp, request)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	if keepNativeResponse {
		// Compacting copies the body, which is released with resp.
		var compact bytes.Buffer
		if err := json.Compact(&compact, respBody); err == nil {
			bifrostResp.NativeResponse = json.RawMessage(compact.Bytes())
		}
	}
	return bifrostResp, nil
}

// typesafeFilePart is a multipart file part to send: an asset that arrived as one.
type typesafeFilePart struct {
	name        string
	contentType string
	data        []byte
}

// typesafeFileParts returns the asset parts that arrived as multipart file parts,
// each with its bytes and the content type read from them.
func typesafeFileParts(input schemas.DecisionInput) []typesafeFilePart {
	var files []typesafeFilePart
	for _, message := range input.Messages {
		for _, part := range message.Content.Parts {
			if part.IsAsset() && part.Name != nil {
				files = append(files, typesafeFilePart{name: *part.Name, contentType: typesafeImageType(part.Data), data: part.Data})
			}
		}
	}
	return files
}

// typesafeMultipartBody writes the JSON body as the first part, "request", then
// each file part under its name and content type, and returns the body with its
// multipart content type.
func typesafeMultipartBody(jsonBody []byte, files []typesafeFilePart) ([]byte, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="request"`)
	header.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(jsonBody); err != nil {
		return nil, "", err
	}
	for _, file := range files {
		// MIME quoting, not Go's, so any name the client sent parses back unchanged.
		disposition := mime.FormatMediaType("form-data", map[string]string{"name": file.name, "filename": file.name})
		if disposition == "" {
			return nil, "", fmt.Errorf("multipart part name %q cannot be encoded", file.name)
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", disposition)
		header.Set("Content-Type", file.contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(file.data); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}
