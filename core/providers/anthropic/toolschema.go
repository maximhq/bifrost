package anthropic

import (
	"errors"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// NormalizeAnthropicToolInputSchema converts root-level JSON Schema
// compositions into an object schema accepted by Anthropic, which accepts
// composition inside properties but rejects oneOf, anyOf, and allOf directly
// on a custom tool's input_schema. The caller-owned request is not mutated.
func NormalizeAnthropicToolInputSchema(input *schemas.ToolFunctionParameters) (*schemas.ToolFunctionParameters, error) {
	normalized, err := schemas.FlattenRootComposition(input)
	if errors.Is(err, schemas.ErrToolSchemaTooComplex) {
		return nil, providerUtils.InvalidRequestErrorf("anthropic %s", err.Error())
	}
	return normalized, err
}
