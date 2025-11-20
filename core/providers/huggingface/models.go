package huggingface

import (
	"slices"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	defaultModelFetchLimit = 200
	maxModelFetchLimit     = 1000
)

func (provider *HuggingFaceProvider) extractCursor(resp *fasthttp.Response) string {
	if cursor := resp.Header.Peek("X-Next-Page"); len(cursor) > 0 {
		return string(cursor)
	}
	return ""
}

func (response *HuggingFaceListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}
	for _, model := range response.Models {
		if model.ModelID == "" {
			continue
		}

		supported := deriveSupportedMethods(model.PipelineTag, model.Tags)
		if len(supported) == 0 {
			continue
		}

		newModel := schemas.Model{
			ID:               model.ModelID,
			Name:             schemas.Ptr(model.ModelID),
			SupportedMethods: supported,
			HuggingFaceID:    schemas.Ptr(model.ID),
		}

		if model.PipelineTag != "" {
			newModel.Architecture = &schemas.Architecture{
				Modality: schemas.Ptr(model.PipelineTag),
			}
		}

		bifrostResponse.Data = append(bifrostResponse.Data, newModel)
	}
	return bifrostResponse
}

func deriveSupportedMethods(pipeline string, tags []string) []string {
	normalized := strings.TrimSpace(strings.ToLower(pipeline))

	methodsSet := map[schemas.RequestType]struct{}{}

	addMethods := func(methods ...schemas.RequestType) {
		for _, method := range methods {
			methodsSet[method] = struct{}{}
		}
	}

	switch normalized {
	case "text-generation", "text2text-generation", "summarization", "conversational", "chat-completion":
		addMethods(schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest)
	case "text-embedding", "sentence-similarity", "feature-extraction", "embeddings":
		addMethods(schemas.EmbeddingRequest)
	}

	for _, tag := range tags {
		switch strings.ToLower(tag) {
		case "text-embedding", "sentence-similarity", "feature-extraction", "embeddings":
			addMethods(schemas.EmbeddingRequest)
		case "text-generation", "summarization", "conversational", "chat-completion":
			addMethods(schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest)
		}
	}

	if len(methodsSet) == 0 {
		return nil
	}

	methods := make([]string, 0, len(methodsSet))
	for method := range methodsSet {
		methods = append(methods, string(method))
	}

	slices.Sort(methods)
	return methods
}
