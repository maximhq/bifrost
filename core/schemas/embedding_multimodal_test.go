package schemas

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddingInputValidateRejectsEmpty(t *testing.T) {
	err := ValidateEmbeddingInput(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

func TestEmbeddingContentPartValidateRejectsMultipleModalities(t *testing.T) {
	text := "bad"
	part := EmbeddingContentPart{
		Type:  EmbeddingContentPartTypeImage,
		Text:  &text,
		Image: &EmbeddingMediaPart{URL: Ptr("https://example.com/img.png")},
	}

	err := part.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one modality")
}
