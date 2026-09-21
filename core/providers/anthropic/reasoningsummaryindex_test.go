package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToBifrostResponsesStream_ReasoningSummaryIndex pins summary_index on the
// reasoning_summary_* events. The Responses streaming spec marks it required and
// non-nullable, and clients index the item's summary array with it, so emitting the
// events without it breaks strict consumers mid-stream.
func TestToBifrostResponsesStream_ReasoningSummaryIndex(t *testing.T) {
	t.Parallel()

	responses := driveResponsesStream(t, visibleThinkingToolUseLifecycle(
		[]string{"Let me figure out ", "which tool to call."}, "sig-test-123"))

	var deltas, dones int
	for _, response := range responses {
		switch response.Type {
		case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
			deltas++
			if response.SummaryIndex == nil {
				t.Fatalf("reasoning_summary_text.delta must carry summary_index")
			}
			if *response.SummaryIndex != 0 {
				t.Errorf("summary_index = %d, want 0", *response.SummaryIndex)
			}
		case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone:
			dones++
			if response.SummaryIndex == nil {
				t.Fatalf("reasoning_summary_text.done must carry summary_index")
			}
			if *response.SummaryIndex != 0 {
				t.Errorf("summary_index = %d, want 0", *response.SummaryIndex)
			}
		}
	}

	// Two thinking deltas plus the signature delta, which rides the same event type.
	if deltas != 3 {
		t.Errorf("reasoning_summary_text.delta count = %d, want 3", deltas)
	}
	if dones != 1 {
		t.Errorf("reasoning_summary_text.done count = %d, want 1", dones)
	}
}
