package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decisionText(text string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText(text)}
}

func decisionImage(url string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr(url)}
}

func decisionFile(mimeType string, edit func(*schemas.DecisionInputPart)) schemas.DecisionInputPart {
	part := schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeFile, MimeType: schemas.Ptr(mimeType)}
	edit(&part)
	return part
}

func decisionMessage(role string, parts ...schemas.DecisionInputPart) schemas.DecisionInputMessage {
	return schemas.DecisionInputMessage{Role: role, Content: schemas.DecisionInputContent{Parts: parts}}
}

func isDamagedQuestion() []schemas.DecisionQuestion {
	return []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_damaged"), Instructions: schemas.NewDecisionText("Is the item damaged?")}}
}

// geminiDecisionBody converts a request and returns its wire JSON, encoded as
// the provider encodes it.
func geminiDecisionBody(t *testing.T, request *schemas.BifrostDecisionRequest) string {
	t.Helper()
	native, err := ToGeminiDecisionRequest(request)
	require.NoError(t, err)
	body, err := providerUtils.MarshalSorted(native)
	require.NoError(t, err)
	return string(body)
}

// TestToGeminiDecisionInput pins how each input becomes Gemini's input, which
// has no messages: text as it is, structured input as sorted JSON text, one
// speaker's messages as their content alone (a string when there is no
// media), and several speakers' messages with a role label each, media as
// blocks in place, and tool items as JSON text.
func TestToGeminiDecisionInput(t *testing.T) {
	callType, outputType := schemas.DecisionInputItemTypeFunctionCall, schemas.DecisionInputItemTypeFunctionCallOutput
	for name, tc := range map[string]struct {
		input schemas.DecisionInput
		want  string
	}{
		"text": {
			input: schemas.DecisionInput{Text: schemas.Ptr("The mug arrived cracked.")},
			want:  `"The mug arrived cracked."`,
		},
		"structured as sorted JSON text": {
			input: schemas.DecisionInput{Structured: map[string]any{"tier": "Gold", "order": map[string]any{"id": "8821"}}},
			want:  `"{\"order\":{\"id\":\"8821\"},\"tier\":\"Gold\"}"`,
		},
		"one speaker, text only": {
			input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
				{Role: "user", Content: schemas.DecisionInputContent{Text: schemas.Ptr("Order #8821 arrived late.")}},
				decisionMessage("", decisionText("Can I get a refund?")),
			}},
			want: `"Order #8821 arrived late.\nCan I get a refund?"`,
		},
		"one speaker with media": {
			input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{decisionMessage("user",
				decisionText("Customer photo."),
				decisionImage("data:image/jpeg;base64,/9j/4AAQ"),
				decisionFile("application/pdf", func(p *schemas.DecisionInputPart) { p.Data.File = []byte("%PDF") }),
			)}},
			want: `[{"text":"Customer photo.","type":"text"},{"data":"/9j/4AAQ","mime_type":"image/jpeg","type":"image"},{"data":"JVBERg==","mime_type":"application/pdf","type":"document"}]`,
		},
		"several speakers keep their roles": {
			input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
				decisionMessage("user", decisionText("My headphones broke, see photo"), decisionImage("data:image/jpeg;base64,/9j/")),
				{Role: "assistant", Content: schemas.DecisionInputContent{Text: schemas.Ptr("Sorry! Can you send a clip of the sound?")}},
				decisionMessage("user", decisionFile("audio/mpeg", func(p *schemas.DecisionInputPart) { p.URL = schemas.Ptr("https://example.com/left.mp3") })),
			}},
			want: `[{"text":"user: My headphones broke, see photo","type":"text"},{"data":"/9j/","mime_type":"image/jpeg","type":"image"},{"text":"\nassistant: Sorry! Can you send a clip of the sound?\nuser: ","type":"text"},{"mime_type":"audio/mpeg","type":"audio","uri":"https://example.com/left.mp3"}]`,
		},
		"several speakers, text only": {
			input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
				{Role: "user", Content: schemas.DecisionInputContent{Text: schemas.Ptr("Can I cancel?")}},
				{Role: "assistant", Content: schemas.DecisionInputContent{Text: schemas.Ptr("You could cancel your plan.")}},
			}},
			want: `"user: Can I cancel?\nassistant: You could cancel your plan."`,
		},
		"tool items as JSON text": {
			input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
				{Role: "user", Content: schemas.DecisionInputContent{Text: schemas.Ptr("Weather in Paris?")}},
				{Type: &callType, CallID: schemas.Ptr("c1"), Name: schemas.Ptr("weather"), Arguments: schemas.Ptr(`{"city":"Paris"}`)},
				{Type: &outputType, CallID: schemas.Ptr("c1"), Output: &schemas.DecisionInputContent{Text: schemas.Ptr("18C")}},
			}},
			want: `"user: Weather in Paris?\n{\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}\n{\"type\":\"function_call_output\",\"call_id\":\"c1\",\"output\":\"18C\"}"`,
		},
		"json part and refusal as text": {
			input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{decisionMessage("assistant",
				schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeJSON, Data: schemas.DecisionData{Structured: map[string]any{"b": 1, "a": 2}}},
				schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeRefusal, Refusal: schemas.Ptr(" I can't help.")},
			)}},
			want: `"{\"a\":2,\"b\":1} I can't help."`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := geminiDecisionBody(t, &schemas.BifrostDecisionRequest{Model: "gemini-decision-1", Input: tc.input, Questions: isDamagedQuestion()})
			var wire struct {
				Input json.RawMessage `json:"input"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &wire))
			assert.JSONEq(t, tc.want, string(wire.Input))
		})
	}
}

// TestToGeminiDecisionMedia pins each media part's block: images from a data
// URL (sent as its payload) or a URL with an image extension, and files by
// media type (audio, video, PDF) from bytes or a URL, with detail mapped onto
// Gemini's resolution.
func TestToGeminiDecisionMedia(t *testing.T) {
	for name, tc := range map[string]struct {
		part schemas.DecisionInputPart
		want string
	}{
		"image data URL with detail": {
			part: schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,iVBOR"), Detail: schemas.Ptr("low")},
			want: `{"type":"image","data":"iVBOR","mime_type":"image/png","resolution":"low"}`,
		},
		"image URL, original detail": {
			part: schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("https://example.com/box.webp?sig=1"), Detail: schemas.Ptr("original")},
			want: `{"type":"image","uri":"https://example.com/box.webp?sig=1","mime_type":"image/webp","resolution":"ultra_high"}`,
		},
		"image auto detail": {
			part: schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("https://example.com/box.png"), Detail: schemas.Ptr("auto")},
			want: `{"type":"image","uri":"https://example.com/box.png","mime_type":"image/png"}`,
		},
		"detail read case-insensitively": {
			part: schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("https://example.com/box.png"), Detail: schemas.Ptr("Original")},
			want: `{"type":"image","uri":"https://example.com/box.png","mime_type":"image/png","resolution":"ultra_high"}`,
		},
		"uppercase detail sent lowercased": {
			part: schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,iVBOR"), Detail: schemas.Ptr("HIGH")},
			want: `{"type":"image","data":"iVBOR","mime_type":"image/png","resolution":"high"}`,
		},
		"pdf bytes": {
			part: decisionFile("application/pdf", func(p *schemas.DecisionInputPart) { p.Data.File = []byte("%PDF-1.7") }),
			want: `{"type":"document","data":"JVBERi0xLjc=","mime_type":"application/pdf"}`,
		},
		"audio URL": {
			part: decisionFile("audio/mpeg", func(p *schemas.DecisionInputPart) { p.URL = schemas.Ptr("https://example.com/call.mp3") }),
			want: `{"type":"audio","uri":"https://example.com/call.mp3","mime_type":"audio/mpeg"}`,
		},
		"video Files API URI at medium resolution": {
			part: decisionFile("video/mp4", func(p *schemas.DecisionInputPart) {
				p.URL, p.Detail = schemas.Ptr("https://generativelanguage.googleapis.com/v1beta/files/abc123"), schemas.Ptr("medium")
			}),
			want: `{"type":"video","uri":"https://generativelanguage.googleapis.com/v1beta/files/abc123","mime_type":"video/mp4","resolution":"medium"}`,
		},
		"video from a multipart file part": {
			part: decisionFile("video/webm", func(p *schemas.DecisionInputPart) { p.Name, p.Data.File = schemas.Ptr("clip"), []byte("webm") }),
			want: `{"type":"video","data":"d2VibQ==","mime_type":"video/webm"}`,
		},
		// Which media types and resolutions a block takes is Gemini's to check:
		// these are sent as they are, and Gemini answers for them.
		"other file type sent as a document": {
			part: decisionFile("text/csv", func(p *schemas.DecisionInputPart) { p.Data.File = []byte("a,b") }),
			want: `{"type":"document","data":"YSxi","mime_type":"text/csv"}`,
		},
		"detail on audio sent as a resolution": {
			part: decisionFile("audio/mpeg", func(p *schemas.DecisionInputPart) {
				p.URL, p.Detail = schemas.Ptr("https://example.com/a.mp3"), schemas.Ptr("low")
			}),
			want: `{"type":"audio","uri":"https://example.com/a.mp3","mime_type":"audio/mpeg","resolution":"low"}`,
		},
		// Gemini serves ultra_high for images only, so a video's original
		// detail is the highest resolution video takes.
		"original detail on video is high": {
			part: decisionFile("video/mp4", func(p *schemas.DecisionInputPart) {
				p.URL, p.Detail = schemas.Ptr("https://example.com/a.mp4"), schemas.Ptr("original")
			}),
			want: `{"type":"video","uri":"https://example.com/a.mp4","mime_type":"video/mp4","resolution":"high"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			native, err := ToGeminiDecisionRequest(&schemas.BifrostDecisionRequest{
				Model:     "gemini-decision-1",
				Input:     schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{decisionMessage("user", tc.part)}},
				Questions: isDamagedQuestion(),
			})
			require.NoError(t, err)
			require.Len(t, native.Input.Blocks, 1)
			block, err := providerUtils.MarshalSorted(native.Input.Blocks[0])
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(block))
		})
	}
}

