package handlers

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

var usageNullJSONSuffix = []byte(`,"usage":null}`)

// marshalChatCompletionStreamEvents converts an internal chat stream chunk
// into the OpenAI Chat Completions wire contract. Internal chunks may combine
// usage with a terminal choice. On the wire, usage is omitted unless requested
// and requested usage is sent as a separate final chunk with no choices.
//
// The common path performs one marshal and no extra allocation beyond the JSON
// output. The second marshal exists only for the single terminal chunk of an
// include_usage stream.
func marshalChatCompletionStreamEvents(response *schemas.BifrostChatResponse, includeUsage bool) (primaryJSON, usageJSON []byte, err error) {
	if response == nil {
		return nil, nil, fmt.Errorf("chat completion stream response is nil")
	}

	usage := response.Usage
	if usage == nil {
		primaryJSON, err = sonic.Marshal(response)
		if err != nil {
			return nil, nil, err
		}
		if includeUsage {
			primaryJSON = appendUsageNull(primaryJSON)
		}
		return primaryJSON, nil, nil
	}

	if len(response.Choices) > 0 {
		primary := *response
		primary.Usage = nil
		primaryJSON, err = sonic.Marshal(&primary)
		if err != nil {
			return nil, nil, err
		}
		if includeUsage {
			primaryJSON = appendUsageNull(primaryJSON)
		}
	}

	if !includeUsage {
		return primaryJSON, nil, nil
	}

	usageOnly := *response
	usageOnly.Choices = []schemas.BifrostResponseChoice{}
	usageJSON, err = sonic.Marshal(&usageOnly)
	if err != nil {
		return nil, nil, err
	}
	return primaryJSON, usageJSON, nil
}

// appendUsageNull adds the spec-required null usage member to ordinary chunks
// when include_usage is enabled. sonic always emits a JSON object here. Keeping
// this as a direct append avoids a map conversion and its hot-path allocations.
func appendUsageNull(encoded []byte) []byte {
	if len(encoded) == 0 || encoded[len(encoded)-1] != '}' {
		return encoded
	}
	encoded = encoded[:len(encoded)-1]
	return append(encoded, usageNullJSONSuffix...)
}
