package bifrost

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// decisionEmulationProvider satisfies schemas.Provider for the emulation seam;
// only Responses is implemented. It records the outbound request so tests can
// assert what actually reaches the emulating model, and returns a canned
// response.
type decisionEmulationProvider struct {
	schemas.Provider
	lastRequest *schemas.BifrostResponsesRequest
	response    *schemas.BifrostResponsesResponse
	err         *schemas.BifrostError
}

func (p *decisionEmulationProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	p.lastRequest = req
	return p.response, p.err
}

// emulationFunctionCallResponse wraps emit_decision arguments in the Responses
// function-call output shape the emulation path parses.
func emulationFunctionCallResponse(args string) *schemas.BifrostResponsesResponse {
	fnType := schemas.ResponsesMessageTypeFunctionCall
	name := "emit_decision"
	return &schemas.BifrostResponsesResponse{
		Model: "gpt-4o-mini",
		Output: []schemas.ResponsesMessage{{
			Type:                 &fnType,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: &name, Arguments: &args},
		}},
	}
}

// structuredDecisionRequest carries every allowed description type in every
// slot: string | object | array for predicate outcomes and score levels, plus
// null for a choice option - the same matrix the typesafe converter and the
// harness cases pin.
func structuredDecisionRequest() *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input:    schemas.DecisionInput{Text: schemas.Ptr("Customer message: I was double charged and want a refund.")},
		Questions: []schemas.DecisionQuestion{
			{
				Type:         schemas.DecisionTypePredicate,
				Name:         schemas.Ptr("approve"),
				Instructions: schemas.NewDecisionText("Approve a billing review?"),
				Criteria: &schemas.DecisionCriteria{
					True:  &schemas.DecisionText{Structured: map[string]any{"meaning": "clear billing error"}},
					False: &schemas.DecisionText{Structured: []any{"no billing issue", "general inquiry"}},
				},
			},
			{
				Type:         schemas.DecisionTypeChoice,
				Name:         schemas.Ptr("category"),
				Instructions: schemas.NewDecisionText("Pick the ticket category"),
				Choices: []schemas.DecisionChoice{
					{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Description: &schemas.DecisionText{Structured: map[string]any{"rubric": "money issues"}}},
					{Value: schemas.DecisionScalar{Str: schemas.Ptr("bug")}, Description: &schemas.DecisionText{Structured: []any{"crash", "defect"}}},
					{Value: schemas.DecisionScalar{Str: schemas.Ptr("other")}},
					{Value: schemas.DecisionScalar{Str: schemas.Ptr("support")}, Description: schemas.NewDecisionText("service questions")},
				},
			},
			{
				Type:         schemas.DecisionTypeScore,
				Name:         schemas.Ptr("urgency"),
				Instructions: schemas.NewDecisionText("How urgent?"),
				Levels: []schemas.DecisionLevel{
					{Label: "0", Description: schemas.NewDecisionText("low")},
					{Label: "1", Description: &schemas.DecisionText{Structured: map[string]any{"level": "high", "examples": []any{"outage"}}}},
					{Label: "2", Description: &schemas.DecisionText{Structured: []any{"critical", "churn risk"}}},
				},
			},
		},
	}
}

// decisionAnswerNamed returns the answer to the named question.
func decisionAnswerNamed(t *testing.T, resp *schemas.BifrostDecisionResponse, name string) schemas.DecisionAnswer {
	t.Helper()
	for _, answer := range resp.Answers {
		if answer.Name != nil && *answer.Name == name {
			return answer
		}
	}
	t.Fatalf("no answer named %q in %+v", name, resp.Answers)
	return schemas.DecisionAnswer{}
}

