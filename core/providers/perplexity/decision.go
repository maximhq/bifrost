package perplexity

import (
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
// so they validate exactly as they do on Typesafe. An input with images is sent as
// plain-string text items and image_url items in their original order (see
// providerUtils.ExtractDecisionImages). The request is not modified.
func ToPerplexityDecisionRequest(request *schemas.BifrostDecisionRequest) (*typesafe.TypesafeDecisionRequest, error) {
	input, state, err := providerUtils.ExtractDecisionImages(request.Input)
	if err != nil {
		return nil, err
	}
	textOnly := *request
	textOnly.Input = input
	native, err := typesafe.ToTypesafeDecisionRequest(&textOnly)
	if err != nil {
		return nil, err
	}
	if state != nil {
		native.State = state
	}
	return native, nil
}

// ToBifrostDecisionResponse converts Perplexity's response, which is Typesafe's systemone
// response, into the normalized shape. The body carries no id, so the caller passes the
// one from the x-request-id header.
func ToBifrostDecisionResponse(resp *typesafe.TypesafeDecisionResponse, request *schemas.BifrostDecisionRequest, id string) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	bifrostResp, bifrostErr := typesafe.ToBifrostDecisionResponse(resp, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	bifrostResp.ID = id
	return bifrostResp, nil
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
