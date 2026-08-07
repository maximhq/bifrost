package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Bedrock rejects a Converse request carrying more than 4 cachePoint markers outright —
// "ValidationException: A maximum of 4 blocks with cache_control may be provided. Found 5.",
// verified live against global.anthropic.claude-haiku-4-5. These tests pin the clamp that keeps an
// over-marked request from failing, and pin WHICH markers it sacrifices.
//
// The direction is the design. Converse caches cumulatively up to each cachePoint, so a marker
// later in render order (toolConfig -> system -> messages) anchors a strictly longer prefix.
// Dropping the earliest costs an intermediate checkpoint; dropping the latest would surrender the
// longest cached prefix — reintroducing exactly the collapse
// convertBifrostSystemReminderToBedrockUserMessage was fixed to prevent. A clamp trimming the
// wrong end would eliminate the 400s while silently restoring the bug.

func cachePointBlock() BedrockContentBlock {
	return BedrockContentBlock{CachePoint: &BedrockCachePoint{Type: BedrockCachePointTypeDefault}}
}

func textBlock(s string) BedrockContentBlock {
	return BedrockContentBlock{Text: schemas.Ptr(s)}
}

// countCachePointsInRequest returns markers per render region plus the total.
//
// `messages` includes markers nested inside ToolResult.Content. AWS counts checkpoints across
// `messages` as a whole, so a helper that walked only direct content would report a total the
// provider disagrees with — and every test built on it would be blind to a nested-counting
// regression, which is the exact class of bug clampBedrockCachePoints originally shipped with.
func countCachePointsInRequest(req *BedrockConverseRequest) (tools, system, messages, total int) {
	if req.ToolConfig != nil {
		for _, t := range req.ToolConfig.Tools {
			if t.CachePoint != nil {
				tools++
			}
		}
	}
	for _, s := range req.System {
		if s.CachePoint != nil {
			system++
		}
	}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.CachePoint != nil {
				messages++
			}
			if b.ToolResult != nil {
				for _, inner := range b.ToolResult.Content {
					if inner.CachePoint != nil {
						messages++
					}
				}
			}
		}
	}
	return tools, system, messages, tools + system + messages
}

// buildOverMarkedRequest returns a request with 2 system + 1 tool-result + 2 message markers = 5,
// one past the cap. The shape mirrors real Claude Code traffic, which already sits at exactly 4.
func buildOverMarkedRequest() *BedrockConverseRequest {
	return &BedrockConverseRequest{
		System: []BedrockSystemMessage{
			{Text: schemas.Ptr("system prompt")},
			{CachePoint: &BedrockCachePoint{Type: BedrockCachePointTypeDefault}},
			{Text: schemas.Ptr("tool definitions")},
			{CachePoint: &BedrockCachePoint{Type: BedrockCachePointTypeDefault}},
		},
		Messages: []BedrockMessage{
			{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{
				textBlock("tool result"), cachePointBlock(),
			}},
			{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{
				textBlock("second turn"), cachePointBlock(),
			}},
			{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{
				textBlock("<system-reminder>\nstay concise\n</system-reminder>\n"), cachePointBlock(),
			}},
		},
	}
}

// TestClampBedrockCachePoints_UnderCapUntouched guards against over-eager clamping: at or below
// the cap nothing may be removed, since every marker there is free cache coverage.
func TestClampBedrockCachePoints_UnderCapUntouched(t *testing.T) {
	req := &BedrockConverseRequest{
		System: []BedrockSystemMessage{
			{Text: schemas.Ptr("a")},
			{CachePoint: &BedrockCachePoint{Type: BedrockCachePointTypeDefault}},
		},
		Messages: []BedrockMessage{
			{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{textBlock("x"), cachePointBlock()}},
		},
	}
	dropped := clampBedrockCachePoints(req)
	assert.Equal(t, 0, dropped, "a request under the cap must not be trimmed")
	_, _, _, total := countCachePointsInRequest(req)
	assert.Equal(t, 2, total, "all markers must survive")
	require.Len(t, req.Messages[0].Content, 2, "non-marker blocks must be untouched")
}

// TestClampBedrockCachePoints_DropsEarliestKeepsAnchor is the load-bearing case: 5 markers get
// trimmed to 4, the earliest goes, and the reminder's anchor on the final turn survives.
func TestClampBedrockCachePoints_DropsEarliestKeepsAnchor(t *testing.T) {
	req := buildOverMarkedRequest()
	_, _, _, before := countCachePointsInRequest(req)
	require.Equal(t, 5, before, "fixture should start one over the cap")

	dropped := clampBedrockCachePoints(req)
	assert.Equal(t, 1, dropped)

	tools, system, messages, total := countCachePointsInRequest(req)
	assert.Equal(t, BedrockMaxCachePoints, total, "must land exactly on the cap")
	assert.Equal(t, 0, tools)
	assert.Equal(t, 1, system, "the earlier of the two system markers is the one sacrificed")
	assert.Equal(t, 3, messages, "every message-level marker must survive")

	// The last message's anchor is what keeps the conversation body cacheable.
	last := req.Messages[len(req.Messages)-1]
	require.NotEmpty(t, last.Content)
	assert.NotNil(t, last.Content[len(last.Content)-1].CachePoint,
		"the final breakpoint was dropped — clamping must never surrender the longest cached prefix")

	// Text content must be preserved: only the marker is removed, never the payload.
	var texts int
	for _, s := range req.System {
		if s.Text != nil {
			texts++
		}
	}
	assert.Equal(t, 2, texts, "clamping must not delete system text, only markers")
}