// questionDescription walks the emit_decision tool schema down to the value
// description of one question property.
func questionDescription(t *testing.T, req *schemas.BifrostResponsesRequest, question, field string) string {
	t.Helper()
	if req == nil || req.Params == nil || len(req.Params.Tools) != 1 {
		t.Fatalf("expected exactly one tool on the outbound request: %+v", req)
	}
	tool := req.Params.Tools[0]
	if tool.ResponsesToolFunction == nil || tool.ResponsesToolFunction.Parameters == nil {
		t.Fatalf("tool carries no parameters: %+v", tool)
	}
	prop, ok := tool.ResponsesToolFunction.Parameters.Properties.Get(question)
	if !ok {
		t.Fatalf("question %q missing from tool schema", question)
	}
	nested, ok := prop.(map[string]any)["properties"].(map[string]any)[field].(map[string]any)
	if !ok {
		t.Fatalf("question %q has no %q property", question, field)
	}
	desc, _ := nested["description"].(string)
	return desc
}

func TestEmulateDecisionForwardsStructuredCriteria(t *testing.T) {
	args := `{
		"approve":  {"value": 0.9, "confidence": 0.8},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 0.7, "bug": 0.1, "support": 0.1, "other": 0.1}},
		"urgency":  {"value": 2, "confidence": 0.6, "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}}
	}`
	provider := &decisionEmulationProvider{response: emulationFunctionCallResponse(args)}

	var b Bifrost
	resp, bifrostErr := b.emulateDecisionViaResponses(nil, provider, schemas.Key{}, structuredDecisionRequest())
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}

	// Outbound: every criteria rubric must reach the model's tool schema,
	// strings verbatim and structured values as sorted JSON.
	noulDesc := questionDescription(t, provider.lastRequest, "approve", "value")
	for _, want := range []string{`true={"meaning":"clear billing error"}`, `false=["no billing issue","general inquiry"]`} {
		if !strings.Contains(noulDesc, want) {
			t.Errorf("noul description missing %q: %q", want, noulDesc)
		}
	}
	choiceDesc := questionDescription(t, provider.lastRequest, "category", "choice")
	for _, want := range []string{`{"rubric":"money issues"}`, `["crash","defect"]`, "service questions"} {
		if !strings.Contains(choiceDesc, want) {
			t.Errorf("choice description missing %q: %q", want, choiceDesc)
		}
	}
	scoreDesc := questionDescription(t, provider.lastRequest, "urgency", "value")
	for _, want := range []string{"0=low", `{"examples":["outage"],"level":"high"}`, `["critical","churn risk"]`} {
		if !strings.Contains(scoreDesc, want) {
			t.Errorf("score description missing %q: %q", want, scoreDesc)
		}
	}
	for _, desc := range []string{noulDesc, choiceDesc, scoreDesc} {
		if strings.Contains(desc, "map[") {
			t.Errorf("description leaks Go map syntax: %q", desc)
		}
	}

	// Outbound framing: forced tool call, system prompt, state verbatim.
	if provider.lastRequest.Params.ToolChoice == nil || provider.lastRequest.Params.ToolChoice.ResponsesToolChoiceStr == nil ||
		*provider.lastRequest.Params.ToolChoice.ResponsesToolChoiceStr != string(schemas.ResponsesToolChoiceTypeRequired) {
		t.Errorf("tool choice not forced to required: %+v", provider.lastRequest.Params.ToolChoice)
	}
	if provider.lastRequest.Params.Instructions == nil || *provider.lastRequest.Params.Instructions != decisionSystemPrompt {
		t.Errorf("system prompt not attached: %+v", provider.lastRequest.Params.Instructions)
	}
	if len(provider.lastRequest.Input) != 1 || provider.lastRequest.Input[0].Content.ContentStr == nil ||
		*provider.lastRequest.Input[0].Content.ContentStr != "Customer message: I was double charged and want a refund." {
		t.Errorf("state not forwarded verbatim: %+v", provider.lastRequest.Input)
	}

	// Inbound: answers typed per kind, structured legend rendered as JSON.
	if approve := decisionAnswerNamed(t, resp, "approve"); approve.Type != schemas.DecisionTypePredicate || *approve.Probability != 0.9 {
		t.Errorf("noul answer = %+v", approve)
	}
	if category := decisionAnswerNamed(t, resp, "category"); *category.Choice.Str != "billing" {
		t.Errorf("choice answer = %+v", category)
	}
	urgency := decisionAnswerNamed(t, resp, "urgency")
	// Score derives from the distribution: 0*0.1 + 1*0.1 + 2*0.8 = 1.7.
	if math.Abs(*urgency.Score-1.7) > 1e-9 {
		t.Errorf("score answer = %+v", urgency)
	}
	// The legend echoes each level's description verbatim, matching the native
	// API's shape (values are strings, objects, or arrays - not stringified).
	if !reflect.DeepEqual(urgency.Legend["1"], map[string]any{"level": "high", "examples": []any{"outage"}}) ||
		!reflect.DeepEqual(urgency.Legend["2"], []any{"critical", "churn risk"}) {
		t.Errorf("structured legend not echoed verbatim: %+v", urgency.Legend)
	}
}

