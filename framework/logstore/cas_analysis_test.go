package logstore

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCASAnalysisUsesProductionCodec(t *testing.T) {
	cases := [][]byte{
		[]byte("[1, 2, 3] \n\t"),
		[]byte(`[{"role":"user","content":"same"},{"role":"user","content":"same"}]`),
		[]byte("not-json-fallback"),
		{0xff, 0xfe, 0x00, 'x'},
	}
	for _, raw := range cases {
		analysis, err := AnalyzeCASField(raw, 4)
		require.NoError(t, err)
		rebuilt, err := analysis.Reconstruct()
		require.NoError(t, err)
		require.True(t, bytes.Equal(raw, rebuilt))
		require.NotEmpty(t, analysis.Objects)
		require.NotEmpty(t, analysis.ManifestHash())
		for _, object := range analysis.Objects {
			require.Equal(t, "zstd", object.Codec)
			require.Equal(t, object.CompressedBytes, int64(len(object.Data)))
		}
	}
}

func TestCASRowPayloadForAnalysisPreservesProductionPreviewAndSummary(t *testing.T) {
	entry := bigChatEntry("analysis-preview", "first", "assistant", "last user")
	require.NoError(t, entry.SerializeFields())
	serialized := ExtractPayload(entry)
	row, summary, hasObject, err := CASRowPayloadForAnalysis(serialized, entry.ContentSummary, false, []string{"input_history", "tools"})
	require.NoError(t, err)
	require.True(t, hasObject)
	require.Equal(t, entry.ContentSummary, summary)
	require.NotEmpty(t, row["input_history"])
	require.Less(t, len(row["input_history"]), len(serialized["input_history"]))
	require.Empty(t, row["tools"])
}

func TestCASObjectStoreForAnalysisReusesLookup(t *testing.T) {
	raw := []byte(`[{"x":"one"},{"x":"two"}]`)
	analysis, err := AnalyzeCASField(raw, 4)
	require.NoError(t, err)
	objects := make(map[string]CASAnalysisObject, len(analysis.Objects))
	for _, object := range analysis.Objects {
		objects[object.Hash] = object
	}
	store := NewCASObjectStoreForAnalysis(objects)
	for i := 0; i < 3; i++ {
		rebuilt, rebuildErr := store.Reconstruct(analysis.ManifestHash())
		require.NoError(t, rebuildErr)
		require.Equal(t, raw, rebuilt)
	}
}

func TestCASAnalysisFallbackAndColumns(t *testing.T) {
	analysis, err := AnalyzeCASField([]byte("opaque"), 2)
	require.NoError(t, err)
	require.True(t, analysis.Fallback)

	allColumns := PayloadColumnsForAnalysis()
	require.Contains(t, allColumns, "token_usage")
	require.Contains(t, allColumns, "cache_debug")
	columns := CASPayloadColumnsForAnalysis()
	require.NotContains(t, columns, "token_usage")
	require.NotContains(t, columns, "cache_debug")
	require.Contains(t, columns, "input_history")
	require.Contains(t, columns, "output_message")
}

func TestCASAnalysisRejectsInvalidChunkThreshold(t *testing.T) {
	_, err := AnalyzeCASField([]byte("[]"), 0)
	require.Error(t, err)
}
