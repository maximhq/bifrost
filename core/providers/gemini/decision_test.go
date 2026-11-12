package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decisionCapture is what the fake decisions server saw of the last request.
type decisionCapture struct {
	path   string
	apiKey string
	body   map[string]interface{}
}

// newDecisionServer starts a fake decisions API answering every request with
// status and body, and returns a provider pointed at it, what it saw, and the
// number of requests it received.
func newDecisionServer(t *testing.T, status int, body string) (*GeminiProvider, *decisionCapture, *atomic.Int32) {
	t.Helper()
	seen := &decisionCapture{}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		seen.path = r.URL.Path
		seen.apiKey = r.Header.Get("x-goog-api-key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seen.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"},
	}, testNoopLogger{})
	return provider, seen, &calls
}

func decisionContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

var decisionTestKey = schemas.Key{Value: *schemas.NewSecretVar("test-key")}

// decisionTypeQuestions asks one question of each type the API serves.
func decisionTypeQuestions() []schemas.DecisionQuestion {
	return []schemas.DecisionQuestion{
		{
			Type:         schemas.DecisionTypePredicate,
			Name:         schemas.Ptr("is_defect"),
			Instructions: schemas.NewDecisionText("Is the customer reporting a product defect?"),
			Criteria: &schemas.DecisionCriteria{
				True:  schemas.NewDecisionText("The product itself is broken."),
				False: schemas.NewDecisionText("Preference or shipping complaint."),
			},
		},
		{
			Type:         schemas.DecisionTypeChoice,
			Name:         schemas.Ptr("department"),
			Instructions: schemas.NewDecisionText("Which department should handle this request?"),
			Choices: []schemas.DecisionChoice{
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Description: schemas.NewDecisionText("Payments and refunds.")},
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("other")}},
			},
		},
		{
			Type:         schemas.DecisionTypeScore,
			Name:         schemas.Ptr("severity"),
			Instructions: schemas.NewDecisionText("How severe is this issue?"),
			Levels: []schemas.DecisionLevel{
				{Label: "0", Description: schemas.NewDecisionText("Cosmetic")},
				{Label: "blocked", Description: schemas.NewDecisionText("No workaround")},
			},
		},
	}
}

const decisionTypesResponse = `{"answers":{` +
	`"is_defect":{"type":"noul","noul":0.03,"confidence":0.97},` +
	`"department":{"type":"choice","choice":"billing","probabilities":{"other":0.1,"billing":0.9},"confidence":0.9},` +
	`"severity":{"type":"score","score":1.2,"legend":{"0":"Cosmetic","1":"blocked: No workaround"},"probabilities":{"0":0.2,"1":0.8},"confidence":0.8}},` +
	`"usage":{"total_input_tokens":100,"input_tokens_by_modality":[{"modality":"text","tokens":100}],"total_output_tokens":0,"total_tokens":100}}`