func TestEmulateDecisionStructuredOutputFallbackPath(t *testing.T) {
	// A model that ignores the function tool but answers with a JSON object as
	// output text takes the structured-output fallback in
	// extractDecisionToolArguments; structured criteria must survive that path
	// identically.
	msgType := schemas.ResponsesMessageTypeMessage
	content := `{
		"approve":  {"value": 0.4, "confidence": 0.5},
		"category": {"choice": "support", "confidence": 0.6, "probabilities": {"billing": 0.2, "bug": 0.1, "support": 0.6, "other": 0.1}},
		"urgency":  {"value": 0, "confidence": 0.7, "probabilities": {"0": 0.8, "1": 0.1, "2": 0.1}}
	}`
	provider := &decisionEmulationProvider{response: &schemas.BifrostResponsesResponse{
		Model: "gpt-4o-mini",
		Output: []schemas.ResponsesMessage{{
			Type:    &msgType,
			Content: &schemas.ResponsesMessageContent{ContentStr: &content},
		}},
	}}

	var b Bifrost
	resp, bifrostErr := b.emulateDecisionViaResponses(nil, provider, schemas.Key{}, structuredDecisionRequest())
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if category := decisionAnswerNamed(t, resp, "category"); *category.Choice.Str != "support" {
		t.Errorf("choice answer = %+v", category)
	}
	if urgency := decisionAnswerNamed(t, resp, "urgency"); urgency.Legend["0"] != "low" {
		t.Errorf("string legend level lost: %+v", urgency.Legend)
	}
}

func TestEmulateDecisionRejectsMalformedCriteriaBeforeDispatch(t *testing.T) {
	// A choice question without choices fails schema building; the request
	// must 400 locally without ever reaching the provider.
	req := structuredDecisionRequest()
	req.Questions[1].Choices = nil

	provider := &decisionEmulationProvider{}
	var b Bifrost
	_, bifrostErr := b.emulateDecisionViaResponses(nil, provider, schemas.Key{}, req)
	if bifrostErr == nil || bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "at least one option") {
		t.Fatalf("expected local criteria rejection, got %+v", bifrostErr)
	}
	if provider.lastRequest != nil {
		t.Error("malformed criteria must be rejected before the provider is called")
	}
}

func TestEmulateDecisionRejectsAnswerOutsideStructuredOptions(t *testing.T) {
	// The model answers with an option that is not in the structured criteria
	// map; the emulation must reject it so fallbacks can proceed, not fabricate
	// a valid-looking decision.
	// Every other field is complete so the only defect is the unknown option.
	args := `{
		"approve":  {"value": 0.9, "confidence": 0.8},
		"category": {"choice": "nonexistent", "confidence": 0.7, "probabilities": {"billing": 0.7, "bug": 0.1, "support": 0.1, "other": 0.1}},
		"urgency":  {"value": 1, "confidence": 0.6, "probabilities": {"0": 0.1, "1": 0.8, "2": 0.1}}
	}`
	provider := &decisionEmulationProvider{response: emulationFunctionCallResponse(args)}
	var b Bifrost
	_, bifrostErr := b.emulateDecisionViaResponses(nil, provider, schemas.Key{}, structuredDecisionRequest())
	if bifrostErr == nil || bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "not an allowed option") {
		t.Fatalf("expected out-of-options rejection, got %+v", bifrostErr)
	}
}