// TestToGeminiDecisionRequestRejects pins what is refused before Gemini is
// called, each with a reason the caller can act on, rather than dropped or
// sent in a form the model would misread.
func TestToGeminiDecisionRequestRejects(t *testing.T) {
	inMessage := func(part schemas.DecisionInputPart) schemas.DecisionInput {
		return schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{decisionMessage("user", part)}}
	}
	outputType := schemas.DecisionInputItemTypeFunctionCallOutput
	for name, tc := range map[string]struct {
		input     schemas.DecisionInput
		questions []schemas.DecisionQuestion
		want      string
		absent    string // text the message must not carry, such as a URL's signature
	}{
		"empty input":        {input: schemas.DecisionInput{}, want: "decision input is required"},
		"image file":         {input: inMessage(decisionFile("image/png", func(p *schemas.DecisionInputPart) { p.Data.File = []byte("png") })), want: "send images as input_image"},
		"file without bytes": {input: inMessage(decisionFile("audio/mpeg", func(*schemas.DecisionInputPart) {})), want: "requires data, url"},
		"asset image":        {input: inMessage(schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("r-cam"), Data: schemas.DecisionData{File: []byte("png")}}), want: "sent by reference"},
		// A signed URL's query string is a credential, and the message is
		// persisted in the request's log row, so it names no URL.
		"image URL without type": {
			input:  inMessage(decisionImage("https://storage.example.com/photo?X-Goog-Signature=s3cr3t")),
			want:   "cannot be told from its extension",
			absent: "s3cr3t",
		},
		"non-base64 data URL":    {input: inMessage(decisionImage("data:image/png,raw")), want: "must be base64"},
		"unknown part":           {input: inMessage(schemas.DecisionInputPart{Type: "input_audio"}), want: `"input_audio" part`},
		"media in a tool output": {input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Type: &outputType, CallID: schemas.Ptr("c1"), Output: &schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{decisionImage("data:image/png;base64,AA")}}}}}, want: "tool output"},
		// A Go caller can set both; JSON decoding sets one, so only this path
		// could drop the parts' media unseen.
		"text and parts on one message": {input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{
			Text: schemas.Ptr("Here is the photo."), Parts: []schemas.DecisionInputPart{decisionImage("data:image/png;base64,AA")},
		}}}}, want: "sets both text and parts"},
		"box question": {
			input:     schemas.DecisionInput{Text: schemas.Ptr("photo")},
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypeBox, Name: schemas.Ptr("dent"), Instructions: schemas.NewDecisionText("Where is the dent?")}},
			want:      "box question",
		},
		"choice without choices": {
			input:     schemas.DecisionInput{Text: schemas.Ptr("x")},
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("dept")}},
			want:      "requires choices",
		},
		"image in instructions": {
			input:     schemas.DecisionInput{Text: schemas.Ptr("x")},
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Instructions: &schemas.DecisionText{Structured: []interface{}{map[string]interface{}{"type": "input_image", "image_url": "https://example.com/a.png"}}}}},
			want:      "text only",
		},
		// A Go caller's typed slice is read as the same list decoded from JSON.
		"image in typed instructions": {
			input: schemas.DecisionInput{Text: schemas.Ptr("x")},
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Instructions: &schemas.DecisionText{Structured: []schemas.DecisionInputPart{
				decisionText("Is the item in this photo red?"), decisionImage("https://example.com/a.png"),
			}}}},
			want: "text only",
		},
		// A file is not one of the instruction part types, so a list carrying one
		// must not fall back to JSON text with its image inside.
		"image and file in instructions": {
			input: schemas.DecisionInput{Text: schemas.Ptr("x")},
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Instructions: &schemas.DecisionText{Structured: []interface{}{
				map[string]interface{}{"type": "input_text", "text": "Does the photo match the receipt?"},
				map[string]interface{}{"type": "input_image", "image_url": "https://example.com/a.png"},
				map[string]interface{}{"type": "input_file", "url": "https://example.com/a.pdf", "mime_type": "application/pdf"},
			}}}},
			want: "text only",
		},
		"file in instructions": {
			input: schemas.DecisionInput{Text: schemas.Ptr("x")},
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Instructions: &schemas.DecisionText{Structured: []interface{}{
				map[string]interface{}{"type": "input_file", "url": "https://example.com/a.pdf", "mime_type": "application/pdf"},
			}}}},
			want: `"input_file" part`,
		},
		// Gemini requires an input, so one that converts to nothing is refused
		// rather than spent on an upstream call.
		"blank text": {input: schemas.DecisionInput{Text: schemas.Ptr("  ")}, want: "carries no content"},
		"messages without content": {input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
			{Role: "user", Content: schemas.DecisionInputContent{Text: schemas.Ptr("")}},
			{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{{Type: schemas.DecisionInputPartTypeText}}}},
		}}, want: "carries no content"},
		"non-image data URL": {input: inMessage(decisionImage("data:application/pdf;base64,JVBERi0=")), want: "not an image type"},
		"image in instructions after an unrecognized element": {
			input: schemas.DecisionInput{Text: schemas.Ptr("x")},
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Instructions: &schemas.DecisionText{Structured: []interface{}{
				map[string]interface{}{"note": "not a part"},
				map[string]interface{}{"type": "input_image", "image_url": "data:image/png;base64,iVBO"},
			}}}},
			want: "text only",
		},
		// Instructions decode an object into Structured too, so a single media
		// part must not go out as JSON text.
		"image as single instruction object": {
			input: schemas.DecisionInput{Text: schemas.Ptr("x")},
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Instructions: &schemas.DecisionText{
				Structured: map[string]interface{}{"type": "input_image", "image_url": "https://example.com/a.png"},
			}}},
			want: `"input_image" part`,
		},
		"image as a typed instruction part": {
			input: schemas.DecisionInput{Text: schemas.Ptr("x")},
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Instructions: &schemas.DecisionText{
				Structured: decisionImage("https://example.com/a.png"),
			}}},
			want: "text only",
		},
	} {
		t.Run(name, func(t *testing.T) {
			questions := tc.questions
			if questions == nil {
				questions = isDamagedQuestion()
			}
			_, err := ToGeminiDecisionRequest(&schemas.BifrostDecisionRequest{Model: "gemini-decision-1", Input: tc.input, Questions: questions})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			if tc.absent != "" {
				assert.NotContains(t, err.Error(), tc.absent)
			}
		})
	}
}

