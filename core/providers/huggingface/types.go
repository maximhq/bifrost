package huggingface

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

type apiModelEntry struct {
	ID          string      `json:"id"`
	ModelID     string      `json:"modelId"`
	Author      string      `json:"author"`
	PipelineTag string      `json:"pipeline_tag"`
	Tags        []string    `json:"tags"`
	Private     bool        `json:"private"`
	Gated       interface{} `json:"gated"` // Can be bool or string like "manual"
	LibraryName string      `json:"library_name"`
	CreatedAt   string      `json:"createdAt"`
	Likes       int         `json:"likes"`
	Downloads   int         `json:"downloads"`
}

type huggingFaceModelHubEntry struct {
	ID          string                   `json:"id"`
	ModelID     string                   `json:"modelId"`
	Author      string                   `json:"author"`
	PipelineTag string                   `json:"pipeline_tag"`
	Tags        []string                 `json:"tags"`
	Private     bool                     `json:"private"`
	Gated       bool                     `json:"gated"`
	CardData    huggingFaceModelCardData `json:"cardData"`
}

type huggingFaceModelCardData struct {
	ModelName        string `json:"model_name"`
	ShortDescription string `json:"short_description"`
	Description      string `json:"description"`
	Summary          string `json:"summary"`
}

type huggingFaceErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// huggingFaceEmbeddingRequest represents the request format for HuggingFace feature extraction API
type huggingFaceEmbeddingRequest struct {
	Inputs         interface{} `json:"inputs"` // Can be string or []string
	Normalize      *bool       `json:"normalize,omitempty"`
	PromptName     *string     `json:"prompt_name,omitempty"`
	Truncate       *bool       `json:"truncate,omitempty"`
	TruncDirection *string     `json:"truncation_direction,omitempty"`
}

// huggingFaceEmbeddingResponse represents the response from HuggingFace feature extraction API
// The response is a 2D array of floats ([][]float32)
type huggingFaceEmbeddingResponse [][]float32

func convertHubEntriesToModels(entries []huggingFaceModelHubEntry, provider schemas.ModelProvider) []schemas.Model {
	models := make([]schemas.Model, 0, len(entries))
	for _, entry := range entries {
		if entry.ModelID == "" {
			continue
		}

		supported := deriveSupportedMethods(entry.PipelineTag, entry.Tags)
		if len(supported) == 0 {
			continue
		}

		identifier := string(provider) + "/" + entry.ModelID
		canonical := modelHubBaseURL + "/" + entry.ModelID

		description := entry.CardData.Description
		if description == "" {
			description = entry.CardData.ShortDescription
		}
		if description == "" {
			description = entry.CardData.Summary
		}

		name := entry.CardData.ModelName
		if name == "" {
			name = entry.ModelID
		}

		model := schemas.Model{
			ID:               identifier,
			CanonicalSlug:    schemas.Ptr(canonical),
			Name:             schemas.Ptr(name),
			Deployment:       schemas.Ptr(entry.ModelID),
			SupportedMethods: supported,
			HuggingFaceID:    schemas.Ptr(entry.ModelID),
		}

		if entry.Author != "" {
			model.OwnedBy = schemas.Ptr(entry.Author)
		}

		if description != "" {
			model.Description = schemas.Ptr(description)
		}

		if entry.PipelineTag != "" {
			model.Architecture = &schemas.Architecture{
				Modality: schemas.Ptr(entry.PipelineTag),
			}
		}

		models = append(models, model)
	}

	slices.SortFunc(models, func(a, b schemas.Model) int {
		return strings.Compare(a.ID, b.ID)
	})

	return models
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
