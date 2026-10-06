package handlers

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveTranscriptGroupsFragmentsIntoSpeakerTurns(t *testing.T) {
	t.Parallel()

	var transcript liveTranscript
	assert.Nil(t, transcript.snapshot())

	// The two speakers overlap on the wire: the user's last word arrives after the assistant has
	// started. Timing, not arrival order, decides which turn a fragment belongs to.
	transcript.append("user", "Hi there. In one sentence What can you help me", 0, 3000)
	transcript.append("assistant", "I can", 3000, 3100)
	transcript.append("user", " with", 3100, 3300)
	transcript.append("assistant", " help you with quick answers.", 3300, 10000)
	// The assistant pauses well past the gap and carries on; nobody spoke in between, so it is
	// still the same turn. A new response starts without a leading space; the join adds one.
	transcript.append("assistant", "Anything else?", 14000, 15000)
	transcript.append("user", "", 20000, 20000)
	transcript.append("user", "What is the weather in Paris?", 20000, 23000)

	lines := transcript.snapshot()
	require.Len(t, lines, 3, "only another speaker starts a turn; an interleaved fragment and an empty one do not")
	assert.Equal(t, schemas.LiveTranscriptLine{Role: "user", Text: "Hi there. In one sentence What can you help me with", StartMs: 0, EndMs: 3300}, lines[0])
	assert.Equal(t, schemas.LiveTranscriptLine{Role: "assistant", Text: "I can help you with quick answers. Anything else?", StartMs: 3000, EndMs: 15000}, lines[1])
	assert.Equal(t, schemas.LiveTranscriptLine{Role: "user", Text: "What is the weather in Paris?", StartMs: 20000, EndMs: 23000}, lines[2])

	// Past the cap, later fragments are dropped and what was kept stays intact.
	transcript.append("assistant", strings.Repeat("x", liveTranscriptMaxBytes), 24000, 25000)
	assert.Len(t, transcript.snapshot(), 3)
}