// Bedrock Mantle's OpenAI-compatible surface rejects tool_choice "required" for
// gpt-oss ("Supported options: [auto]"), so emulation there must send "auto";
// every other surface, Claude on Mantle included, keeps the forced call.
func TestEmulateDecisionToolChoicePerSurface(t *testing.T) {
	args := `{
		"approve":  {"value": 0.9, "confidence": 0.8},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 0.7, "bug": 0.1, "support": 0.1, "other": 0.1}},
		"urgency":  {"value": 2, "confidence": 0.6, "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}}
	}`
	cases := []struct {
		provider schemas.ModelProvider
		model    string
		want     schemas.ResponsesToolChoiceType
	}{
		{schemas.BedrockMantle, "openai.gpt-oss-120b", schemas.ResponsesToolChoiceTypeAuto},
		{schemas.BedrockMantle, "openai.gpt-oss-20b", schemas.ResponsesToolChoiceTypeAuto},
		{schemas.BedrockMantle, "anthropic.claude-opus-4-8", schemas.ResponsesToolChoiceTypeRequired},
		{schemas.OpenAI, "gpt-4o-mini", schemas.ResponsesToolChoiceTypeRequired},
	}
	for _, tc := range cases {
		t.Run(string(tc.provider)+"/"+tc.model, func(t *testing.T) {
			provider := &decisionEmulationProvider{response: emulationFunctionCallResponse(args)}
			req := structuredDecisionRequest()
			req.Provider, req.Model = tc.provider, tc.model

			var b Bifrost
			if _, bifrostErr := b.emulateDecisionViaResponses(nil, provider, schemas.Key{}, req); bifrostErr != nil {
				t.Fatalf("unexpected error: %v", bifrostErr)
			}
			choice := provider.lastRequest.Params.ToolChoice
			if choice == nil || choice.ResponsesToolChoiceStr == nil || *choice.ResponsesToolChoiceStr != string(tc.want) {
				t.Errorf("tool_choice = %+v, want %q", choice, tc.want)
			}
		})
	}
}

// TestEmulateDecisionRefusesPassthroughExtensions pins #7599's "never text-only"
// rule: when native extensions (e.g. images) were asked to reach the wire, an
// emulating chat model refuses instead of silently answering without them.
func TestEmulateDecisionRefusesPassthroughExtensions(t *testing.T) {
	provider := &decisionEmulationProvider{response: emulationFunctionCallResponse(`{"approve": {"value": 0.9, "confidence": 0.8}}`)}
	req := &schemas.BifrostDecisionRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		Input:       schemas.DecisionInput{Text: schemas.Ptr("Describe this photo.")},
		Questions:   []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("approve"), Instructions: schemas.NewDecisionText("Approve?")}},
		ExtraParams: map[string]interface{}{"images": []string{"data:image/png;base64,iVBORw0KGgo="}},
	}

	var b Bifrost
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	_, bifrostErr := b.emulateDecisionViaResponses(ctx, provider, schemas.Key{}, req)
	if bifrostErr == nil {
		t.Fatal("expected refusal when extensions must reach the wire")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 || !strings.Contains(bifrostErr.Error.Message, "images") {
		t.Errorf("unexpected refusal: %+v", bifrostErr)
	}
	if provider.lastRequest != nil {
		t.Error("the emulating model must not be called")
	}

	// Without the passthrough flag the extras were never promised to the wire;
	// emulation proceeds as before.
	plain := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := b.emulateDecisionViaResponses(plain, provider, schemas.Key{}, req); bifrostErr != nil {
		t.Fatalf("unexpected error without passthrough: %v", bifrostErr)
	}
}

