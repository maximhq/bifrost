package sarvam

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// sarvamDecisionPath is where Sarvam serves decision models in TypeSafe's
// System One shape.
const sarvamDecisionPath = "/v2/systemone"

// ToSarvamDecisionRequest converts a normalized decision request into Sarvam's
// System One body: the input becomes the state, and each question is keyed by
// its name. Sarvam serves the same state + named-questions shape as TypeSafe,
// but reads text only and has no box questions, so an image sent by reference
// and a box question are refused locally rather than sent to fail upstream.
func ToSarvamDecisionRequest(request *schemas.BifrostDecisionRequest) (*typesafe.TypesafeDecisionRequest, error) {
	if request.Input.HasAssetParts() {
		return nil, providerUtils.InvalidRequestErrorf("decision input carries an image sent by reference, which Sarvam does not support")
	}
	if decisionInputEmpty(request.Input) {
		return nil, providerUtils.InvalidRequestErrorf("decision input is required")
	}
	for _, question := range request.Questions {
		if question.Type == schemas.DecisionTypeBox {
			return nil, providerUtils.InvalidRequestErrorf("Sarvam does not support box questions")
		}
	}
	return typesafe.ToTypesafeDecisionRequest(request)
}

// decisionInputEmpty reports whether the input carries nothing to decide on: no
// member set, or a text or messages member that is present but empty. Sarvam
// answers an empty state with a confident probability, so an empty input is
// refused locally instead of being sent.
func decisionInputEmpty(input schemas.DecisionInput) bool {
	if input.IsEmpty() {
		return true
	}
	if input.Text != nil && strings.TrimSpace(*input.Text) == "" {
		return true
	}
	return input.Messages != nil && len(input.Messages) == 0
}

// Decision answers a decision request on Sarvam's /v2/systemone. Sarvam bills
// input tokens only; the response's output_tokens is always 0.
func (provider *SarvamProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return typesafe.HandleTypesafeDecisionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, sarvamDecisionPath),
		request,
		ToSarvamDecisionRequest,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		openai.ParseOpenAIError,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		false,
	)
}
