package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// developerRoleUnsupported reports whether the target model rejects role
// "developer", in which case developer messages are sent as system. The
// datasheet's supports_developer_role decides; without it only Qwen on Fireworks
// is converted, since its chat templates fail with "Unexpected message role".
// Resolved on the base provider so a custom provider built on one gets the same answer.
func developerRoleUnsupported(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) bool {
	provider = schemas.ResolveBaseProvider(ctx, provider)
	capModel := schemas.ResolveCanonicalModel(ctx, model)
	caps := schemas.ResolveModelCaps(provider, capModel)
	return !caps.SupportsDeveloperRole(defaultSupportsDeveloperRole(provider, capModel))
}

func defaultSupportsDeveloperRole(provider schemas.ModelProvider, model string) bool {
	return provider != schemas.Fireworks || !strings.Contains(strings.ToLower(model), "qwen")
}
