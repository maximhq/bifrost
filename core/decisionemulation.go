package bifrost

import (
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// errDecisionNoContent is returned when an emulating response carries neither the
// emit_decision function call nor a JSON-object message body.
var errDecisionNoContent = errors.New("model returned no decision function call or structured output")

// isUnsupportedOperation reports whether an error is a provider's
// "operation not supported" signal (set by NewUnsupportedOperationError).
func isUnsupportedOperation(err *schemas.BifrostError) bool {
	return err != nil && err.Error != nil && err.Error.Code != nil && *err.Error.Code == "unsupported_operation"
}

// decisionSystemPrompt frames the judgment task for an emulating LLM.
const decisionSystemPrompt = "You are a judgment engine. Read the given state and answer every question by " +
	"calling the provided function exactly once. For each question emit the requested value and your " +
	"confidence from 0 to 1. For every choice and score question also report the full probability " +
	"distribution over its options or levels; the probabilities must sum to 1. For choice, select an option " +
	"with the highest probability. Base every answer only on the state; do not invent facts."

// emulateDecisionViaResponses answers a decision request through a general model
// when the provider has no native decision support. It runs against the provider's
// own Responses API - the richest interface every provider implements (natively on
// openai/anthropic/gemini, via chat translation elsewhere) - encoding the questions
// as a forced function tool and mapping the function-call arguments back to the
// neutral DecisionResponse shape. Used for both the primary path (an LLM named as
// the decision model) and fallbacks (an LLM after the native provider fails) - both
// flow through the same dispatch case.
func (bifrost *Bifrost) emulateDecisionViaResponses(
	ctx *schemas.BifrostContext,
	provider schemas.Provider,
	key schemas.Key,
	req *schemas.BifrostDecisionRequest,
) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	if req == nil || len(req.Questions) == 0 {
		return nil, providerUtils.NewBifrostBadRequestError("decision request requires at least one question")
	}

	tool, err := providerUtils.BuildDecisionResponsesTool(req.Questions)
	if err != nil {
		return nil, providerUtils.NewBifrostBadRequestError(err.Error())
	}

	// State as the user message: string verbatim, structured as sorted JSON.
	stateText, marshalErr := decisionStateText(req.State)
	if marshalErr != nil {
		return nil, providerUtils.NewBifrostBadRequestError("decision state could not be serialized: " + marshalErr.Error())
	}

	instructions := decisionSystemPrompt
	userRole := schemas.ResponsesInputMessageRoleUser
	toolChoice := decisionToolChoice(ctx, req.Provider, req.Model)
	responsesReq := &schemas.BifrostResponsesRequest{
		Provider: req.Provider,
		Model:    req.Model,
		Input: []schemas.ResponsesMessage{
			{
				Role:    &userRole,
				Content: &schemas.ResponsesMessageContent{ContentStr: &stateText},
			},
		},
		Params: &schemas.ResponsesParameters{
			Instructions: &instructions,
			Tools:        []schemas.ResponsesTool{*tool},
			ToolChoice:   &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: &toolChoice},
		},
	}

	resp, respErr := provider.Responses(ctx, key, responsesReq)
	if respErr != nil {
		return nil, respErr
	}

	argsJSON, extractErr := extractDecisionToolArguments(resp)
	if extractErr != nil {
		return nil, providerUtils.NewBifrostOperationError(extractErr.Error(), nil)
	}

	answers, parseErr := providerUtils.ParseDecisionAnswers([]byte(argsJSON), req.Questions)
	if parseErr != nil {
		return nil, providerUtils.NewBifrostOperationError(parseErr.Error(), nil)
	}

	decision := &schemas.BifrostDecisionResponse{
		Model:   modelForDecisionResponse(resp, req),
		Answers: answers,
	}
	// Carry the underlying response's id and resolved-model/latency metadata so
	// downstream pricing and integrations see the model that actually handled the
	// turn, not just the requested one. RequestType is deliberately not copied -
	// post-hooks run before final normalization.
	if resp != nil {
		if resp.ID != nil {
			decision.ID = *resp.ID
		}
		decision.ExtraFields.ResolvedModelUsed = resp.Model
		decision.ExtraFields.Latency = resp.ExtraFields.Latency
		if resp.Usage != nil {
			decision.Usage = resp.Usage.ToBifrostLLMUsage()
		}
	}
	// Provider/Model on ExtraFields drive downstream cost calculation.
	decision.ExtraFields.Provider = req.Provider
	decision.ExtraFields.OriginalModelRequested = req.Model
	return decision, nil
}

