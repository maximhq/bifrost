package nace

import "github.com/maximhq/bifrost/core/providers/typesafe"

// NaceDecisionResponse retains Drex metadata alongside its System One answers.
type NaceDecisionResponse struct {
	Model     string                  `json:"model"`
	RequestID string                  `json:"request_id"`
	Answers   map[string]NaceAnswer   `json:"answers"`
	Usage     *typesafe.TypesafeUsage `json:"usage,omitempty"`
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
