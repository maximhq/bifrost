package jsonparser

import "github.com/maximhq/bifrost/core/schemas"

// deepCopyBifrostResponse creates a deep copy of BifrostResponse to avoid modifying the original
func (p *JsonParserPlugin) deepCopyBifrostResponse(original *schemas.BifrostResponse) *schemas.BifrostResponse {
	if original == nil {
		return nil
	}

	// Create a new BifrostResponse
	result := &schemas.BifrostResponse{
		ID:                original.ID,
		Object:            original.Object,
		Model:             original.Model,
		Created:           original.Created,
		ServiceTier:       original.ServiceTier,
		SystemFingerprint: original.SystemFingerprint,
		Usage:             original.Usage,       // Shallow copy - usage shouldn't be modified
		ExtraFields:       original.ExtraFields, // Shallow copy
	}

	// Deep copy Choices slice (this is what we're interested in for the JSON parser)
	if original.Choices != nil {
		result.Choices = make([]schemas.BifrostResponseChoice, len(original.Choices))
		for i, choice := range original.Choices {
			result.Choices[i] = p.deepCopyBifrostResponseChoice(choice)
		}
	}

	// Shallow copy other response types since we don't modify them
	result.Data = original.Data
	result.Speech = original.Speech
	result.Transcribe = original.Transcribe

	return result
}

// deepCopyBifrostResponseChoice creates a deep copy of BifrostResponseChoice
func (p *JsonParserPlugin) deepCopyBifrostResponseChoice(original schemas.BifrostResponseChoice) schemas.BifrostResponseChoice {
	result := schemas.BifrostResponseChoice{
		Index:        original.Index,
		FinishReason: original.FinishReason,
	}

	// Deep copy BifrostStreamResponseChoice if it exists (this is what we modify for streaming)
	if original.BifrostStreamResponseChoice != nil {
		result.BifrostStreamResponseChoice = p.deepCopyBifrostStreamResponseChoice(original.BifrostStreamResponseChoice)
	}

	// Shallow copy BifrostNonStreamResponseChoice since we don't modify it
	result.BifrostNonStreamResponseChoice = original.BifrostNonStreamResponseChoice

	return result
}

// deepCopyBifrostStreamResponseChoice creates a deep copy of BifrostStreamResponseChoice
func (p *JsonParserPlugin) deepCopyBifrostStreamResponseChoice(original *schemas.BifrostStreamResponseChoice) *schemas.BifrostStreamResponseChoice {
	if original == nil {
		return nil
	}

	result := &schemas.BifrostStreamResponseChoice{}

	// Deep copy Delta if it exists (this is what we modify)
	result.Delta = p.deepCopyBifrostStreamDelta(original.Delta)

	return result
}

// deepCopyBifrostStreamDelta creates a deep copy of BifrostStreamDelta
func (p *JsonParserPlugin) deepCopyBifrostStreamDelta(original schemas.BifrostStreamDelta) schemas.BifrostStreamDelta {
	result := schemas.BifrostStreamDelta{
		Role:      original.Role,
		Thought:   original.Thought,   // Shallow copy
		Refusal:   original.Refusal,   // Shallow copy
		ToolCalls: original.ToolCalls, // Shallow copy - we don't modify tool calls
	}

	// Deep copy Content pointer if it exists (this is what we modify)
	if original.Content != nil {
		contentCopy := *original.Content
		result.Content = &contentCopy
	}

	return result
}