// TestGeminiDecisionStructuredInstructions pins that instructions written as
// structured data, not as content parts, are still sent as their JSON text,
// even a list of objects with types of their own.
func TestGeminiDecisionStructuredInstructions(t *testing.T) {
	native, err := ToGeminiDecisionRequest(&schemas.BifrostDecisionRequest{
		Model: "gemini-decision-1",
		Input: schemas.DecisionInput{Text: schemas.Ptr("A red sedan.")},
		Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_car"), Instructions: &schemas.DecisionText{
			Structured: []interface{}{map[string]interface{}{"type": "sedan"}, map[string]interface{}{"type": "coupe"}},
		}}},
	})
	require.NoError(t, err)
	assert.Equal(t, `[{"type":"sedan"},{"type":"coupe"}]`, native.Questions["is_car"].Instructions)
}

// TestToGeminiDecisionQuestions pins the question mapping: predicate criteria
// as true_description and false_description, choices keyed by option with an
// empty description allowed, score levels as non-empty text lowest first with
// labels folded in, and instructions written as parts joined into text.
func TestToGeminiDecisionQuestions(t *testing.T) {
	yes := true
	request := &schemas.BifrostDecisionRequest{
		Model: "gemini-decision-1",
		Input: schemas.DecisionInput{Text: schemas.Ptr("Customer returned the sweater: wrong color.")},
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_defect"), Instructions: schemas.NewDecisionText("Is this a defect?"),
				Criteria: &schemas.DecisionCriteria{True: schemas.NewDecisionText("Broken or faulty."), False: &schemas.DecisionText{}}},
			{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("dept"), Instructions: schemas.NewDecisionText("Who handles it?"),
				Choices: []schemas.DecisionChoice{
					{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Description: schemas.NewDecisionText("Payments.")},
					{Value: schemas.DecisionScalar{Str: schemas.Ptr("other")}},
					{Value: schemas.DecisionScalar{Bool: &yes}, Description: &schemas.DecisionText{Structured: map[string]any{"k": "v"}}},
				}},
			{Type: schemas.DecisionTypeScore, Instructions: &schemas.DecisionText{Structured: []interface{}{
				map[string]interface{}{"type": "input_text", "text": "How severe is it?"},
				map[string]interface{}{"type": "input_json", "data": map[string]interface{}{"scale": "0-2"}},
			}},
				Levels: []schemas.DecisionLevel{{Label: "0", Description: schemas.NewDecisionText("Cosmetic")}, {Label: "blocked"}, {}}},
		},
	}
	body := geminiDecisionBody(t, request)
	var wire struct {
		Questions json.RawMessage `json:"questions"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &wire))
	assert.JSONEq(t, `{
		"is_defect": {"type": "noul", "instructions": "Is this a defect?", "criteria": {"true_description": "Broken or faulty."}},
		"dept": {"type": "choice", "instructions": "Who handles it?", "criteria": {"billing": "Payments.", "other": "", "true": "{\"k\":\"v\"}"}},
		"question_3": {"type": "score", "instructions": "How severe is it?\n{\"scale\":\"0-2\"}", "criteria": ["Cosmetic", "blocked", "2"]}
	}`, string(wire.Questions))
	assert.Contains(t, body, `"model":"gemini-decision-1"`)
}

// TestGeminiDecisionTypedInstructionParts pins that instructions a Go caller
// writes as a typed slice of parts are joined into text, as the same parts
// decoded from JSON are, rather than sent as the slice's JSON text.
func TestGeminiDecisionTypedInstructionParts(t *testing.T) {
	native, err := ToGeminiDecisionRequest(&schemas.BifrostDecisionRequest{
		Model: "gemini-decision-1",
		Input: schemas.DecisionInput{Text: schemas.Ptr("Export fails in Safari.")},
		Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_bug"), Instructions: &schemas.DecisionText{
			Structured: []schemas.DecisionInputPart{decisionText("Is this a bug?"), decisionText("Answer for the export feature.")},
		}}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Is this a bug?\nAnswer for the export feature.", native.Questions["is_bug"].Instructions)
}

// TestGeminiDecisionQuestionKeys pins the keys questions are sent under. Gemini
// takes only 1 to 64 of [A-Za-z0-9_-] as a key, and its model never reads keys,
// so a name it takes is the key as it is, and any other name gets the key
// question_<position>, suffixed when a name already holds that key. Answers come
// back under those keys and are reported under the questions' own names.
func TestGeminiDecisionQuestionKeys(t *testing.T) {
	predicate := func(name *string) schemas.DecisionQuestion {
		return schemas.DecisionQuestion{Type: schemas.DecisionTypePredicate, Name: name, Instructions: schemas.NewDecisionText("Is it true?")}
	}
	longest := strings.Repeat("k", 64)
	request := &schemas.BifrostDecisionRequest{
		Model: "gemini-decision-1",
		Input: schemas.DecisionInput{Text: schemas.Ptr("The sky is blue.")},
		Questions: []schemas.DecisionQuestion{
			predicate(schemas.Ptr("Is the sky blue?")),
			predicate(schemas.Ptr("a.b")),
			predicate(schemas.Ptr(longest)),
			predicate(schemas.Ptr(longest + "k")),
			predicate(schemas.Ptr("question_2")),
			predicate(nil),
		},
	}
	native, err := ToGeminiDecisionRequest(request)
	require.NoError(t, err)
	keys := make([]string, 0, len(native.Questions))
	for key := range native.Questions {
		keys = append(keys, key)
	}
	assert.ElementsMatch(t, []string{"question_1", "question_2_2", longest, "question_4", "question_2", "question_6"}, keys)

	answers := make(map[string]GeminiDecisionAnswer, len(keys))
	for i, key := range []string{"question_1", "question_2_2", longest, "question_4", "question_2", "question_6"} {
		answers[key] = GeminiDecisionAnswer{Type: GeminiDecisionQuestionTypeNoul, Noul: schemas.Ptr(float64(i) / 10)}
	}
	converted, bifrostErr := ToBifrostDecisionResponse(&GeminiDecisionResponse{Answers: answers}, request)
	require.Nil(t, bifrostErr)
	require.Len(t, converted.Answers, 6)
	for i, answer := range converted.Answers {
		assert.Equal(t, request.Questions[i].Name, answer.Name, "answer %d keeps its question's name", i)
		assert.Equal(t, float64(i)/10, *answer.Probability, "answer %d is the one sent under its question's key", i)
	}
}

// TestToBifrostGeminiDecisionResponse pins the answer mapping: answers in
// question order by the names the questions were sent under, a boolean choice
// answered with its boolean, probabilities ordered by the question, Gemini's
// legend kept, the requested model reported, and usage split by modality.
func TestToBifrostGeminiDecisionResponse(t *testing.T) {
	yes, no := true, false
	request := &schemas.BifrostDecisionRequest{
		Model: "gemini-decision-1",
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypeScore, Levels: []schemas.DecisionLevel{{Label: "0"}, {Label: "1"}}},
			{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("refund"), Choices: []schemas.DecisionChoice{{Value: schemas.DecisionScalar{Bool: &yes}}, {Value: schemas.DecisionScalar{Bool: &no}}}},
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_damaged")},
		},
	}
	var resp GeminiDecisionResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"answers": {
			"is_damaged": {"type": "noul", "noul": 0.97, "confidence": 0.97},
			"refund": {"type": "choice", "choice": "true", "probabilities": {"false": 0.1, "true": 0.9}, "confidence": 0.9},
			"question_1": {"type": "score", "score": 0.8, "legend": {"0": "Fine", "1": "Broken"}, "probabilities": {"0": 0.2, "1": 0.8}, "confidence": 0.8}
		},
		"usage": {"total_input_tokens": 300, "input_tokens_by_modality": [{"modality": "text", "tokens": 40}, {"modality": "image", "tokens": 258}, {"modality": "audio", "tokens": 2}], "total_output_tokens": 0, "total_tokens": 300}
	}`), &resp))

	converted, bifrostErr := ToBifrostDecisionResponse(&resp, request)
	require.Nil(t, bifrostErr)
	assert.Equal(t, "gemini-decision-1", converted.Model)
	require.Len(t, converted.Answers, 3)
	assert.Equal(t, 0.8, *converted.Answers[0].Score)
	assert.Equal(t, map[string]any{"0": "Fine", "1": "Broken"}, converted.Answers[0].Legend)
	assert.Equal(t, 0.2, converted.Answers[0].Probabilities[0].Probability)
	assert.Equal(t, true, *converted.Answers[1].Choice.Bool)
	assert.Equal(t, true, *converted.Answers[1].Probabilities[0].Value.Bool, "ordered by the question's choices")
	assert.Equal(t, 0.97, *converted.Answers[2].Probability)
	assert.Equal(t, "is_damaged", *converted.Answers[2].Name)
	require.NotNil(t, converted.Usage)
	assert.Equal(t, 300, converted.Usage.PromptTokens)
	assert.Equal(t, 300, converted.Usage.TotalTokens)
	assert.Equal(t, schemas.ChatPromptTokensDetails{TextTokens: 40, ImageTokens: 258, AudioTokens: 2}, *converted.Usage.PromptTokensDetails)

	delete(resp.Answers, "refund")
	_, bifrostErr = ToBifrostDecisionResponse(&resp, request)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, `no answer for question "refund"`)

	resp.Answers["refund"] = GeminiDecisionAnswer{Type: "noul", Noul: schemas.Ptr(0.9)}
	_, bifrostErr = ToBifrostDecisionResponse(&resp, request)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, `answered question "refund" as "noul"; expected "choice"`)

	delete(resp.Answers, "refund")
	request.Questions = request.Questions[2:]
	resp.Answers["is_damaged"] = GeminiDecisionAnswer{Type: "noul", Noul: schemas.Ptr(1.2)}
	_, bifrostErr = ToBifrostDecisionResponse(&resp, request)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, `is outside [0,1]`)
}

