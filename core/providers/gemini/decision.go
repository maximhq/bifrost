package gemini

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// geminiDecisionsPath is where the Gemini API serves Gemini Decision models,
// under the provider's /v1beta base URL. Decisions are a sibling of the
// Interactions API: they share its content blocks and usage, not its endpoint.
const geminiDecisionsPath = "/decisions"

// geminiDecisionQuestionTypes maps each normalized question type Gemini serves
// to its own name. A box question has no Gemini counterpart.
var geminiDecisionQuestionTypes = map[schemas.DecisionType]string{
	schemas.DecisionTypePredicate: GeminiDecisionQuestionTypeNoul,
	schemas.DecisionTypeChoice:    GeminiDecisionQuestionTypeChoice,
	schemas.DecisionTypeScore:     GeminiDecisionQuestionTypeScore,
}

// geminiDecisionMaxKeyLength is the longest question key Gemini takes.
const geminiDecisionMaxKeyLength = 64

// ToGeminiDecisionRequest converts a normalized decision request into the body
// of POST /v1beta/decisions. Each question is sent under the key
// geminiDecisionQuestionKeys gives its name, by which its answer is matched
// back. The request is not modified.
func ToGeminiDecisionRequest(request *schemas.BifrostDecisionRequest) (*GeminiDecisionRequest, error) {
	if len(request.Questions) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("decision request requires at least one question")
	}
	input, err := toGeminiDecisionInput(request.Input)
	if err != nil {
		return nil, err
	}
	names, err := schemas.DecisionQuestionNames(request.Questions)
	if err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err.Error())
	}
	keys := geminiDecisionQuestionKeys(names)
	questions := make(map[string]GeminiDecisionQuestion, len(request.Questions))
	for i, question := range request.Questions {
		native, err := toGeminiDecisionQuestion(names[i], question)
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

// geminiDecisionQuestionKeys returns the key each question is sent under, in
// question order. Gemini takes only 1 to 64 of [A-Za-z0-9_-] as a question key,
// and its model never reads keys, so a name Gemini takes is its own key and any
// other name, such as "Is the sky blue?", gets question_<position>, suffixed
// when a name already holds that key. The answers come back under the same keys.
func geminiDecisionQuestionKeys(names []string) []string {
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
	return keys
}

// isGeminiDecisionKey reports whether Gemini takes key as a question key: 1 to
// 64 ASCII letters, digits, underscores, or hyphens.
func isGeminiDecisionKey(key string) bool {
	if key == "" || len(key) > geminiDecisionMaxKeyLength {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// toGeminiDecisionInput writes the input as Gemini reads it: text as it is, a
// structured input as its sorted JSON text (Google asks for JSON data as text),
// and messages as geminiDecisionMessages lays them out. An input that comes out
// empty, such as blank text or messages with no content, is rejected before the
// call, since Gemini requires one.
func toGeminiDecisionInput(input schemas.DecisionInput) (GeminiDecisionInput, error) {
	if err := input.Validate(); err != nil {
		return GeminiDecisionInput{}, providerUtils.InvalidRequestErrorf("%s", err.Error())
	}
	var converted GeminiDecisionInput
	switch {
	case input.Text != nil:
		converted = GeminiDecisionInput{Text: input.Text}
	case input.Messages != nil:
		var err error
		if converted, err = geminiDecisionMessages(input.Messages); err != nil {
			return GeminiDecisionInput{}, err
		}
	case input.Structured != nil:
		text, err := geminiDecisionJSONText(input.Structured)
		if err != nil {
			return GeminiDecisionInput{}, err
		}
		converted = GeminiDecisionInput{Text: &text}
	default:
		return GeminiDecisionInput{}, providerUtils.InvalidRequestErrorf("decision input is required")
	}
	if len(converted.Blocks) == 0 && (converted.Text == nil || strings.TrimSpace(*converted.Text) == "") {
		return GeminiDecisionInput{}, providerUtils.InvalidRequestErrorf("gemini decisions require an input, and this request's input carries no content")
	}
	return converted, nil
}

// geminiDecisionMessages lays a message list out as Gemini's input, which has
// no messages: each message's text and media in order, with a newline between
// messages because Gemini joins adjacent text with no separator, and a tool
// call or output as its JSON text. When the messages come from more than one
// role, or carry tool items, each message opens with its role ("user: ") so
// the model can tell who said what; with one speaker a label would add
// nothing. An input that comes out as text alone is sent as a string.
func geminiDecisionMessages(messages []schemas.DecisionInputMessage) (GeminiDecisionInput, error) {
	labelled := geminiDecisionHasSpeakers(messages)
	var input geminiDecisionInputBuilder
	for i, message := range messages {
		if i > 0 {
			input.addText("\n")
		}
		if isGeminiDecisionToolItem(message) {
			text, err := geminiDecisionToolItemText(message)
			if err != nil {
				return GeminiDecisionInput{}, err
			}
			input.addText(text)
			continue
		}
		// JSON decoding sets one of the two, but a Go caller can set both, and
		// sending the text alone would drop the parts' media unseen.
		if message.Content.Text != nil && message.Content.Parts != nil {
			return GeminiDecisionInput{}, providerUtils.InvalidRequestErrorf("decision message %d sets both text and parts; send one", i)
		}
		if labelled {
			input.addText(geminiDecisionRole(message) + ": ")
		}
		if message.Content.Text != nil {
			input.addText(*message.Content.Text)
			continue
		}
		for _, part := range message.Content.Parts {
			if err := input.addPart(part); err != nil {
				return GeminiDecisionInput{}, err
			}
		}
	}
	return input.build(), nil
}

// geminiDecisionHasSpeakers reports whether the messages need role labels: they
// come from more than one role, or carry a tool call or output.
func geminiDecisionHasSpeakers(messages []schemas.DecisionInputMessage) bool {
	role := ""
	for i, message := range messages {
		if isGeminiDecisionToolItem(message) {
			return true
		}
		if i == 0 {
			role = geminiDecisionRole(message)
		} else if geminiDecisionRole(message) != role {
			return true
		}
	}
	return false
}

// geminiDecisionRole is a message's role, or "user" for a message sent
// without one.
func geminiDecisionRole(message schemas.DecisionInputMessage) string {
	if message.Role == "" {
		return "user"
	}
	return message.Role
}

// isGeminiDecisionToolItem reports whether the item is a tool call or output
// rather than a message.
func isGeminiDecisionToolItem(message schemas.DecisionInputMessage) bool {
	return message.Type != nil && *message.Type != schemas.DecisionInputItemTypeMessage
}

// geminiDecisionToolItemText is a tool call or output as its JSON text, which
// keeps the call ID an output answers. A tool output carrying an image or file
// is rejected: as text, the model would read its encoding, not the media.
func geminiDecisionToolItemText(item schemas.DecisionInputMessage) (string, error) {
	if item.Output != nil {
		for _, part := range item.Output.Parts {
			if !part.IsTextual() {
				return "", providerUtils.InvalidRequestErrorf("decision input carries a %q part in a tool output, which gemini cannot read; send media in a message", part.Type)
			}
		}
	}
	raw, err := providerUtils.MarshalSorted(item)
	if err != nil {
		return "", providerUtils.InvalidRequestErrorf("decision input item could not be encoded as text: %v", err)
	}
	return string(raw), nil
}

// geminiDecisionInputBuilder collects content blocks, merging adjacent text
// into one block.
type geminiDecisionInputBuilder struct {
	blocks  []GeminiInteractionsContent
	pending strings.Builder
}

func (b *geminiDecisionInputBuilder) addText(text string) {
	b.pending.WriteString(text)
}

func (b *geminiDecisionInputBuilder) addMedia(block GeminiInteractionsContent) {
	b.flush()
	b.blocks = append(b.blocks, block)
}

func (b *geminiDecisionInputBuilder) flush() {
	if b.pending.Len() == 0 {
		return
	}
	text := b.pending.String()
	b.blocks = append(b.blocks, GeminiInteractionsContent{Type: GeminiContentTypeText, Text: &text})
	b.pending.Reset()
}

// build returns the input: a string when there is no media, the blocks
// otherwise.
func (b *geminiDecisionInputBuilder) build() GeminiDecisionInput {
	if len(b.blocks) == 0 {
		text := b.pending.String()
		return GeminiDecisionInput{Text: &text}
	}
	b.flush()
	return GeminiDecisionInput{Blocks: b.blocks}
}

// addPart adds one message part: text, output text, a refusal, or JSON as
// text, and an image or file as its media block. A part of any other type is
// rejected rather than dropped.
func (b *geminiDecisionInputBuilder) addPart(part schemas.DecisionInputPart) error {
	switch part.Type {
	case schemas.DecisionInputPartTypeText, schemas.DecisionInputPartTypeOutputText:
		if part.Text != nil {
			b.addText(providerUtils.DecisionTextString(part.Text))
		}
	case schemas.DecisionInputPartTypeRefusal:
		if part.Refusal != nil {
			b.addText(*part.Refusal)
		}
	case schemas.DecisionInputPartTypeJSON:
		if part.Data.Structured == nil {
			return providerUtils.InvalidRequestErrorf("decision input carries an input_json part without data")
		}
		text, err := geminiDecisionJSONText(part.Data.Structured)
		if err != nil {
			return err
		}
		b.addText(text)
	case schemas.DecisionInputPartTypeImage:
		block, err := geminiDecisionImage(part)
		if err != nil {
			return err
		}
		b.addMedia(block)
	case schemas.DecisionInputPartTypeFile:
		block, err := geminiDecisionFile(part)
		if err != nil {
			return err
		}
		b.addMedia(block)
	case "":
		return providerUtils.InvalidRequestErrorf("decision input carries a part without a type")
	default:
		return providerUtils.InvalidRequestErrorf("decision input carries an %q part, which gemini cannot read; route the request to a provider that accepts it", part.Type)
	}
	return nil
}

// geminiDecisionImage converts an input_image part. A data URL's payload is
// sent as data, as it is, under the data URL's media type; any other URL as a
// uri, under the media type its extension names, since Gemini requires one. An
// image sent by reference (a Typesafe asset) is rejected: Gemini reads only
// images given as evidence.
func geminiDecisionImage(part schemas.DecisionInputPart) (GeminiInteractionsContent, error) {
	if part.IsAsset() {
		return GeminiInteractionsContent{}, providerUtils.InvalidRequestErrorf("decision input carries an image sent by reference, which gemini cannot read; send it in image_url")
	}
	if part.ImageURL == nil || *part.ImageURL == "" {
		return GeminiInteractionsContent{}, providerUtils.InvalidRequestErrorf("decision input carries an input_image part without an image_url")
	}
	block := GeminiInteractionsContent{Type: GeminiContentTypeImage, Resolution: geminiDecisionResolution(part.Detail, GeminiContentTypeImage)}
	url := *part.ImageURL
	if strings.HasPrefix(url, "data:") {
		mediaType, payload, ok := geminiDataURLParts(url)
		if !ok {
			return GeminiInteractionsContent{}, providerUtils.InvalidRequestErrorf("input_image data URL must be base64 and state its media type, such as data:image/png;base64,...")
		}
		if !strings.HasPrefix(mediaType, "image/") {
			return GeminiInteractionsContent{}, providerUtils.InvalidRequestErrorf("input_image data URL has media type %q, not an image type; send other media as input_file", mediaType)
		}
		block.Data, block.MimeType = &payload, &mediaType
		return block, nil
	}
	mediaType := mimeTypeFromURI(url)
	if !strings.HasPrefix(mediaType, "image/") {
		// The URL is not named: a signed URL's query string is a credential, and
		// this message is kept in the request's log row.
		return GeminiInteractionsContent{}, providerUtils.InvalidRequestErrorf("the media type of an input_image URL cannot be told from its extension; send the image as a base64 data URL")
	}
	block.URI, block.MimeType = &url, &mediaType
	return block, nil
}

// geminiDataURLParts splits a base64 data URL into its media type and payload.
// It reads only up to the first comma, unlike schemas.ParseDataURL's regular
// expression, which scans the whole payload of what can be a large image.
func geminiDataURLParts(url string) (mediaType string, payload string, ok bool) {
	header, payload, found := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
	if !found || payload == "" {
		return "", "", false
	}
	segments := strings.Split(header, ";")
	mediaType = strings.ToLower(strings.TrimSpace(segments[0]))
	isBase64 := false
	for _, segment := range segments[1:] {
		if strings.EqualFold(strings.TrimSpace(segment), "base64") {
			isBase64 = true
		}
	}
	return mediaType, payload, mediaType != "" && isBase64
}

// geminiDecisionFile converts an input_file part into the block its media type
// names: audio/* an audio block, video/* a video block, and any other type a
// document block, Gemini's block for application/pdf. Which media types and
// resolutions a block takes is Gemini's to check, so a file it does not read
// gets Gemini's own error. The bytes are sent as base64 data, or the URL as a
// uri, under the part's media type.
func geminiDecisionFile(part schemas.DecisionInputPart) (GeminiInteractionsContent, error) {
	if err := part.ValidateFile(); err != nil {
		return GeminiInteractionsContent{}, providerUtils.InvalidRequestErrorf("%s", err.Error())
	}
	mediaType := strings.TrimSpace(*part.MimeType)
	lower := strings.ToLower(mediaType)
	blockType := GeminiContentTypeDocument
	switch {
	case strings.HasPrefix(lower, "audio/"):
		blockType = GeminiContentTypeAudio
	case strings.HasPrefix(lower, "video/"):
		blockType = GeminiContentTypeVideo
	}
	block := GeminiInteractionsContent{Type: blockType, MimeType: &mediaType, Resolution: geminiDecisionResolution(part.Detail, blockType)}
	if len(part.Data.File) > 0 {
		data := base64.StdEncoding.EncodeToString(part.Data.File)
		block.Data = &data
	} else {
		url := *part.URL
		block.URI = &url
	}
	return block, nil
}

// geminiDecisionResolution maps a part's detail onto Gemini's media
// resolution for a block of blockType. The detail is read case-insensitively.
// "auto", or no detail, leaves it to Gemini; "original", the full resolution,
// is the highest the block takes: ultra_high for an image (Gemini serves it for
// images only) and high for anything else; any other value, such as "low",
// "high", or Gemini's own "medium", is sent lowercased. Gemini checks which
// blocks take a resolution (audio and documents take none), so a detail it
// does not take gets Gemini's own error rather than being dropped.
func geminiDecisionResolution(detail *string, blockType string) *string {
	if detail == nil {
		return nil
	}
	resolution := strings.ToLower(strings.TrimSpace(*detail))
	switch resolution {
	case "", "auto":
		return nil
	case "original":
		resolution = "high"
		if blockType == GeminiContentTypeImage {
			resolution = "ultra_high"
		}
	}
	return &resolution
}

// geminiDecisionJSONText is a structured value as sorted JSON text.
func geminiDecisionJSONText(value interface{}) (string, error) {
	raw, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return "", providerUtils.InvalidRequestErrorf("decision input could not be encoded as JSON text: %v", err)
	}
	return string(raw), nil
}

// geminiDecisionValueText is a description value as text: a string as it is,
// a structured value as its sorted JSON, nil as empty.
func geminiDecisionValueText(value interface{}) string {
	return providerUtils.DecisionTextString(&schemas.DecisionText{Structured: value})
}

// toGeminiDecisionQuestion converts one question to Gemini's, with its
// criteria in the form its type takes. Gemini's questions are text only, so a
// structured description is sent as its JSON text.
func toGeminiDecisionQuestion(name string, question schemas.DecisionQuestion) (GeminiDecisionQuestion, error) {
	nativeType, ok := geminiDecisionQuestionTypes[question.Type]
	if !ok {
		if question.Type == schemas.DecisionTypeBox {
			return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("question %q is a box question, which gemini does not serve; it serves predicate, choice, and score questions", name)
		}
		return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("question %q has unsupported type %q; expected predicate, choice, or score", name, question.Type)
	}
	instructions, err := geminiDecisionInstructions(name, question.Instructions)
	if err != nil {
		return GeminiDecisionQuestion{}, err
	}
	native := GeminiDecisionQuestion{Type: nativeType, Instructions: instructions}

	switch question.Type {
	case schemas.DecisionTypePredicate:
		if question.Criteria != nil {
			noul := GeminiNoulCriteria{
				TrueDescription:  geminiDecisionDescription(question.Criteria.True),
				FalseDescription: geminiDecisionDescription(question.Criteria.False),
			}
			if noul.TrueDescription != nil || noul.FalseDescription != nil {
				native.Criteria = &GeminiDecisionCriteria{Noul: &noul}
			}
		}
	case schemas.DecisionTypeChoice:
		if len(question.Choices) == 0 {
			return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("question %q has type choice and requires choices", name)
		}
		options := make(map[string]string, len(question.Choices))
		for i, choice := range question.Choices {
			key, err := choice.Key()
			if err != nil {
				return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("question %q choice %d: %s", name, i, err.Error())
			}
			if _, seen := options[key]; seen {
				return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("question %q has more than one choice %q", name, key)
			}
			options[key] = providerUtils.DecisionTextString(choice.Description)
		}
		native.Criteria = &GeminiDecisionCriteria{Choice: options}
	case schemas.DecisionTypeScore:
		if len(question.Levels) == 0 {
			return GeminiDecisionQuestion{}, providerUtils.InvalidRequestErrorf("question %q has type score and requires levels", name)
		}
		levels := make([]string, len(question.Levels))
		for i, level := range question.Levels {
			levels[i] = geminiDecisionLevel(i, level)
		}
		native.Criteria = &GeminiDecisionCriteria{Score: levels}
	}
	return native, nil
}

// geminiDecisionDescription is one side of a predicate's criteria as text, or
// nil when the side was not sent, was sent as null, or is empty.
func geminiDecisionDescription(side *schemas.DecisionText) *string {
	if side == nil {
		return nil
	}
	text := providerUtils.DecisionTextString(side)
	if text == "" {
		return nil
	}
	return &text
}

// geminiDecisionLevel is a score level's description, with any label folded in
// as other unlabelled providers fold it. Gemini rejects an empty level, so a
// level with neither is described by its label or, without one, its index.
func geminiDecisionLevel(index int, level schemas.DecisionLevel) string {
	text := geminiDecisionValueText(providerUtils.DecisionLevelDescription(index, level))
	if strings.TrimSpace(text) != "" {
		return text
	}
	if level.Label != "" {
		return level.Label
	}
	return strconv.Itoa(index)
}

// geminiDecisionInstructionMediaTypes are the part types that carry media,
// which Gemini's question instructions cannot hold.
var geminiDecisionInstructionMediaTypes = map[string]bool{
	schemas.DecisionInputPartTypeImage: true,
	schemas.DecisionInputPartTypeFile:  true,
	"input_audio":                      true,
}

// geminiDecisionInstructions writes a question's instructions as the text
// Gemini's questions take. Instructions written as a list of parts (input_text
// and input_json, as OpenAI's questions take them) are joined line by line.
// Instructions carrying an image, file, or audio part, as a list holding one
// or as a single part, are rejected, since Gemini's instructions are text only
// and the media would otherwise go out as JSON text the model reads instead of
// seeing. Any other structured value is sent as its JSON text.
func geminiDecisionInstructions(name string, instructions *schemas.DecisionText) (string, error) {
	if instructions == nil {
		return "", nil
	}
	parts, ok := geminiDecisionInstructionList(instructions.Structured)
	if !ok {
		if part, isObject := geminiDecisionInstructionObject(instructions.Structured); isObject {
			if media := geminiDecisionInstructionMedia([]interface{}{part}); media != "" {
				return "", providerUtils.InvalidRequestErrorf("question %q instructions carry an %q part; gemini's question instructions are text only", name, media)
			}
		}
		return providerUtils.DecisionTextString(instructions), nil
	}
	if media := geminiDecisionInstructionMedia(parts); media != "" {
		return "", providerUtils.InvalidRequestErrorf("question %q instructions carry an %q part; gemini's question instructions are text only", name, media)
	}
	if !isDecisionInstructionPartList(parts) {
		return providerUtils.DecisionTextString(instructions), nil
	}
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		object := part.(map[string]interface{})
		if object["type"] == schemas.DecisionInputPartTypeText {
			lines = append(lines, geminiDecisionValueText(object["text"]))
		} else {
			lines = append(lines, geminiDecisionValueText(object["data"]))
		}
	}
	return strings.Join(lines, "\n"), nil
}

// geminiDecisionInstructionMedia returns the type of the first part in the list
// that carries media, or "" when none does.
func geminiDecisionInstructionMedia(parts []interface{}) string {
	for _, part := range parts {
		object, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		if partType, _ := object["type"].(string); geminiDecisionInstructionMediaTypes[partType] {
			return partType
		}
	}
	return ""
}

// geminiDecisionInstructionList returns structured instructions as a list in the
// []interface{} shape a JSON request decodes into, or false when they are not a
// list. A Go caller's typed slice, such as []schemas.DecisionInputPart, is
// encoded and decoded into that shape, so it is read the way the same list sent
// over HTTP is; only such a slice pays for the round trip.
func geminiDecisionInstructionList(value interface{}) ([]interface{}, bool) {
	if list, ok := value.([]interface{}); ok {
		return list, true
	}
	if value == nil || reflect.TypeOf(value).Kind() != reflect.Slice {
		return nil, false
	}
	raw, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return nil, false
	}
	var list []interface{}
	if err := schemas.Unmarshal(raw, &list); err != nil {
		return nil, false
	}
	return list, true
}

