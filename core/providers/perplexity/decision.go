package perplexity

import (
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// perplexityDecisionPath is where Perplexity serves its decider models.
const perplexityDecisionPath = "/v1/decisions"

// perplexityRequestIDHeader carries the decision's id, which the body omits.
const perplexityRequestIDHeader = "x-request-id"

// ToPerplexityDecisionRequest converts a normalized decision request into Perplexity's
// body, which is Typesafe's systemone request. Questions go through Typesafe's converter,
// so they validate exactly as they do on Typesafe, except that Perplexity also serves
// locate questions (v1.3 deciders). An input with images is sent as plain-string text
// items and image_url items in their original order (see
// providerUtils.ExtractDecisionImages). The request is not modified.
//
// A request with a locate question must carry its input as a plain text string, which
// is sent unchanged as the state: Perplexity answers with character offsets into that
// string, so the input is never reshaped. Images, messages, structured input, and
// web_grounding mode are rejected for such a request, as Perplexity documents.
func ToPerplexityDecisionRequest(request *schemas.BifrostDecisionRequest) (*typesafe.TypesafeDecisionRequest, error) {
	locate := hasLocateQuestion(request.Questions)
	if locate {
		if err := validateLocateRequest(request); err != nil {
			return nil, err
		}
	}
	input, state, err := providerUtils.ExtractDecisionImages(request.Input)
	if err != nil {
		return nil, err
	}
	textOnly := *request
	textOnly.Input = input
	native, err := typesafe.ToTypesafeDecisionRequestWithLocate(&textOnly)
	if err != nil {
		return nil, err
	}
	if state != nil {
		native.State = state
	}
	return native, nil
}

// hasLocateQuestion reports whether any question is a locate question.
func hasLocateQuestion(questions []schemas.DecisionQuestion) bool {
	for _, question := range questions {
		if question.Type == schemas.DecisionTypeLocate {
			return true
		}
	}
	return false
}

// validateLocateRequest enforces Perplexity's rules for a request with a locate
// question: the input is a plain text string (no images or other parts, no messages,
// no structured state), and the request does not ask for web_grounding mode.
func validateLocateRequest(request *schemas.BifrostDecisionRequest) error {
	switch {
	case request.Input.Text != nil:
	case request.Input.Messages != nil:
		if partType := request.Input.NonTextPartType(); partType == schemas.DecisionInputPartTypeImage {
			return providerUtils.InvalidRequestErrorf("locate questions cannot be answered on an input with images; send the text to search as a plain string input")
		}
		return providerUtils.InvalidRequestErrorf("locate questions need the input as a plain text string, which their span offsets index; this input is a list of messages")
	case request.Input.Structured != nil:
		return providerUtils.InvalidRequestErrorf("locate questions need the input as a plain text string, which their span offsets index; this input is structured JSON")
	default:
		return providerUtils.InvalidRequestErrorf("locate questions need the input as a plain text string, which their span offsets index; the input is null")
	}
	if mode, ok := request.ExtraParams["mode"]; ok && isWebGroundingMode(mode) {
		return providerUtils.InvalidRequestErrorf("locate questions cannot be combined with mode %q", "web_grounding")
	}
	return nil
}

// isWebGroundingMode reports whether a forwarded mode extension asks for
// web_grounding, whether it arrived as a Go string or as raw JSON.
func isWebGroundingMode(mode interface{}) bool {
	switch value := mode.(type) {
	case string:
		return value == "web_grounding"
	case json.RawMessage:
		var text string
		return json.Unmarshal(value, &text) == nil && text == "web_grounding"
	case []byte:
		var text string
		return json.Unmarshal(value, &text) == nil && text == "web_grounding"
	}
	return false
}

// decisionRequestID returns the x-request-id response header. Header names keep the
// casing the transport gave them, so the lookup ignores case.
func decisionRequestID(headers map[string]string) string {
	for name, value := range headers {
		if strings.EqualFold(name, perplexityRequestIDHeader) {
			return value
		}
	}
	return ""
}