// decisionToolChoice picks the tool_choice that forces the emit_decision call.
// It prefers the "required" mode over a named-function choice: emit_decision is
// the only tool, so "required" obliges the model to call it, and the string mode
// is accepted by OpenAI, Anthropic, and OpenAI-compatible providers like
// Perplexity that reject the named-function object. Bedrock Mantle's
// OpenAI-compatible surface is the exception: it accepts only "auto" for gpt-oss
// ("Supported options: [auto]"), so the call there rests on the system prompt,
// and extraction already accepts a lone call or a JSON text body.
func decisionToolChoice(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) string {
	if provider == schemas.BedrockMantle && !schemas.IsAnthropicModelFamily(ctx, model) {
		return string(schemas.ResponsesToolChoiceTypeAuto)
	}
	return string(schemas.ResponsesToolChoiceTypeRequired)
}

// decisionStateText renders the state into a message body.
func decisionStateText(state interface{}) (string, error) {
	if s, ok := state.(string); ok {
		return s, nil
	}
	raw, err := providerUtils.MarshalSorted(state)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// modelForDecisionResponse prefers the model the response reported (the resolved
// model after any provider-side handoff), falling back to the request.
func modelForDecisionResponse(resp *schemas.BifrostResponsesResponse, req *schemas.BifrostDecisionRequest) string {
	if resp != nil && resp.Model != "" {
		return resp.Model
	}
	return req.Model
}

// extractDecisionToolArguments pulls the emit_decision function-call arguments from
// the Responses output items. Falls back to a lone function call, then to a plain
// output-text message for models that answered via native structured output.
func extractDecisionToolArguments(resp *schemas.BifrostResponsesResponse) (string, error) {
	if resp == nil || len(resp.Output) == 0 {
		return "", errDecisionNoContent
	}

	// Collect every function call, counting emit_decision calls separately, before
	// returning: a duplicate or ambiguous set must be rejected rather than silently
	// taking the first. Providers may drop MaxToolCalls/ParallelToolCalls on the
	// Responses-to-Chat fallback, so extraction cannot assume a single call.
	var namedCalls []string
	var allCalls []string
	for i := range resp.Output {
		item := resp.Output[i]
		if item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCall {
			continue
		}
		if item.ResponsesToolMessage == nil || item.ResponsesToolMessage.Arguments == nil {
			continue
		}
		allCalls = append(allCalls, *item.ResponsesToolMessage.Arguments)
		if item.ResponsesToolMessage.Name != nil && *item.ResponsesToolMessage.Name == providerUtils.DecisionToolName {
			namedCalls = append(namedCalls, *item.ResponsesToolMessage.Arguments)
		}
	}
	switch {
	case len(namedCalls) > 1:
		return "", fmt.Errorf("model returned %d %s calls; expected exactly one", len(namedCalls), providerUtils.DecisionToolName)
	case len(namedCalls) == 1:
		return namedCalls[0], nil
	case len(allCalls) > 1:
		// No call round-tripped the name, and there is more than one - ambiguous.
		return "", fmt.Errorf("model returned %d tool calls with no unambiguous %s call", len(allCalls), providerUtils.DecisionToolName)
	case len(allCalls) == 1:
		// A single function call is acceptable even if the name did not round-trip.
		return allCalls[0], nil
	}

	// Native structured-output path: the JSON object arrives as output text.
	for i := range resp.Output {
		item := resp.Output[i]
		if item.Type != nil && *item.Type != schemas.ResponsesMessageTypeMessage {
			continue
		}
		if item.Content == nil {
			continue
		}
		if item.Content.ContentStr != nil && isJSONObject(*item.Content.ContentStr) {
			return *item.Content.ContentStr, nil
		}
		for _, block := range item.Content.ContentBlocks {
			if block.Text != nil && isJSONObject(*block.Text) {
				return *block.Text, nil
			}
		}
	}
	return "", errDecisionNoContent
}

// isJSONObject reports whether s parses as a JSON object.
func isJSONObject(s string) bool {
	var obj map[string]interface{}
	return sonic.Unmarshal([]byte(s), &obj) == nil
}