// TestGeminiDecisionAllTypes pins the request and response mapping of noul,
// choice, and score questions.
func TestGeminiDecisionAllTypes(t *testing.T) {
	provider, seen, _ := newDecisionServer(t, http.StatusOK, decisionTypesResponse)
	input := "Customer returned the sweater because they preferred green."
	resp, bifrostErr := provider.Decision(decisionContext(), decisionTestKey, &schemas.BifrostDecisionRequest{
		Provider:  schemas.Gemini,
		Model:     "gemini-decision-1",
		Input:     schemas.DecisionInput{Text: &input},
		Questions: decisionTypeQuestions(),
	})
	require.Nil(t, bifrostErr)

	assert.Equal(t, "/v1beta/decisions", seen.path)
	assert.Equal(t, "test-key", seen.apiKey)
	assert.Equal(t, map[string]interface{}{
		"model": "gemini-decision-1",
		"input": input,
		"questions": map[string]interface{}{
			"is_defect": map[string]interface{}{
				"type":         "noul",
				"instructions": "Is the customer reporting a product defect?",
				"criteria": map[string]interface{}{
					"true_description":  "The product itself is broken.",
					"false_description": "Preference or shipping complaint.",
				},
			},
			"department": map[string]interface{}{
				"type":         "choice",
				"instructions": "Which department should handle this request?",
				"criteria":     map[string]interface{}{"billing": "Payments and refunds.", "other": ""},
			},
			"severity": map[string]interface{}{
				"type":         "score",
				"instructions": "How severe is this issue?",
				"criteria":     []interface{}{"Cosmetic", "blocked: No workaround"},
			},
		},
	}, seen.body)

	require.Len(t, resp.Answers, 3)
	noul := resp.Answers[0]
	assert.Equal(t, schemas.DecisionTypePredicate, noul.Type)
	assert.Equal(t, "is_defect", *noul.Name)
	assert.Equal(t, 0.03, *noul.Probability)
	assert.Equal(t, 0.97, *noul.Confidence)

	choice := resp.Answers[1]
	assert.Equal(t, schemas.DecisionTypeChoice, choice.Type)
	assert.Equal(t, "billing", *choice.Choice.Str)
	require.Len(t, choice.Probabilities, 2)
	assert.Equal(t, "billing", *choice.Probabilities[0].Value.Str)
	assert.Equal(t, 0.9, choice.Probabilities[0].Probability)
	assert.Equal(t, "other", *choice.Probabilities[1].Value.Str)

	score := resp.Answers[2]
	assert.Equal(t, schemas.DecisionTypeScore, score.Type)
	assert.Equal(t, 1.2, *score.Score)
	assert.Equal(t, map[string]any{"0": "Cosmetic", "1": "blocked: No workaround"}, score.Legend)
	require.Len(t, score.Probabilities, 2)
	assert.Equal(t, "blocked", *score.Probabilities[1].Label)
	assert.Equal(t, 0.8, score.Probabilities[1].Probability)

	require.NotNil(t, resp.Usage)
	assert.Equal(t, 100, resp.Usage.PromptTokens)
	assert.Equal(t, 0, resp.Usage.CompletionTokens)
	assert.Equal(t, 100, resp.Usage.TotalTokens)
	require.NotNil(t, resp.Usage.PromptTokensDetails)
	assert.Equal(t, 100, resp.Usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, "gemini-decision-1", resp.Model)
}

// TestGeminiDecisionPositionalKeys pins that a question name the API rejects
// as a key is sent under a positional key that collides with no other key,
// and that its answer comes back under the question's own name.
func TestGeminiDecisionPositionalKeys(t *testing.T) {
	long := "a_name_that_is_far_too_long_to_be_a_question_key_for_the_decisions_api"
	provider, seen, _ := newDecisionServer(t, http.StatusOK, `{"answers":{`+
		`"question_1_2":{"type":"noul","noul":0.9,"confidence":0.9},`+
		`"question_2":{"type":"noul","noul":0.2,"confidence":0.8},`+
		`"question_1":{"type":"noul","noul":0.5,"confidence":0.5},`+
		`"question_4":{"type":"noul","noul":0.7,"confidence":0.7}},`+
		`"usage":{"total_input_tokens":10,"total_output_tokens":0,"total_tokens":10}}`)
	input := "The mug arrived cracked."
	ask := func(name *string) schemas.DecisionQuestion {
		return schemas.DecisionQuestion{Type: schemas.DecisionTypePredicate, Name: name, Instructions: schemas.NewDecisionText("Is it damaged?")}
	}
	resp, bifrostErr := provider.Decision(decisionContext(), decisionTestKey, &schemas.BifrostDecisionRequest{
		Provider:  schemas.Gemini,
		Model:     "gemini-decision-1",
		Input:     schemas.DecisionInput{Text: &input},
		Questions: []schemas.DecisionQuestion{ask(schemas.Ptr("is it damaged?")), ask(nil), ask(schemas.Ptr("question_1")), ask(&long)},
	})
	require.Nil(t, bifrostErr)

	questions, ok := seen.body["questions"].(map[string]interface{})
	require.True(t, ok)
	keys := make([]string, 0, len(questions))
	for key := range questions {
		keys = append(keys, key)
	}
	assert.ElementsMatch(t, []string{"question_1_2", "question_2", "question_1", "question_4"}, keys)

	require.Len(t, resp.Answers, 4)
	assert.Equal(t, "is it damaged?", *resp.Answers[0].Name)
	assert.Equal(t, 0.9, *resp.Answers[0].Probability)
	assert.Nil(t, resp.Answers[1].Name)
	assert.Equal(t, 0.2, *resp.Answers[1].Probability)
	assert.Equal(t, "question_1", *resp.Answers[2].Name)
	assert.Equal(t, 0.5, *resp.Answers[2].Probability)
	assert.Equal(t, long, *resp.Answers[3].Name)
	assert.Equal(t, 0.7, *resp.Answers[3].Probability)
}

