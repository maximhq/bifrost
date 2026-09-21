package bifrost

import (
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

// structuredDecisionRequest carries every allowed criteria type in every slot:
// string | object | array for noul keys and score levels, plus null for choice
// options - the same matrix the typesafe converter and the harness cases pin.
func structuredDecisionRequest() *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		State:    "Customer message: I was double charged and want a refund.",
		Questions: map[string]schemas.DecisionQuestion{
			"approve": {
				Kind:         schemas.DecisionKindNoul,
				Instructions: "Approve a billing review?",
				Criteria: map[string]any{
					"true":  map[string]any{"meaning": "clear billing error"},
					"false": []any{"no billing issue", "general inquiry"},
				},
			},
			"category": {
				Kind:         schemas.DecisionKindChoice,
				Instructions: "Pick the ticket category",
				Criteria: map[string]any{
					"billing": map[string]any{"rubric": "money issues"},
					"bug":     []any{"crash", "defect"},
					"support": "service questions",
					"other":   nil,
				},
			},
			"urgency": {
				Kind:         schemas.DecisionKindScore,
				Instructions: "How urgent?",
				Criteria: []any{
					"low",
					map[string]any{"level": "high", "examples": []any{"outage"}},
					[]any{"critical", "churn risk"},
				},
			},
		},
	}
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
	if resp.Answers["approve"].Kind != schemas.DecisionKindNoul || resp.Answers["approve"].Value.(float64) != 0.9 {
		t.Errorf("noul answer = %+v", resp.Answers["approve"])
	}
	if resp.Answers["category"].Value.(string) != "billing" {
		t.Errorf("choice answer = %+v", resp.Answers["category"])
	}
	urgency := resp.Answers["urgency"]
	// Score derives from the distribution: 0*0.1 + 1*0.1 + 2*0.8 = 1.7.
	if math.Abs(urgency.Value.(float64)-1.7) > 1e-9 {
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
	if resp.Answers["category"].Value.(string) != "support" {
		t.Errorf("choice answer = %+v", resp.Answers["category"])
	}
	if resp.Answers["urgency"].Legend["0"] != "low" {
		t.Errorf("string legend level lost: %+v", resp.Answers["urgency"].Legend)
	}
}

func TestEmulateDecisionRejectsMalformedCriteriaBeforeDispatch(t *testing.T) {
	// A choice question whose criteria is not a map fails schema building; the
	// request must 400 locally without ever reaching the provider.
	req := structuredDecisionRequest()
	question := req.Questions["category"]
	question.Criteria = []any{"not", "a", "map"}
	req.Questions["category"] = question

	provider := &decisionEmulationProvider{}
	var b Bifrost
	_, bifrostErr := b.emulateDecisionViaResponses(nil, provider, schemas.Key{}, req)
	if bifrostErr == nil || bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "choice criteria must be a map of options") {
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
