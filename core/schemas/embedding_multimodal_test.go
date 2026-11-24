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

// A 2D embedding with its chunk texts must survive the JSON round trip that cached and stored
// responses take, with the chunks still aligned to the rows.
func TestEmbeddingDataKeepsChunksOnRoundTrip(t *testing.T) {
	in := EmbeddingData{
		Index:     0,
		Object:    "embedding",
		Embedding: EmbeddingStruct{Embedding2DArray: [][]float64{{0.1, 0.2}, {0.3, 0.4}}},
		Chunks:    []string{"first chunk", "second chunk"},
	}
	raw, err := MarshalSorted(in)
	require.NoError(t, err)

	var out EmbeddingData
	require.NoError(t, Unmarshal(raw, &out))
	require.Equal(t, in.Chunks, out.Chunks)
	require.Equal(t, in.Embedding.Embedding2DArray, out.Embedding.Embedding2DArray)

	withoutChunks, err := MarshalSorted(EmbeddingData{Object: "embedding", Embedding: EmbeddingStruct{EmbeddingArray: []float64{1}}})
	require.NoError(t, err)
	require.NotContains(t, string(withoutChunks), "chunks")
}