// geminiDecisionInstructionObject returns structured instructions as the
// map[string]interface{} shape a JSON object decodes into, or false when they
// are not an object. A Go caller's struct, such as a schemas.DecisionInputPart,
// is encoded and decoded into that shape, so a single part is read the way the
// same part sent over HTTP is.
func geminiDecisionInstructionObject(value interface{}) (map[string]interface{}, bool) {
	if object, ok := value.(map[string]interface{}); ok {
		return object, true
	}
	if value == nil {
		return nil, false
	}
	kind := reflect.TypeOf(value).Kind()
	if kind == reflect.Pointer {
		kind = reflect.TypeOf(value).Elem().Kind()
	}
	if kind != reflect.Struct && kind != reflect.Map {
		return nil, false
	}
	raw, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return nil, false
	}
	var object map[string]interface{}
	if err := schemas.Unmarshal(raw, &object); err != nil {
		return nil, false
	}
	return object, true
}

// isDecisionInstructionPartList reports whether instructions are a non-empty
// list of input_text and input_json parts. Media parts are rejected before this
// is asked.
func isDecisionInstructionPartList(parts []interface{}) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		object, ok := part.(map[string]interface{})
		if !ok {
			return false
		}
		switch object["type"] {
		case schemas.DecisionInputPartTypeText, schemas.DecisionInputPartTypeJSON:
		default:
			return false
		}
	}
	return true
}

