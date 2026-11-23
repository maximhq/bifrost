package nace

import "github.com/maximhq/bifrost/core/providers/typesafe"

// NaceDecisionResponse retains Drex metadata alongside its System One answers.
type NaceDecisionResponse struct {
	Model     string                `json:"model"`
	RequestID string                `json:"request_id"`
	Answers   map[string]NaceAnswer `json:"answers"`
	Usage     *NaceUsage            `json:"usage,omitempty"`
}

// NaceAnswer adapts Drex's ordered score legend to the shared converter.
type NaceAnswer struct {
	typesafe.TypesafeAnswer
	Legend []string `json:"legend,omitempty"`
}

// NaceError is Drex's public error body.
type NaceError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// NaceDecisionRequest adds Drex's ordered images to the shared System One body.
type NaceDecisionRequest struct {
	*typesafe.TypesafeDecisionRequest
	Images []string `json:"images,omitempty"`
}

// NaceUsage includes image tokens, already counted in input_tokens.
type NaceUsage struct {
	typesafe.TypesafeUsage
	ImageTokens int `json:"image_tokens,omitempty"`
}

// NaceModel is a native GET /v1/models entry, including alias targets.
type NaceModel struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ReleaseDate string  `json:"release_date"`
	AliasFor    *string `json:"alias_for"`
}

// NaceListModelsResponse is the native catalog envelope.
type NaceListModelsResponse struct {
	Models []NaceModel `json:"models"`
}
