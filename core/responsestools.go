package bifrost

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// hoistResponsesAdditionalTools promotes embedded client declarations before wire conversion without mutating the request shared by fallback attempts.
func hoistResponsesAdditionalTools(request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesRequest, *schemas.BifrostError) {
	if request == nil {
		return nil, nil
	}
	var tools []schemas.ResponsesTool
	found := false
	for _, message := range request.Input {
		if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeAdditionalTools {
			continue
		}
		found = true
		var declared []schemas.ResponsesTool
		if err := schemas.Unmarshal(message.AdditionalTools, &declared); err != nil {
			return nil, providerUtils.NewBifrostBadRequestError("invalid tools in additional_tools input item")
		}
		tools = append(tools, declared...)
	}
	if !found {
		return request, nil
	}

	prepared := *request
	params := schemas.ResponsesParameters{}
	if request.Params != nil {
		params = *request.Params
	}
	merged := make([]schemas.ResponsesTool, 0, len(params.Tools)+len(tools))
	merged = append(merged, params.Tools...)
	params.Tools = append(merged, tools...)
	prepared.Params = &params
	prepared.Input = make([]schemas.ResponsesMessage, 0, len(request.Input))
	for _, message := range request.Input {
		if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeAdditionalTools {
			prepared.Input = append(prepared.Input, message)
		}
	}
	return &prepared, nil
}
