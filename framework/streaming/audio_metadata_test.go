package streaming

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestBuildCompleteMessageFromAudioStreamChunksPreservesTerminalMetadata(t *testing.T) {
	accumulator := &Accumulator{}
	subtitle := "https://example.com/subtitles.json"
	usage := &schemas.SpeechUsage{InputChars: 42}
	chunks := []*AudioStreamChunk{
		{
			ChunkIndex: 1,
			Delta: &schemas.BifrostSpeechStreamResponse{
				Type:         schemas.SpeechStreamResponseTypeDone,
				Usage:        usage,
				SubtitleFile: &subtitle,
			},
		},
		{
			ChunkIndex: 0,
			Delta: &schemas.BifrostSpeechStreamResponse{
				Type:  schemas.SpeechStreamResponseTypeDelta,
				Audio: []byte("audio"),
			},
		},
	}

	complete := accumulator.buildCompleteMessageFromAudioStreamChunks(chunks)
	require.Equal(t, []byte("audio"), complete.Audio)
	require.Same(t, usage, complete.Usage)
	require.NotNil(t, complete.SubtitleFile)
	require.Equal(t, subtitle, *complete.SubtitleFile)
}