// ToBifrostDecisionResponse converts a decisions response into the normalized
// shape: answers in question order, matched by the keys the request sent the
// questions under and reported under the questions' own names. Gemini names no
// model, so the requested one is reported, and a score answer without a legend
// gets the one its levels give.
func ToBifrostDecisionResponse(resp *GeminiDecisionResponse, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	names, err := schemas.DecisionQuestionNames(request.Questions)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(err.Error(), nil)
	}
	keys := geminiDecisionQuestionKeys(names)
	answers := make([]schemas.DecisionAnswer, 0, len(request.Questions))
	for i, question := range request.Questions {
		name := names[i]
		native, ok := resp.Answers[keys[i]]
		if !ok {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("gemini returned no answer for question %q", name), nil)
		}
		if expected := geminiDecisionQuestionTypes[question.Type]; native.Type != expected {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("gemini answered question %q as %q; expected %q", name, native.Type, expected), nil)
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
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("gemini noul answer for question %q carries no value", name), nil)
			}
			if *native.Noul < 0 || *native.Noul > 1 {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("gemini noul answer for question %q is outside [0,1]: %v", name, *native.Noul), nil)
			}
			answer.Probability = native.Noul
		case schemas.DecisionTypeChoice:
			if native.Choice == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("gemini choice answer for question %q carries no value", name), nil)
			}
			choice := providerUtils.DecisionChoiceForKey(question, *native.Choice)
			answer.Choice = &choice
		case schemas.DecisionTypeScore:
			if native.Score == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("gemini score answer for question %q carries no value", name), nil)
			}
			answer.Score = native.Score
			answer.Legend = native.Legend
			if answer.Legend == nil {
				answer.Legend = providerUtils.DecisionScoreLegend(question)
			}
		}
		answers = append(answers, answer)
	}

	response := &schemas.BifrostDecisionResponse{Model: request.Model, Answers: answers}
	if resp.Usage != nil {
		response.Usage = resp.Usage.toBifrostLLMUsage()
	}
	return response, nil
}

// toBifrostLLMUsage reports the input tokens as prompt tokens, split into
// text, image, and audio as Gemini splits them (video frames and PDF pages
// count as image). Decisions generate no output.
func (u *GeminiDecisionUsage) toBifrostLLMUsage() *schemas.BifrostLLMUsage {
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = u.InputTokens + u.OutputTokens
	}
	var details schemas.ChatPromptTokensDetails
	for _, modality := range u.InputTokensByModality {
		switch strings.ToLower(modality.Modality) {
		case "text":
			details.TextTokens += modality.Tokens
		case "image":
			details.ImageTokens += modality.Tokens
		case "audio":
			details.AudioTokens += modality.Tokens
		}
	}
	if details.TextTokens+details.ImageTokens+details.AudioTokens > 0 {
		usage.PromptTokensDetails = &details
	}
	return usage
}