// TestClampBedrockCachePoints_DropsCachePointOnlyEntries verifies the removal leaves no empty
// objects behind — a system entry that was marker-only has nothing left to serialize, and an
// empty {} in the system array is itself a validation error.
func TestClampBedrockCachePoints_DropsCachePointOnlyEntries(t *testing.T) {
	req := buildOverMarkedRequest()
	beforeSystem := len(req.System)

	clampBedrockCachePoints(req)

	assert.Equal(t, beforeSystem-1, len(req.System),
		"a marker-only system entry must be removed outright, not left as an empty object")
	for i, s := range req.System {
		assert.False(t, s.Text == nil && s.GuardContent == nil && s.CachePoint == nil,
			"system[%d] is an empty object", i)
	}
}

// TestClampBedrockCachePoints_NilSafe guards trivial inputs.
func TestClampBedrockCachePoints_NilSafe(t *testing.T) {
	assert.Equal(t, 0, clampBedrockCachePoints(nil))
	assert.Equal(t, 0, clampBedrockCachePoints(&BedrockConverseRequest{}))
}

// TestClampBedrockCachePoints_CountsNestedToolResultMarkers covers the case flagged in review on
// PR #5931: convertToolMessages emits a CachePoint *inside* ToolResult.Content whenever a client
// puts cache_control on a tool-result block (utils.go, the ChatContentBlockTypeText and
// ChatContentBlockTypeImage arms). AWS counts checkpoints across `messages` as a whole, so those
// nested markers spend the same 4-per-request budget as direct ones.
//
// This fixture is over the cap ONLY because of the nested marker — 2 system + 2 direct message
// markers + 1 nested = 5. A clamp that walks direct content alone sees 4, concludes there is
// nothing to do, and forwards 5 to Bedrock, which rejects the request with the very
// ValidationException the clamp exists to avoid.
// Both sibling passes (stripCachePointsFromBedrockRequest, downgradeExtendedCacheTTLInBedrockRequest)
// already recurse here; this pins the clamp to the same shape.
func TestClampBedrockCachePoints_CountsNestedToolResultMarkers(t *testing.T) {
	req := &BedrockConverseRequest{
		System: []BedrockSystemMessage{
			{Text: schemas.Ptr("system prompt")},
			{CachePoint: &BedrockCachePoint{Type: BedrockCachePointTypeDefault}},
			{Text: schemas.Ptr("tool definitions")},
			{CachePoint: &BedrockCachePoint{Type: BedrockCachePointTypeDefault}},
		},
		Messages: []BedrockMessage{
			{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{
				// A tool result whose NESTED content carries a marker.
				{ToolResult: &BedrockToolResult{
					ToolUseID: "tooluse_1",
					Content: []BedrockContentBlock{
						textBlock("tool output"),
						cachePointBlock(),
					},
				}},
				cachePointBlock(),
			}},
			{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{
				textBlock("<system-reminder>\nstay concise\n</system-reminder>\n"), cachePointBlock(),
			}},
		},
	}

	// 2 system + 2 direct + 1 nested = 5, one over the cap. countCachePointsInRequest folds
	// nested tool-result markers into its `messages` figure, matching how AWS counts them.
	_, systemBefore, messagesBefore, totalBefore := countCachePointsInRequest(req)
	require.Equal(t, 2, systemBefore)
	require.Equal(t, 3, messagesBefore, "2 direct + 1 nested tool-result marker")
	require.Equal(t, 5, totalBefore, "fixture should start one over the cap")

	dropped := clampBedrockCachePoints(req)
	assert.Equal(t, 1, dropped, "the nested marker must be counted, so exactly one must be dropped")

	_, systemAfter, messagesAfter, totalAfter := countCachePointsInRequest(req)
	assert.Equal(t, BedrockMaxCachePoints, totalAfter,
		"total markers including nested tool-result content must land on the cap")
	assert.Equal(t, 3, messagesAfter, "no message-level marker, direct or nested, may be dropped here")

	// Earliest-first: the sacrificed marker is a system one, and the conversation anchor survives.
	assert.Equal(t, 1, systemAfter, "the earlier system marker is the one dropped")
	last := req.Messages[len(req.Messages)-1]
	assert.NotNil(t, last.Content[len(last.Content)-1].CachePoint,
		"the final breakpoint must survive — it anchors the longest cached prefix")

	// Nested payload text must be preserved; only markers are removable.
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.ToolResult == nil {
				continue
			}
			var hasText bool
			for _, inner := range b.ToolResult.Content {
				if inner.Text != nil {
					hasText = true
				}
			}
			assert.True(t, hasText, "clamping must not delete tool-result text, only markers")
		}
	}
}
