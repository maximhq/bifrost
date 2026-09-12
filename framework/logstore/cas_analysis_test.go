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
