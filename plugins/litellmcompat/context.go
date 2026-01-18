package litellmcompat

import "github.com/maximhq/bifrost/core/schemas"

// TransformContextKey is the key used to store TransformContext in BifrostContext
const TransformContextKey schemas.BifrostContextKey = "litellmcompat-transform-context"

// TransformContext tracks what transformations were applied to a request
// so they can be reversed on the response
type TransformContext struct {
	// Text-to-chat transform state
	// TextToChatApplied indicates that a text completion request was converted to chat
	TextToChatApplied bool
	// OriginalRequestType stores the original request type before transformation
	OriginalRequestType schemas.RequestType
	// OriginalModel preserves the original model string for response metadata
	OriginalModel string
	// IsStreaming indicates if the original request was a streaming request
	IsStreaming bool
}
