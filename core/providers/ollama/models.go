// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains converters for list models requests and responses.
package ollama

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOllamaModel converts a Bifrost model to Ollama format.
// Note: Ollama's /api/tags endpoint is GET-only and doesn't need a request body.
// This function is included for completeness and potential future use.
func ToOllamaModel(bifrostModel *schemas.Model) *OllamaModel {
	if bifrostModel == nil {
		return nil
	}

	return &OllamaModel{
		Name:  bifrostModel.ID,
		Model: bifrostModel.ID,
	}
}

// ToBifrostModel converts an Ollama model to Bifrost format.
func (m *OllamaModel) ToBifrostModel() *schemas.Model {
	if m == nil {
		return nil
	}

	created := m.ModifiedAt.Unix()
	ownedBy := "ollama"

	return &schemas.Model{
		ID:      m.Name,
		Created: &created,
		OwnedBy: &ownedBy,
	}
}

// GetModelInfo returns formatted model information for display.
func (m *OllamaModel) GetModelInfo() map[string]interface{} {
	if m == nil {
		return nil
	}

	info := map[string]interface{}{
		"name":        m.Name,
		"model":       m.Model,
		"modified_at": m.ModifiedAt,
		"size":        m.Size,
		"digest":      m.Digest,
	}

	if m.Details.Family != "" {
		info["family"] = m.Details.Family
	}
	if m.Details.ParameterSize != "" {
		info["parameter_size"] = m.Details.ParameterSize
	}
	if m.Details.QuantizationLevel != "" {
		info["quantization_level"] = m.Details.QuantizationLevel
	}
	if m.Details.Format != "" {
		info["format"] = m.Details.Format
	}

	return info
}