// TestGeminiDecisionServesDecisionModelsOnly pins the gate that keeps a chat
// model's decisions on emulation: a Gemini Decision model is sent to
// POST {base}/decisions with the API key header, and any other model is
// reported as unsupported without a call.
func TestGeminiDecisionServesDecisionModelsOnly(t *testing.T) {
	var path, apiKey, body string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		path, apiKey = r.URL.Path, r.Header.Get("x-goog-api-key")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"answers":{"is_damaged":{"type":"noul","noul":0.91,"confidence":0.91}},"usage":{"total_input_tokens":12,"total_output_tokens":0,"total_tokens":12}}`)
	}))
	defer server.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-key"}, Models: []string{"*"}}
	request := func(model string) *schemas.BifrostDecisionRequest {
		return &schemas.BifrostDecisionRequest{Provider: schemas.Gemini, Model: model, Input: schemas.DecisionInput{Text: schemas.Ptr("The mug arrived cracked.")}, Questions: isDamagedQuestion()}
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Decision(ctx, key, request("gemini-decision-1"))
	require.Nil(t, bifrostErr)
	assert.Equal(t, "/v1beta/decisions", path)
	assert.Equal(t, "test-key", apiKey)
	assert.JSONEq(t, `{"model":"gemini-decision-1","input":"The mug arrived cracked.","questions":{"is_damaged":{"type":"noul","instructions":"Is the item damaged?"}}}`, body)
	assert.Equal(t, 0.91, *resp.Answers[0].Probability)
	assert.Equal(t, 12, resp.Usage.PromptTokens)

	_, bifrostErr = provider.Decision(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, request("gemini-3-flash"))
	require.NotNil(t, bifrostErr)
	assert.Equal(t, 1, calls, "a chat model is not sent to the decisions endpoint")
	assert.True(t, strings.Contains(bifrostErr.Error.Message, "not supported") || strings.Contains(bifrostErr.Error.Message, "unsupported"), bifrostErr.Error.Message)
}

// BenchmarkToGeminiDecisionRequest measures converting and encoding a decision
// with text, an inline image, and a 1 MB inline video, the work a Gemini
// decision adds before the call. The video's bytes are base64-encoded once.
func BenchmarkToGeminiDecisionRequest(b *testing.B) {
	video := make([]byte, 1<<20)
	request := &schemas.BifrostDecisionRequest{
		Model: "gemini-decision-1",
		Input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{decisionMessage("user",
			decisionText("Return request #8821."),
			decisionImage("data:image/jpeg;base64,"+strings.Repeat("A", 64<<10)),
			decisionFile("video/mp4", func(p *schemas.DecisionInputPart) { p.Data.File = video }),
		)}},
		Questions: isDamagedQuestion(),
	}
	b.ReportAllocs()
	for b.Loop() {
		native, err := ToGeminiDecisionRequest(request)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := providerUtils.MarshalSorted(native); err != nil {
			b.Fatal(err)
		}
	}
}