// TestGeminiDecisionMediaInput pins message input as content blocks: text and
// JSON parts as text, a base64 data URL as inline data with its media type and
// resolution, an image URL by URI with the type its extension states, and a
// newline block between messages.
func TestGeminiDecisionMediaInput(t *testing.T) {
	provider, seen, _ := newDecisionServer(t, http.StatusOK, `{"answers":{"visible_damage":{"type":"noul","noul":0.8,"confidence":0.8}},`+
		`"usage":{"total_input_tokens":300,"input_tokens_by_modality":[{"modality":"text","tokens":40},{"modality":"image","tokens":260}],"total_output_tokens":0,"total_tokens":300}}`)
	resp, bifrostErr := provider.Decision(decisionContext(), decisionTestKey, &schemas.BifrostDecisionRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-decision-1",
		Input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
			{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
				{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText("Customer photo for return #8821.")},
				{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/jpeg;base64,/9j/4AAQ"), Detail: schemas.Ptr("low")},
				{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("https://example.com/photos/box.PNG?sig=1")},
				{Type: schemas.DecisionInputPartTypeJSON, Data: schemas.DecisionData{Structured: map[string]interface{}{"tier": "Gold"}}},
			}}},
			{Role: "user", Content: schemas.DecisionInputContent{Text: schemas.Ptr("Please hurry.")}},
		}},
		Questions: []schemas.DecisionQuestion{{
			Type:         schemas.DecisionTypePredicate,
			Name:         schemas.Ptr("visible_damage"),
			Instructions: schemas.NewDecisionText("Does the photo show damage?"),
		}},
	})
	require.Nil(t, bifrostErr)

	assert.Equal(t, []interface{}{
		map[string]interface{}{"type": "text", "text": "Customer photo for return #8821."},
		map[string]interface{}{"type": "image", "data": "/9j/4AAQ", "mime_type": "image/jpeg", "resolution": "low"},
		map[string]interface{}{"type": "image", "uri": "https://example.com/photos/box.PNG?sig=1", "mime_type": "image/png"},
		map[string]interface{}{"type": "text", "text": `{"tier":"Gold"}`},
		map[string]interface{}{"type": "text", "text": "\n"},
		map[string]interface{}{"type": "text", "text": "Please hurry."},
	}, seen.body["input"])
	require.NotNil(t, resp.Usage.PromptTokensDetails)
	assert.Equal(t, 40, resp.Usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, 260, resp.Usage.PromptTokensDetails.ImageTokens)
	assert.Equal(t, 300, resp.Usage.PromptTokens)
	assert.Equal(t, 0, resp.Usage.CompletionTokens)
}

// TestGeminiDecisionErrors pins that a safety block keeps the API's message
// and string code with its 400, and that a regular Google error is parsed as
// usual.
func TestGeminiDecisionErrors(t *testing.T) {
	input := "some input"
	request := &schemas.BifrostDecisionRequest{
		Provider:  schemas.Gemini,
		Model:     "gemini-decision-1",
		Input:     schemas.DecisionInput{Text: &input},
		Questions: decisionTypeQuestions(),
	}

	t.Run("safety", func(t *testing.T) {
		provider, _, _ := newDecisionServer(t, http.StatusBadRequest,
			`{"error":{"code":"safety","message":"Request blocked due to safety violations (harmful content). Please modify your input and retry."}}`)
		_, bifrostErr := provider.Decision(decisionContext(), decisionTestKey, request)
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
		require.NotNil(t, bifrostErr.Error)
		assert.Equal(t, "Request blocked due to safety violations (harmful content). Please modify your input and retry.", bifrostErr.Error.Message)
		require.NotNil(t, bifrostErr.Error.Code)
		assert.Equal(t, "safety", *bifrostErr.Error.Code)
	})

	t.Run("google error", func(t *testing.T) {
		provider, _, _ := newDecisionServer(t, http.StatusBadRequest,
			`{"error":{"code":400,"message":"question key 'a b' must match [A-Za-z0-9_-]+","status":"INVALID_ARGUMENT"}}`)
		_, bifrostErr := provider.Decision(decisionContext(), decisionTestKey, request)
		require.NotNil(t, bifrostErr)
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
		assert.Equal(t, "question key 'a b' must match [A-Za-z0-9_-]+", bifrostErr.Error.Message)
		assert.Equal(t, "INVALID_ARGUMENT", *bifrostErr.Error.Type)
	})

	t.Run("answer of another type", func(t *testing.T) {
		provider, _, _ := newDecisionServer(t, http.StatusOK, `{"answers":{`+
			`"is_defect":{"type":"choice","choice":"x"},"department":{"type":"choice","choice":"billing"},"severity":{"type":"score","score":1}}}`)
		_, bifrostErr := provider.Decision(decisionContext(), decisionTestKey, request)
		require.NotNil(t, bifrostErr)
		assert.Contains(t, bifrostErr.Error.Message, `answered decision question "is_defect" as "choice"`)
	})
}

