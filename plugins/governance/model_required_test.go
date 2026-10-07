package governance

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Request types whose model is optional are excluded so the resolver evaluates a model allowlist
// only when the caller actually supplied a model. Video edit belongs to that set: the OpenAI SDKs
// send no model on that route and the provider infers it from the source video, so requiring one
// would reject a legitimate request with "Model ” is not allowed".
func TestIsModelRequiredForRequest_OptionalModelTypes(t *testing.T) {
	for _, requestType := range []schemas.RequestType{
		schemas.VideoEditRequest,
		schemas.PassthroughRequest,
		schemas.PassthroughStreamRequest,
		schemas.VideoRetrieveRequest,
		schemas.VideoDownloadRequest,
	} {
		if IsModelRequiredForRequest(requestType) {
			t.Errorf("%s: model must not be required", requestType)
		}
	}

	// Request types that always carry a model stay required, so their allowlist is always enforced.
	for _, requestType := range []schemas.RequestType{
		schemas.ChatCompletionRequest,
		schemas.ResponsesRequest,
		schemas.VideoGenerationRequest,
		schemas.ImageEditRequest,
	} {
		if !IsModelRequiredForRequest(requestType) {
			t.Errorf("%s: model must stay required", requestType)
		}
	}
}