// TestEmulateDecisionListOnlyDetails pins that emulation honours what only a
// list-shaped request carries: an unnamed question is asked under a generated
// name and answered without one, a boolean choice is answered with its
// boolean, and a level label reaches the model with its level.
func TestEmulateDecisionListOnlyDetails(t *testing.T) {
	args := `{
		"question_1": {"value": 0.2, "confidence": 0.9},
		"refund":     {"choice": "true", "confidence": 0.8, "probabilities": {"true": 0.8, "false": 0.2}},
		"severity":   {"value": 1, "confidence": 0.7, "probabilities": {"0": 0.1, "1": 0.9}}
	}`
	provider := &decisionEmulationProvider{response: emulationFunctionCallResponse(args)}
	req := &schemas.BifrostDecisionRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input:    schemas.DecisionInput{Text: schemas.Ptr("Customer message: please refund me.")},
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Instructions: schemas.NewDecisionText("Is the customer angry?")},
			{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("refund"), Instructions: schemas.NewDecisionText("Refund?"), Choices: []schemas.DecisionChoice{
				{Value: schemas.DecisionScalar{Bool: schemas.Ptr(true)}, Description: schemas.NewDecisionText("refund now")},
				{Value: schemas.DecisionScalar{Bool: schemas.Ptr(false)}},
			}},
			{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("severity"), Instructions: schemas.NewDecisionText("How bad?"), Levels: []schemas.DecisionLevel{
				{Label: "Cosmetic"}, {Label: "Blocked", Description: schemas.NewDecisionText("nothing works")},
			}},
		},
	}

	var b Bifrost
	resp, bifrostErr := b.emulateDecisionViaResponses(nil, provider, schemas.Key{}, req)
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if desc := questionDescription(t, provider.lastRequest, "severity", "value"); !strings.Contains(desc, "0=Cosmetic") || !strings.Contains(desc, "1=Blocked: nothing works") {
		t.Errorf("level labels did not reach the model: %q", desc)
	}
	if len(resp.Answers) != 3 {
		t.Fatalf("answers = %+v", resp.Answers)
	}
	if first := resp.Answers[0]; first.Name != nil || first.Probability == nil || *first.Probability != 0.2 {
		t.Errorf("unnamed answer = %+v", first)
	}
	if refund := resp.Answers[1]; refund.Choice == nil || refund.Choice.Bool == nil || !*refund.Choice.Bool || *refund.Probabilities[0].Value.Bool != true {
		t.Errorf("boolean choice answer = %+v", refund)
	}
	if severity := resp.Answers[2]; severity.Probabilities[1].Label == nil || *severity.Probabilities[1].Label != "Blocked" {
		t.Errorf("score answer = %+v", severity)
	}
}

// TestEmulateDecisionRejectsNonTextInput pins that an image, or a part of a
// type Bifrost does not model such as audio, is refused before the model is
// called, since emulation sends the input as text and the model would answer
// without reading it. The audio part is synthetic.
func TestEmulateDecisionRejectsNonTextInput(t *testing.T) {
	for partType, input := range map[string]string{
		"input_image": `[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]`,
		"input_audio": `[{"role":"user","content":[{"type":"input_text","text":"listen"},{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]`,
		"untyped":     `[{"role":"user","content":[{"image_url":"data:image/png;base64,AAAA"}]}]`,
	} {
		provider := &decisionEmulationProvider{}
		req := &schemas.BifrostDecisionRequest{
			Provider:  schemas.OpenAI,
			Model:     "gpt-4o-mini",
			Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("cat"), Instructions: schemas.NewDecisionText("Is this a cat?")}},
		}
		if err := schemas.Unmarshal([]byte(input), &req.Input); err != nil {
			t.Fatalf("decode %s: %v", partType, err)
		}

		var b Bifrost
		_, bifrostErr := b.emulateDecisionViaResponses(nil, provider, schemas.Key{}, req)
		if bifrostErr == nil || bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, partType) {
			t.Errorf("expected %s rejection, got %+v", partType, bifrostErr)
		}
		if provider.lastRequest != nil {
			t.Errorf("the emulating model must not be called for %s", partType)
		}
	}
}