// TestGeminiDecisionRejectsUnsupported pins that a request the API cannot
// serve fails with a 400 before any call upstream.
func TestGeminiDecisionRejectsUnsupported(t *testing.T) {
	text := "evidence"
	textInput := schemas.DecisionInput{Text: &text}
	predicate := []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("q"), Instructions: schemas.NewDecisionText("Is it?")}}
	messages := func(parts ...schemas.DecisionInputPart) schemas.DecisionInput {
		return schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: parts}}}}
	}

	cases := []struct {
		name      string
		input     schemas.DecisionInput
		questions []schemas.DecisionQuestion
		message   string
	}{
		{
			name:      "box question",
			input:     textInput,
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypeBox, Name: schemas.Ptr("where"), Instructions: schemas.NewDecisionText("Where is the dent?")}},
			message:   `type "box", which Gemini decisions do not serve`,
		},
		{
			name:      "empty input",
			questions: predicate,
			message:   "require an input",
		},
		{
			name: "tool output item",
			input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{
				Type:   schemas.Ptr(schemas.DecisionInputItemTypeFunctionCallOutput),
				CallID: schemas.Ptr("call_1"),
				Output: &schemas.DecisionInputContent{Text: schemas.Ptr("ok")},
			}}},
			questions: predicate,
			message:   `not a "function_call_output" input item`,
		},
		{
			name:      "image by reference",
			input:     messages(schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("photo"), Data: schemas.DecisionData{File: []byte{0x89}}}),
			questions: predicate,
			message:   "image sent by reference",
		},
		{
			name:      "audio part",
			input:     messages(schemas.DecisionInputPart{Type: "input_audio"}),
			questions: predicate,
			message:   `not "input_audio"`,
		},
		{
			name:      "image URL without a type",
			input:     messages(schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("https://example.com/photo")}),
			questions: predicate,
			message:   "does not state by its file extension",
		},
		{
			name:  "image in instructions",
			input: textInput,
			questions: []schemas.DecisionQuestion{{
				Type: schemas.DecisionTypePredicate,
				Name: schemas.Ptr("q"),
				Instructions: &schemas.DecisionText{Structured: []interface{}{
					map[string]interface{}{"type": "input_text", "text": "Does this match?"},
					map[string]interface{}{"type": "input_image", "image_url": "data:image/png;base64,iVBO"},
				}},
			}},
			message: "instructions are text only",
		},
		{
			name:  "image in instructions after an unrecognized part",
			input: textInput,
			questions: []schemas.DecisionQuestion{{
				Type: schemas.DecisionTypePredicate,
				Name: schemas.Ptr("q"),
				Instructions: &schemas.DecisionText{Structured: []interface{}{
					map[string]interface{}{"note": "not a part"},
					map[string]interface{}{"type": "input_image", "image_url": "data:image/png;base64,iVBO"},
				}},
			}},
			message: "instructions are text only",
		},
		{
			name:      "choice without choices",
			input:     textInput,
			questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("q"), Instructions: schemas.NewDecisionText("Which?")}},
			message:   "requires choices",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, _, calls := newDecisionServer(t, http.StatusOK, `{}`)
			_, bifrostErr := provider.Decision(decisionContext(), decisionTestKey, &schemas.BifrostDecisionRequest{
				Provider:  schemas.Gemini,
				Model:     "gemini-decision-1",
				Input:     tc.input,
				Questions: tc.questions,
			})
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
			assert.Contains(t, bifrostErr.Error.Message, tc.message)
			assert.Zero(t, calls.Load())
		})
	}
}

// TestGeminiDecisionInstructionParts pins that instructions written as text and
// JSON parts are joined into the API's text instructions.
func TestGeminiDecisionInstructionParts(t *testing.T) {
	question, err := toGeminiDecisionQuestion(0, schemas.DecisionQuestion{
		Type: schemas.DecisionTypePredicate,
		Instructions: &schemas.DecisionText{Structured: []interface{}{
			map[string]interface{}{"type": "input_text", "text": "Is the tier in "},
			map[string]interface{}{"type": "input_json", "data": map[string]interface{}{"tiers": []interface{}{"Gold"}}},
			map[string]interface{}{"type": "input_text", "text": "?"},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, `Is the tier in {"tiers":["Gold"]}?`, question.Instructions)
}
