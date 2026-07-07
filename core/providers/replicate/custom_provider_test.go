package replicate

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomProviderTopLevelExtraParamsForwardedAutomatically(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIsCustomProvider, true)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	req := &schemas.BifrostVideoGenerationRequest{
		Provider: schemas.ModelProvider("custom-replicate"),
		Model:    "openai/sora-2-pro",
		Input:    &schemas.VideoGenerationInput{Prompt: "hello"},
		Params: &schemas.VideoGenerationParameters{
			ExtraParams: map[string]interface{}{
				"custom_parameter": "value",
			},
		},
	}

	wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		req,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToReplicateVideoGenerationInput(req)
		},
	)
	require.Nil(t, bifrostErr)
	assert.Equal(t, "value", providerUtils.GetJSONField(wireBody, "custom_parameter").String())
}
