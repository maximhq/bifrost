package perplexity_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/providers/perplexity"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// trickyLocateInput exercises everything that must survive untouched: leading and
// trailing whitespace, CRLF, tabs, runs of spaces, a non-breaking space, combining and
// precomposed accents (not normalized), astral-plane emoji, quotes, and HTML-sensitive
// characters.
const trickyLocateInput = "  Admit Date: 11/02/2026   Discharge Date: 11/09/2026\r\n\tO2: 2L nasal cannula  \n" +
	"Pt: José / José \U0001F600 \"quoted\" <b>&amp;</b> Apixaban 5 mg PO BID\n\n"

func locateDecisionRequest(input schemas.DecisionInput, questions ...schemas.DecisionQuestion) *schemas.BifrostDecisionRequest {
	if len(questions) == 0 {
		questions = []schemas.DecisionQuestion{{Type: schemas.DecisionTypeLocate, Name: schemas.Ptr("value"), Instructions: schemas.NewDecisionText("The oxygen text.")}}
	}
	return &schemas.BifrostDecisionRequest{Provider: schemas.Perplexity, Model: "pplx-decider-v1.3", Input: input, Questions: questions}
}

// TestToPerplexityDecisionRequest_LocateKeepsStateByteForByte pins that a locate request
// sends the input string, unchanged, as the state Perplexity's offsets index.
func TestToPerplexityDecisionRequest_LocateKeepsStateByteForByte(t *testing.T) {
	native, err := perplexity.ToPerplexityDecisionRequest(locateDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr(trickyLocateInput)}))
	if err != nil {
		t.Fatalf("ToPerplexityDecisionRequest: %v", err)
	}
	body, err := providerUtils.MarshalProviderRequest(native)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		State     string                     `json:"state"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.State != trickyLocateInput {
		t.Errorf("state =\n%q\nwant\n%q", decoded.State, trickyLocateInput)
	}
	if got := string(decoded.Questions["value"]); got != `{"type":"locate","instructions":"The oxygen text."}` {
		t.Errorf("question = %s", got)
	}
}

func TestToPerplexityDecisionRequest_LocateRejections(t *testing.T) {
	image := schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{decisionUserMessage(decisionTextPart("O2: 2L"), decisionImagePart(testImageURL))}}
	textMessages := schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{decisionUserMessage(decisionTextPart("O2: 2L"))}}
	grounding := locateDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("https://example.com")})
	grounding.ExtraParams = map[string]interface{}{"mode": json.RawMessage(`"web_grounding"`)}
	groundingString := locateDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("https://example.com")})
	groundingString.ExtraParams = map[string]interface{}{"mode": "web_grounding"}
	for name, request := range map[string]*schemas.BifrostDecisionRequest{
		"images":                  locateDecisionRequest(image),
		"messages":                locateDecisionRequest(textMessages),
		"structured":              locateDecisionRequest(schemas.DecisionInput{Structured: map[string]any{"note": "O2: 2L"}}),
		"null":                    locateDecisionRequest(schemas.DecisionInput{}),
		"web_grounding raw":       grounding,
		"web_grounding as string": groundingString,
	} {
		_, err := perplexity.ToPerplexityDecisionRequest(request)
		bifrostErr, ok := providerUtils.AsBifrostBadRequestError(err)
		if !ok {
			t.Errorf("%s: err = %v, want a 400", name, err)
			continue
		}
		if !strings.Contains(bifrostErr.Error.Message, "locate") {
			t.Errorf("%s: message = %q", name, bifrostErr.Error.Message)
		}
	}
	if _, err := perplexity.ToPerplexityDecisionRequest(locateDecisionRequest(image)); err == nil || !strings.Contains(err.Error(), "images") {
		t.Errorf("images: err = %v, want it to name images", err)
	}
}

// TestToPerplexityDecisionRequest_ImagesStillWorkWithoutLocate pins that the locate
// rules apply only to requests that ask a locate question.
func TestToPerplexityDecisionRequest_ImagesStillWorkWithoutLocate(t *testing.T) {
	body := encodePerplexityDecision(t, newDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		decisionUserMessage(decisionTextPart("Look at this"), decisionImagePart(testImageURL)),
	}}))
	if !strings.Contains(body, `"image_url"`) {
		t.Errorf("body = %s", body)
	}
}

// TestDecision_LocateRoundTrip pins the full provider call: the state reaches Perplexity
// byte-for-byte, and the spans come back unchanged, including an empty list.
func TestDecision_LocateRoundTrip(t *testing.T) {
	var sentState string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(body, &decoded)
		sentState = decoded.State
		runes := []rune(decoded.State)
		target := []rune("2L nasal cannula")
		start := strings.Index(decoded.State, string(target))
		start = utf8.RuneCountInString(decoded.State[:start])
		end := start + len(target)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"model":"pplx-decider-v1.3","answers":{"value":{"type":"locate","spans":[{"text":%q,"start":%d,"end":%d,"score":1}]},"phone":{"type":"locate","spans":[]}},"usage":{"input_tokens":120,"output_tokens":3}}`,
			string(runes[start:end]), start, end)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := locateDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr(trickyLocateInput)},
		schemas.DecisionQuestion{Type: schemas.DecisionTypeLocate, Name: schemas.Ptr("value"), Instructions: schemas.NewDecisionText("oxygen")},
		schemas.DecisionQuestion{Type: schemas.DecisionTypeLocate, Name: schemas.Ptr("phone"), Instructions: schemas.NewDecisionText("a phone number")},
	)
	resp, bifrostErr := provider.Decision(ctx, newTestKey(), request)
	if bifrostErr != nil {
		t.Fatalf("Decision: %v", bifrostErr.Error.Message)
	}
	if sentState != trickyLocateInput {
		t.Fatalf("sent state =\n%q\nwant\n%q", sentState, trickyLocateInput)
	}
	spans := resp.Answers[0].Spans
	if spans == nil || len(*spans) != 1 {
		t.Fatalf("value spans = %v", spans)
	}
	span := (*spans)[0]
	if got := string([]rune(trickyLocateInput)[span.Start:span.End]); got != span.Text || span.Text != "2L nasal cannula" {
		t.Errorf("input[%d:%d] = %q, span text %q", span.Start, span.End, got, span.Text)
	}
	if span.Score == nil || *span.Score != 1 {
		t.Errorf("score = %v", span.Score)
	}
	if resp.Answers[1].Spans == nil || len(*resp.Answers[1].Spans) != 0 {
		t.Errorf("phone spans = %v, want []", resp.Answers[1].Spans)
	}
}
